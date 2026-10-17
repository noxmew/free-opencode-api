package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

func ChatCompletionToResponses(body []byte, responseID string, toolTypes map[string]string) (map[string]any, error) {
	var completion map[string]any
	if err := json.Unmarshal(body, &completion); err != nil {
		return nil, fmt.Errorf("invalid upstream chat completion: %w", err)
	}
	choices := asSlice(completion["choices"])
	if len(choices) == 0 {
		return nil, fmt.Errorf("upstream chat completion contained no choices")
	}
	choice := asMap(choices[0])
	message := mapAt(choice, "message")
	if message == nil {
		message = map[string]any{}
	}
	content := stringAt(message, "content")
	reasoning := firstString(stringAt(message, "reasoning_content"), stringAt(message, "reasoning"))
	toolCalls := asSlice(message["tool_calls"])
	finishReason := strings.TrimSpace(stringAt(choice, "finish_reason"))

	output := make([]any, 0, len(toolCalls)+2)
	if reasoning != "" {
		output = append(output, reasoningOutputItem(reasoning))
	}
	if content != "" || len(toolCalls) == 0 {
		output = append(output, messageOutputItem(content))
	}
	for _, raw := range toolCalls {
		if item := chatToolCallToResponses(asMap(raw), toolTypes); item != nil {
			output = append(output, item)
		}
	}
	return buildResponsesResponse(responseID, stringAt(completion, "model"), timestampValue(completion["created"]), output, content, finishReason, chatUsageToResponses(completion["usage"])), nil
}

func AnthropicMessageToResponses(body []byte, responseID string, toolTypes map[string]string) (map[string]any, error) {
	var message map[string]any
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("invalid upstream anthropic message: %w", err)
	}
	output := make([]any, 0)
	textParts := make([]string, 0)
	reasoningParts := make([]string, 0)
	messageIndex := -1
	for _, raw := range asSlice(message["content"]) {
		block := asMap(raw)
		switch strings.TrimSpace(stringAt(block, "type")) {
		case "text":
			value := stringAt(block, "text")
			textParts = append(textParts, value)
			if messageIndex < 0 {
				output = append(output, messageOutputItem(""))
				messageIndex = len(output) - 1
			}
			appendOutputText(output[messageIndex].(map[string]any), value)
		case "thinking":
			value := stringAt(block, "thinking")
			if value == "" {
				value = stringAt(block, "text")
			}
			if value != "" {
				reasoningParts = append(reasoningParts, value)
			}
		case "redacted_thinking":
			if value := stringAt(block, "data"); value != "" {
				reasoningParts = append(reasoningParts, value)
			}
		case "tool_use":
			messageIndex = -1
			if item := anthropicToolUseToResponses(block, toolTypes); item != nil {
				output = append(output, item)
			}
		}
	}
	if len(reasoningParts) > 0 {
		output = append([]any{reasoningOutputItem(strings.Join(reasoningParts, "\n"))}, output...)
	}
	if len(output) == 0 {
		output = append(output, messageOutputItem(""))
	}
	stopReason := strings.TrimSpace(stringAt(message, "stop_reason"))
	return buildResponsesResponse(responseID, stringAt(message, "model"), 0, output, strings.Join(textParts, ""), anthropicFinishReason(stopReason), anthropicUsageToResponses(message["usage"])), nil
}

func appendOutputText(item map[string]any, text string) {
	content := asSlice(item["content"])
	if len(content) == 0 {
		item["content"] = []any{map[string]any{
			"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{},
		}}
		return
	}
	part := asMap(content[0])
	part["text"] = stringAt(part, "text") + text
}

func buildResponsesResponse(responseID, model string, created int64, output []any, outputText, finishReason string, usage map[string]any) map[string]any {
	status := "completed"
	var incomplete any
	switch finishReason {
	case "length", "max_tokens", "max_output_tokens":
		status = "incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	case "content_filter":
		status = "incomplete"
		incomplete = map[string]any{"reason": "content_filter"}
	}
	response := map[string]any{
		"id":                   normalizeResponseID(responseID),
		"object":               "response",
		"created_at":           created,
		"status":               status,
		"error":                nil,
		"incomplete_details":   incomplete,
		"instructions":         nil,
		"max_output_tokens":    nil,
		"model":                model,
		"output":               output,
		"output_text":          outputText,
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
		"user":                 nil,
		"metadata":             map[string]any{},
	}
	if usage != nil {
		response["usage"] = usage
	} else {
		response["usage"] = nil
	}
	return response
}

func messageOutputItem(text string) map[string]any {
	return map[string]any{
		"id":     newID("msg_"),
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{map[string]any{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
			"logprobs":    []any{},
		}},
	}
}

func reasoningOutputItem(text string) map[string]any {
	return map[string]any{
		"id":     newID("rs_"),
		"type":   "reasoning",
		"status": "completed",
		"summary": []any{map[string]any{
			"type": "summary_text",
			"text": text,
		}},
	}
}

func chatToolCallToResponses(call map[string]any, toolTypes map[string]string) map[string]any {
	if call == nil {
		return nil
	}
	function := mapAt(call, "function")
	name := strings.TrimSpace(stringAt(function, "name"))
	if name == "" {
		return nil
	}
	callID := strings.TrimSpace(stringAt(call, "id"))
	if callID == "" {
		callID = newID("call_")
	}
	arguments := stringAt(function, "arguments")
	if toolTypes[name] == "custom" {
		return map[string]any{
			"id":      newID("ctc_"),
			"type":    "custom_tool_call",
			"status":  "completed",
			"call_id": callID,
			"name":    name,
			"input":   customToolInput(arguments),
		}
	}
	return map[string]any{
		"id":        newID("fc_"),
		"type":      "function_call",
		"status":    "completed",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
}

func anthropicToolUseToResponses(block map[string]any, toolTypes map[string]string) map[string]any {
	name := strings.TrimSpace(stringAt(block, "name"))
	if name == "" {
		return nil
	}
	callID := strings.TrimSpace(stringAt(block, "id"))
	if callID == "" {
		callID = newID("call_")
	}
	input := block["input"]
	if toolTypes[name] == "custom" {
		inputText := ""
		if object := asMap(input); object != nil {
			inputText = stringAt(object, "input")
		}
		if inputText == "" {
			inputText = stringify(input)
		}
		return map[string]any{
			"id":      newID("ctc_"),
			"type":    "custom_tool_call",
			"status":  "completed",
			"call_id": callID,
			"name":    name,
			"input":   inputText,
		}
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		arguments = []byte("{}")
	}
	return map[string]any{
		"id":        newID("fc_"),
		"type":      "function_call",
		"status":    "completed",
		"call_id":   callID,
		"name":      name,
		"arguments": string(arguments),
	}
}

func customToolInput(arguments string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(arguments), &payload) == nil {
		if input, ok := payload["input"].(string); ok {
			return input
		}
		if input := payload["input"]; input != nil {
			return stringify(input)
		}
	}
	return arguments
}

func timestampValue(value any) int64 {
	if number, ok := value.(float64); ok {
		return int64(number)
	}
	return 0
}

func chatUsageToResponses(value any) map[string]any {
	usage := asMap(value)
	if usage == nil {
		return nil
	}
	input := intValue(usage["prompt_tokens"])
	output := intValue(usage["completion_tokens"])
	total := intValue(usage["total_tokens"])
	if total == 0 {
		total = input + output
	}
	result := map[string]any{
		"input_tokens":          input,
		"output_tokens":         output,
		"total_tokens":          total,
		"input_tokens_details":  map[string]any{"cached_tokens": nestedInt(usage, "prompt_tokens_details", "cached_tokens")},
		"output_tokens_details": map[string]any{"reasoning_tokens": nestedInt(usage, "completion_tokens_details", "reasoning_tokens")},
	}
	return result
}

func anthropicUsageToResponses(value any) map[string]any {
	usage := asMap(value)
	if usage == nil {
		return nil
	}
	input := intValue(usage["input_tokens"])
	output := intValue(usage["output_tokens"])
	return map[string]any{
		"input_tokens":          input,
		"output_tokens":         output,
		"total_tokens":          input + output,
		"input_tokens_details":  map[string]any{"cached_tokens": intValue(usage["cache_read_input_tokens"])},
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
	}
}

func intValue(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case int64:
		return int(number)
	default:
		return 0
	}
}

func nestedInt(object map[string]any, path ...string) int {
	current := any(object)
	for _, key := range path {
		next := asMap(current)
		if next == nil {
			return 0
		}
		current = next[key]
	}
	return intValue(current)
}

func anthropicFinishReason(value string) string {
	switch value {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}
