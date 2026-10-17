package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type chatStreamOutputRef struct {
	kind      string
	toolIndex int
}

type chatStreamTool struct {
	chatIndex   int
	outputIndex int
	itemID      string
	callID      string
	name        string
	kind        string
	arguments   strings.Builder
	pending     strings.Builder
	announced   bool
	done        bool
}

type chatToResponsesStream struct {
	stream          *ResponsesStream
	toolTypes       map[string]string
	model           string
	id              string
	created         int64
	nextOutputIndex int
	textItemID      string
	textOutputIndex int
	textStarted     bool
	textPartStarted bool
	textClosed      bool
	text            strings.Builder
	reasoning       strings.Builder
	tools           map[int]*chatStreamTool
	toolOrder       []int
	outputOrder     []chatStreamOutputRef
	usage           map[string]any
	finishReason    string
}

func StreamChatToResponses(reader io.Reader, stream *ResponsesStream, model string, toolTypes map[string]string) error {
	if stream == nil {
		return errors.New("responses stream is required")
	}
	converter := &chatToResponsesStream{
		stream:    stream,
		toolTypes: toolTypes,
		model:     model,
		tools:     make(map[int]*chatStreamTool),
	}
	buffered := bufio.NewReaderSize(reader, 64*1024)
	var data []string
	for {
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if consumeErr := converter.consume(data); consumeErr != nil {
					return consumeErr
				}
				data = nil
			} else if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if err := converter.consume(data); err != nil {
		return err
	}
	return converter.finish()
}

func (c *chatToResponsesStream) consume(data []string) error {
	if len(data) == 0 {
		return nil
	}
	value := strings.TrimSpace(strings.Join(data, "\n"))
	if value == "" || value == "[DONE]" {
		return nil
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(value), &chunk); err != nil {
		return err
	}
	if errorObject := mapAt(chunk, "error"); errorObject != nil {
		message := firstString(stringAt(errorObject, "message"), stringify(errorObject))
		if message == "" {
			message = "upstream chat stream failed"
		}
		return fmt.Errorf("%s", message)
	}
	if c.id == "" {
		c.id = stringAt(chunk, "id")
	}
	if c.model == "" {
		c.model = stringAt(chunk, "model")
	}
	if c.created == 0 {
		c.created = timestampValue(chunk["created"])
	}
	if usage := mapAt(chunk, "usage"); usage != nil {
		c.usage = usage
	}
	for _, rawChoice := range asSlice(chunk["choices"]) {
		choice := asMap(rawChoice)
		delta := mapAt(choice, "delta")
		if delta == nil {
			continue
		}
		if content, ok := delta["content"].(string); ok && content != "" {
			if err := c.textDelta(content); err != nil {
				return err
			}
		}
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			c.reasoning.WriteString(reasoning)
		} else if reasoning, ok := delta["reasoning"].(string); ok && reasoning != "" {
			c.reasoning.WriteString(reasoning)
		}
		for _, rawCall := range asSlice(delta["tool_calls"]) {
			call := asMap(rawCall)
			index := intValue(call["index"])
			function := mapAt(call, "function")
			if err := c.toolDelta(index, stringAt(call, "id"), stringAt(function, "name"), stringAt(function, "arguments")); err != nil {
				return err
			}
		}
		if finish := strings.TrimSpace(stringAt(choice, "finish_reason")); finish != "" {
			c.finishReason = finish
		}
	}
	return nil
}

func (c *chatToResponsesStream) textDelta(delta string) error {
	if !c.textStarted {
		c.textStarted = true
		c.textOutputIndex = c.nextOutputIndex
		c.nextOutputIndex++
		c.textItemID = newID("msg_")
		c.outputOrder = append(c.outputOrder, chatStreamOutputRef{kind: "text"})
		item := map[string]any{
			"id": c.textItemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{},
		}
		if err := c.stream.event("response.output_item.added", map[string]any{"output_index": c.textOutputIndex, "item": item}); err != nil {
			return err
		}
	}
	if !c.textPartStarted {
		c.textPartStarted = true
		part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}
		if err := c.stream.event("response.content_part.added", map[string]any{
			"item_id": c.textItemID, "output_index": c.textOutputIndex, "content_index": 0, "part": part,
		}); err != nil {
			return err
		}
	}
	c.text.WriteString(delta)
	return c.stream.event("response.output_text.delta", map[string]any{
		"item_id": c.textItemID, "output_index": c.textOutputIndex, "content_index": 0, "delta": delta, "logprobs": []any{},
	})
}

func (c *chatToResponsesStream) toolDelta(index int, id, name, arguments string) error {
	state := c.tools[index]
	if state == nil {
		state = &chatStreamTool{chatIndex: index}
		c.tools[index] = state
		c.toolOrder = append(c.toolOrder, index)
	}
	if id != "" {
		state.callID = id
	}
	if name != "" {
		if state.name == "" {
			state.name = name
		} else {
			state.name += name
		}
	}
	if arguments != "" {
		if state.announced {
			state.arguments.WriteString(arguments)
			return c.emitToolDelta(state, arguments)
		}
		state.pending.WriteString(arguments)
	}
	if !state.announced && state.callID != "" && state.name != "" {
		return c.announceTool(state)
	}
	return nil
}

func (c *chatToResponsesStream) announceTool(state *chatStreamTool) error {
	state.announced = true
	state.outputIndex = c.nextOutputIndex
	c.nextOutputIndex++
	state.kind = c.toolTypes[state.name]
	if state.kind != "custom" {
		state.kind = "function"
	}
	if state.kind == "custom" {
		state.itemID = newID("ctc_")
	} else {
		state.itemID = newID("fc_")
	}
	c.outputOrder = append(c.outputOrder, chatStreamOutputRef{kind: "tool", toolIndex: state.chatIndex})
	item := c.toolItem(state, "in_progress")
	if err := c.stream.event("response.output_item.added", map[string]any{"output_index": state.outputIndex, "item": item}); err != nil {
		return err
	}
	if state.pending.Len() > 0 {
		value := state.pending.String()
		state.pending.Reset()
		state.arguments.WriteString(value)
		return c.emitToolDelta(state, value)
	}
	return nil
}

func (c *chatToResponsesStream) emitToolDelta(state *chatStreamTool, delta string) error {
	if state.kind == "custom" {
		// Chat streams the custom-tool input through a JSON arguments wrapper.
		// Buffer it and emit one extracted custom-input delta at the end.
		return nil
	}
	return c.stream.event("response.function_call_arguments.delta", map[string]any{
		"item_id": state.itemID, "output_index": state.outputIndex, "delta": delta,
	})
}

func (c *chatToResponsesStream) toolItem(state *chatStreamTool, status string) map[string]any {
	itemType := "function_call"
	if state.kind == "custom" {
		itemType = "custom_tool_call"
	}
	item := map[string]any{
		"id":      state.itemID,
		"type":    itemType,
		"status":  status,
		"call_id": state.callID,
		"name":    state.name,
	}
	if state.kind == "custom" {
		item["input"] = customToolInput(state.arguments.String())
	} else {
		item["arguments"] = state.arguments.String()
	}
	return item
}

func (c *chatToResponsesStream) finish() error {
	if c.textStarted && !c.textClosed {
		c.textClosed = true
		text := c.text.String()
		if err := c.stream.event("response.output_text.done", map[string]any{
			"item_id": c.textItemID, "output_index": c.textOutputIndex, "content_index": 0, "text": text, "logprobs": []any{},
		}); err != nil {
			return err
		}
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
		if err := c.stream.event("response.content_part.done", map[string]any{
			"item_id": c.textItemID, "output_index": c.textOutputIndex, "content_index": 0, "part": part,
		}); err != nil {
			return err
		}
		item := messageOutputItem(text)
		item["id"] = c.textItemID
		if err := c.stream.event("response.output_item.done", map[string]any{"output_index": c.textOutputIndex, "item": item}); err != nil {
			return err
		}
	}
	for _, index := range c.toolOrder {
		state := c.tools[index]
		if state == nil || !state.announced || state.done {
			continue
		}
		state.done = true
		arguments := state.arguments.String()
		if state.kind == "custom" {
			input := customToolInput(arguments)
			if err := c.stream.event("response.custom_tool_call_input.delta", map[string]any{
				"item_id": state.itemID, "output_index": state.outputIndex, "delta": input,
			}); err != nil {
				return err
			}
			if err := c.stream.event("response.custom_tool_call_input.done", map[string]any{
				"item_id": state.itemID, "output_index": state.outputIndex, "input": input,
			}); err != nil {
				return err
			}
		} else {
			if err := c.stream.event("response.function_call_arguments.done", map[string]any{
				"item_id": state.itemID, "output_index": state.outputIndex, "arguments": arguments,
			}); err != nil {
				return err
			}
		}
		if err := c.stream.event("response.output_item.done", map[string]any{
			"output_index": state.outputIndex, "item": c.toolItem(state, "completed"),
		}); err != nil {
			return err
		}
	}
	output := make([]any, 0, len(c.outputOrder))
	for _, ref := range c.outputOrder {
		if ref.kind == "text" {
			item := messageOutputItem(c.text.String())
			item["id"] = c.textItemID
			output = append(output, item)
			continue
		}
		state := c.tools[ref.toolIndex]
		if state != nil && state.announced {
			output = append(output, c.toolItem(state, "completed"))
		}
	}
	if len(output) == 0 {
		output = append(output, messageOutputItem(""))
	}
	response := buildResponsesResponse(c.stream.ID(), c.model, c.created, output, c.text.String(), c.finishReason, chatUsageToResponses(c.usage))
	return c.stream.CompleteFinal(response)
}
