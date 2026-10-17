package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type ResponsesStream struct {
	writer   io.Writer
	flush    func()
	sequence int
	id       string
	model    string
	created  int64
	began    bool
	done     bool
}

func NewResponsesStream(writer io.Writer, flush func(), id, model string) *ResponsesStream {
	if strings.TrimSpace(id) == "" {
		id = NewResponseID()
	}
	if flush == nil {
		flush = func() {}
	}
	return &ResponsesStream{writer: writer, flush: flush, id: normalizeResponseID(id), model: model, created: time.Now().Unix()}
}

func (s *ResponsesStream) ID() string {
	return s.id
}

func (s *ResponsesStream) Begin() error {
	if s.began {
		return nil
	}
	s.began = true
	response := s.skeleton("in_progress", nil)
	if err := s.event("response.created", map[string]any{"response": response}); err != nil {
		return err
	}
	return s.event("response.in_progress", map[string]any{"response": response})
}

func (s *ResponsesStream) Complete(response map[string]any) error {
	if s.done {
		return nil
	}
	if !s.began {
		if err := s.Begin(); err != nil {
			return err
		}
	}
	completed := s.completedResponse(response)
	output := asSlice(completed["output"])
	for index, raw := range output {
		item := asMap(raw)
		if item == nil {
			continue
		}
		if err := s.startedItem(index, item); err != nil {
			return err
		}
		if err := s.itemEvents(index, item); err != nil {
			return err
		}
		if err := s.event("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
			return err
		}
	}
	s.done = true
	return s.completedEvent(completed)
}

// CompleteFinal emits only the terminal event. It is used by incremental
// stream converters that already emitted every output-item event.
func (s *ResponsesStream) CompleteFinal(response map[string]any) error {
	if s.done {
		return nil
	}
	if !s.began {
		if err := s.Begin(); err != nil {
			return err
		}
	}
	s.done = true
	return s.completedEvent(s.completedResponse(response))
}

func (s *ResponsesStream) completedResponse(response map[string]any) map[string]any {
	completed := cloneMap(response)
	if completed == nil {
		completed = map[string]any{}
	}
	if stringAt(completed, "id") == "" {
		completed["id"] = s.id
	}
	if intValue(completed["created_at"]) == 0 {
		completed["created_at"] = s.created
	}
	if stringAt(completed, "model") == "" {
		completed["model"] = s.model
	}
	if stringAt(completed, "status") == "" {
		completed["status"] = "completed"
	}
	if _, ok := completed["object"]; !ok {
		completed["object"] = "response"
	}
	return completed
}

func (s *ResponsesStream) completedEvent(completed map[string]any) error {
	eventType := "response.completed"
	switch stringAt(completed, "status") {
	case "incomplete":
		eventType = "response.incomplete"
	case "failed":
		eventType = "response.failed"
	}
	return s.event(eventType, map[string]any{"response": completed})
}

func (s *ResponsesStream) Fail(message string) error {
	if s.done {
		return nil
	}
	if !s.began {
		if err := s.Begin(); err != nil {
			return err
		}
	}
	s.done = true
	response := s.skeleton("failed", map[string]any{
		"code":    "upstream_error",
		"message": message,
	})
	return s.event("response.failed", map[string]any{"response": response})
}

func (s *ResponsesStream) skeleton(status string, responseError any) map[string]any {
	return map[string]any{
		"id":                   s.id,
		"object":               "response",
		"created_at":           s.created,
		"status":               status,
		"error":                responseError,
		"incomplete_details":   nil,
		"instructions":         nil,
		"max_output_tokens":    nil,
		"model":                s.model,
		"output":               []any{},
		"parallel_tool_calls":  true,
		"previous_response_id": nil,
		"reasoning":            nil,
		"store":                false,
		"temperature":          nil,
		"text":                 map[string]any{"format": map[string]any{"type": "text"}},
		"tool_choice":          "auto",
		"tools":                []any{},
		"top_p":                nil,
		"truncation":           "disabled",
		"usage":                nil,
		"user":                 nil,
		"metadata":             map[string]any{},
	}
}

func (s *ResponsesStream) startedItem(index int, item map[string]any) error {
	added := cloneMap(item)
	added["status"] = "in_progress"
	switch stringAt(item, "type") {
	case "message":
		added["content"] = []any{}
	case "function_call":
		added["arguments"] = ""
	case "custom_tool_call":
		added["input"] = ""
	}
	return s.event("response.output_item.added", map[string]any{"output_index": index, "item": added})
}

func (s *ResponsesStream) itemEvents(index int, item map[string]any) error {
	itemID := stringAt(item, "id")
	switch stringAt(item, "type") {
	case "message":
		for contentIndex, raw := range asSlice(item["content"]) {
			part := asMap(raw)
			if stringAt(part, "type") != "output_text" {
				continue
			}
			text := stringAt(part, "text")
			emptyPart := map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
				"logprobs":    []any{},
			}
			if err := s.event("response.content_part.added", map[string]any{
				"item_id": itemID, "output_index": index, "content_index": contentIndex, "part": emptyPart,
			}); err != nil {
				return err
			}
			if err := s.event("response.output_text.delta", map[string]any{
				"item_id": itemID, "output_index": index, "content_index": contentIndex, "delta": text, "logprobs": []any{},
			}); err != nil {
				return err
			}
			if err := s.event("response.output_text.done", map[string]any{
				"item_id": itemID, "output_index": index, "content_index": contentIndex, "text": text, "logprobs": []any{},
			}); err != nil {
				return err
			}
			if err := s.event("response.content_part.done", map[string]any{
				"item_id": itemID, "output_index": index, "content_index": contentIndex, "part": part,
			}); err != nil {
				return err
			}
		}
	case "function_call":
		arguments := stringAt(item, "arguments")
		if err := s.event("response.function_call_arguments.delta", map[string]any{
			"item_id": itemID, "output_index": index, "delta": arguments,
		}); err != nil {
			return err
		}
		return s.event("response.function_call_arguments.done", map[string]any{
			"item_id": itemID, "output_index": index, "arguments": arguments,
		})
	case "custom_tool_call":
		input := stringAt(item, "input")
		if err := s.event("response.custom_tool_call_input.delta", map[string]any{
			"item_id": itemID, "output_index": index, "delta": input,
		}); err != nil {
			return err
		}
		return s.event("response.custom_tool_call_input.done", map[string]any{
			"item_id": itemID, "output_index": index, "input": input,
		})
	}
	return nil
}

func (s *ResponsesStream) event(eventType string, fields map[string]any) error {
	payload := make(map[string]any, len(fields)+2)
	payload["type"] = eventType
	payload["sequence_number"] = s.sequence
	s.sequence++
	for key, value := range fields {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", eventType, encoded); err != nil {
		return err
	}
	s.flush()
	return nil
}

func AggregateAnthropicStream(body []byte) ([]byte, error) {
	type blockState struct {
		kind      string
		text      strings.Builder
		thinking  strings.Builder
		partial   strings.Builder
		id        string
		name      string
		signature string
		input     map[string]any
	}
	blocks := make(map[int]*blockState)
	message := map[string]any{"type": "message", "role": "assistant", "content": []any{}}
	usage := map[string]any{}
	var stopReason string

	err := forEachSSEData(body, func(data []byte) error {
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		switch strings.TrimSpace(stringAt(event, "type")) {
		case "message_start":
			started := mapAt(event, "message")
			if started != nil {
				for key, value := range started {
					if key != "content" && key != "usage" {
						message[key] = value
					}
				}
				if value := mapAt(started, "usage"); value != nil {
					for key, item := range value {
						usage[key] = item
					}
				}
			}
		case "content_block_start":
			index := intValue(event["index"])
			block := mapAt(event, "content_block")
			state := &blockState{kind: strings.TrimSpace(stringAt(block, "type")), id: stringAt(block, "id"), name: stringAt(block, "name")}
			if state.kind == "text" {
				state.text.WriteString(stringAt(block, "text"))
			}
			if state.kind == "thinking" {
				state.thinking.WriteString(stringAt(block, "thinking"))
				state.signature = stringAt(block, "signature")
			}
			blocks[index] = state
		case "content_block_delta":
			index := intValue(event["index"])
			state := blocks[index]
			if state == nil {
				state = &blockState{}
				blocks[index] = state
			}
			delta := mapAt(event, "delta")
			switch strings.TrimSpace(stringAt(delta, "type")) {
			case "text_delta":
				state.text.WriteString(stringAt(delta, "text"))
			case "thinking_delta":
				state.thinking.WriteString(stringAt(delta, "thinking"))
			case "signature_delta":
				state.signature += stringAt(delta, "signature")
			case "input_json_delta":
				state.partial.WriteString(stringAt(delta, "partial_json"))
			}
		case "content_block_stop":
			index := intValue(event["index"])
			state := blocks[index]
			if state != nil && state.kind == "tool_use" {
				if state.input == nil {
					state.input = map[string]any{}
				}
				if raw := strings.TrimSpace(state.partial.String()); raw != "" {
					_ = json.Unmarshal([]byte(raw), &state.input)
				}
			}
		case "message_delta":
			delta := mapAt(event, "delta")
			stopReason = stringAt(delta, "stop_reason")
			if value := mapAt(event, "usage"); value != nil {
				for key, item := range value {
					usage[key] = item
				}
			}
		case "error":
			detail := mapAt(event, "error")
			messageText := firstString(stringAt(detail, "message"), stringify(event["error"]))
			if messageText == "" {
				messageText = "anthropic stream failed"
			}
			return fmt.Errorf("%s", messageText)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sortInts(indices)
	content := make([]any, 0, len(indices))
	for _, index := range indices {
		state := blocks[index]
		switch state.kind {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": state.text.String()})
		case "thinking":
			content = append(content, map[string]any{"type": "thinking", "thinking": state.thinking.String(), "signature": state.signature})
		case "tool_use":
			if state.input == nil {
				state.input = map[string]any{}
			}
			content = append(content, map[string]any{"type": "tool_use", "id": state.id, "name": state.name, "input": state.input})
		}
	}
	message["content"] = content
	if stopReason != "" {
		message["stop_reason"] = stopReason
	}
	if len(usage) > 0 {
		message["usage"] = usage
	}
	return json.Marshal(message)
}

func forEachSSEData(body []byte, consume func([]byte) error) error {
	var data bytes.Buffer
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		value := data.Bytes()
		data.Reset()
		return consume(value)
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		value := bytes.TrimSpace(line[len("data:"):])
		if value == nil {
			continue
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.Write(value)
	}
	return flush()
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
