package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type RequestMeta struct {
	Model  string
	Stream bool
}

func ResponsesRequestMeta(body []byte) (RequestMeta, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return RequestMeta{}, fmt.Errorf("request body must be a JSON object")
	}
	model, _ := request["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return RequestMeta{}, errors.New("model is required")
	}
	return RequestMeta{Model: model, Stream: asBool(request["stream"])}, nil
}

func ResponsesToolTypes(body []byte) map[string]string {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil
	}
	result := make(map[string]string)
	for _, raw := range asSlice(request["tools"]) {
		tool := asMap(raw)
		name := strings.TrimSpace(stringAt(tool, "name"))
		if name == "" {
			continue
		}
		kind := strings.TrimSpace(stringAt(tool, "type"))
		if kind == "" {
			kind = "function"
		}
		result[name] = kind
	}
	return result
}

func ResponsesToChat(body []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	model := strings.TrimSpace(stringAt(request, "model"))
	if model == "" {
		return nil, errors.New("model is required")
	}

	messages, err := responsesMessagesToChat(request["instructions"], request["input"])
	if err != nil {
		return nil, err
	}
	output := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if stream, ok := request["stream"].(bool); ok {
		output["stream"] = stream
	}
	if tools := responsesToolsToChat(request["tools"]); len(tools) > 0 {
		output["tools"] = tools
	}
	if choice := responsesToolChoiceToChat(request["tool_choice"]); choice != nil {
		output["tool_choice"] = choice
	}
	if maxTokens, ok := request["max_output_tokens"]; ok && maxTokens != nil {
		if number, numeric := asNumber(maxTokens); !numeric || number > 0 {
			output["max_completion_tokens"] = maxTokens
		}
	}
	copyResponsesOptionsToChat(output, request)
	return json.Marshal(output)
}

func ResponsesToAnthropic(body []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	model := strings.TrimSpace(stringAt(request, "model"))
	if model == "" {
		return nil, errors.New("model is required")
	}

	messages, err := responsesMessagesToAnthropic(request["input"])
	if err != nil {
		return nil, err
	}
	output := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if system := responsesInstructionsText(request["instructions"]); system != "" {
		output["system"] = system
	}
	if stream, ok := request["stream"].(bool); ok {
		output["stream"] = stream
	}
	if maxTokens, ok := request["max_output_tokens"]; ok && maxTokens != nil {
		if number, numeric := asNumber(maxTokens); !numeric || number > 0 {
			output["max_tokens"] = maxTokens
		} else {
			output["max_tokens"] = 4096
		}
	} else {
		output["max_tokens"] = 4096
	}
	if !isChatNoneToolChoice(request["tool_choice"]) {
		if tools := responsesToolsToAnthropic(request["tools"]); len(tools) > 0 {
			output["tools"] = tools
		}
	}
	if choice := responsesToolChoiceToAnthropic(request["tool_choice"]); choice != nil {
		output["tool_choice"] = choice
	}
	if value, ok := request["temperature"]; ok {
		output["temperature"] = value
	}
	if value, ok := request["top_p"]; ok {
		output["top_p"] = value
	}
	if value, ok := request["stop"]; ok {
		output["stop_sequences"] = value
	}
	if metadata := anthropicMetadata(request["metadata"]); metadata != nil {
		output["metadata"] = metadata
	}
	return json.Marshal(output)
}

func copyResponsesOptionsToChat(output, request map[string]any) {
	for _, key := range []string{
		"temperature", "top_p", "top_logprobs", "presence_penalty", "frequency_penalty",
		"parallel_tool_calls", "user", "store", "safety_identifier",
		"service_tier", "prompt_cache_key",
	} {
		copyIfPresent(output, request, key, key)
	}
	if effort := responsesReasoningEffort(request["reasoning"]); effort != "" {
		output["reasoning_effort"] = effort
	} else if effort := responsesReasoningEffort(request["reasoning_effort"]); effort != "" {
		output["reasoning_effort"] = effort
	}
	if format := mapAt(mapAt(request, "text"), "format"); format != nil {
		if converted := responsesFormatToChat(format); converted != nil {
			output["response_format"] = converted
		}
	} else if format := mapAt(request, "response_format"); format != nil {
		output["response_format"] = format
	}
}

func responsesReasoningEffort(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	object := asMap(value)
	return strings.TrimSpace(stringAt(object, "effort"))
}

func responsesFormatToChat(format map[string]any) map[string]any {
	kind := strings.TrimSpace(stringAt(format, "type"))
	switch kind {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		if nested := mapAt(format, "json_schema"); nested != nil {
			return map[string]any{"type": "json_schema", "json_schema": nested}
		}
		nested := map[string]any{}
		for _, key := range []string{"name", "description", "schema", "strict"} {
			if value, ok := format[key]; ok {
				nested[key] = value
			}
		}
		if len(nested) == 0 {
			return nil
		}
		return map[string]any{"type": "json_schema", "json_schema": nested}
	default:
		return nil
	}
}

func responsesMessagesToChat(instructions any, input any) ([]any, error) {
	messages := make([]any, 0)
	if system := responsesInstructionsText(instructions); system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}

	switch value := input.(type) {
	case nil:
		return messages, nil
	case string:
		if strings.TrimSpace(value) != "" {
			messages = append(messages, map[string]any{"role": "user", "content": value})
		}
		return messages, nil
	case []any:
		var pendingMedia []any
		for index, raw := range value {
			item, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("input[%d] must be an object", index)
			}
			itemType := strings.TrimSpace(stringAt(item, "type"))
			if len(pendingMedia) > 0 && itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				messages = append(messages, map[string]any{"role": "user", "content": pendingMedia})
				pendingMedia = nil
			}
			switch itemType {
			case "reasoning", "compaction", "compaction_summary", "item_reference":
				continue
			case "function_call":
				call, err := responsesFunctionCallToChat(item, false)
				if err != nil {
					return nil, fmt.Errorf("input[%d]: %w", index, err)
				}
				messages = appendAssistantToolCall(messages, call)
			case "custom_tool_call":
				call, err := responsesFunctionCallToChat(item, true)
				if err != nil {
					return nil, fmt.Errorf("input[%d]: %w", index, err)
				}
				messages = appendAssistantToolCall(messages, call)
			case "function_call_output", "custom_tool_call_output":
				callID := strings.TrimSpace(stringAt(item, "call_id"))
				if callID == "" {
					return nil, fmt.Errorf("input[%d] is missing call_id", index)
				}
				text, media := responsesToolOutputToChat(item["output"])
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      text,
				})
				pendingMedia = append(pendingMedia, media...)
			case "message", "":
				role := strings.TrimSpace(stringAt(item, "role"))
				if role == "" {
					role = "user"
				}
				content := responsesContentToChat(item["content"], role == "assistant")
				switch role {
				case "system", "developer":
					text := contentText(content)
					if text != "" {
						messages = append(messages, map[string]any{"role": "system", "content": text})
					}
				case "user":
					if content != nil {
						messages = append(messages, map[string]any{"role": "user", "content": content})
					}
				case "assistant":
					messages = appendAssistantMessage(messages, content)
				default:
					return nil, fmt.Errorf("input[%d] has unsupported role %q", index, role)
				}
			default:
				return nil, fmt.Errorf("input[%d] has unsupported item type %q", index, itemType)
			}
		}
		if len(pendingMedia) > 0 {
			messages = append(messages, map[string]any{"role": "user", "content": pendingMedia})
		}
		return messages, nil
	default:
		return nil, errors.New("input must be a string or array")
	}
}

func appendAssistantMessage(messages []any, content any) []any {
	if content == nil {
		return messages
	}
	if len(messages) > 0 {
		last := asMap(messages[len(messages)-1])
		if stringAt(last, "role") == "assistant" && last["tool_calls"] == nil {
			if existing, ok := last["content"].(string); ok {
				last["content"] = existing + contentText(content)
				return messages
			}
		}
	}
	return append(messages, map[string]any{"role": "assistant", "content": content})
}

func appendAssistantToolCall(messages []any, call map[string]any) []any {
	if len(messages) > 0 {
		last := asMap(messages[len(messages)-1])
		if stringAt(last, "role") == "assistant" {
			if calls, ok := last["tool_calls"].([]any); ok {
				last["tool_calls"] = append(calls, call)
			} else {
				last["tool_calls"] = []any{call}
			}
			if _, exists := last["content"]; !exists {
				last["content"] = nil
			}
			return messages
		}
	}
	return append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{call}})
}

func responsesFunctionCallToChat(item map[string]any, custom bool) (map[string]any, error) {
	name := strings.TrimSpace(stringAt(item, "name"))
	if name == "" {
		return nil, errors.New("tool call is missing name")
	}
	callID := strings.TrimSpace(stringAt(item, "call_id"))
	if callID == "" {
		callID = newID("call_")
	}
	arguments := stringAt(item, "arguments")
	if custom {
		input := item["input"]
		if input == nil {
			input = ""
		}
		encoded, err := json.Marshal(map[string]any{"input": input})
		if err != nil {
			return nil, err
		}
		arguments = string(encoded)
	}
	return map[string]any{
		"id":   callID,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}, nil
}

func responsesContentToChat(content any, assistant bool) any {
	switch value := content.(type) {
	case nil:
		return nil
	case string:
		return value
	case []any:
		parts := make([]any, 0, len(value))
		var text strings.Builder
		allText := true
		for _, raw := range value {
			part := asMap(raw)
			if part == nil {
				allText = false
				parts = append(parts, raw)
				continue
			}
			switch strings.TrimSpace(stringAt(part, "type")) {
			case "input_text", "output_text", "text":
				itemText := stringAt(part, "text")
				text.WriteString(itemText)
				parts = append(parts, map[string]any{"type": "text", "text": itemText})
			case "input_image":
				if assistant {
					allText = false
					continue
				}
				allText = false
				parts = append(parts, responsesImagePartToChat(part))
			case "input_file":
				allText = false
				if filePart, ok := responsesFilePartToChat(part); ok {
					parts = append(parts, filePart)
				}
			default:
				allText = false
				parts = append(parts, part)
			}
		}
		if allText {
			return text.String()
		}
		if assistant {
			return text.String()
		}
		if len(parts) == 0 {
			if text.Len() > 0 {
				return text.String()
			}
			return "[attachment]"
		}
		return parts
	default:
		return stringify(value)
	}
}

func contentText(content any) string {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		var text strings.Builder
		for _, raw := range value {
			part := asMap(raw)
			if strings.TrimSpace(stringAt(part, "type")) == "text" {
				text.WriteString(stringAt(part, "text"))
			}
		}
		return text.String()
	default:
		return stringify(value)
	}
}

func responsesImagePartToChat(part map[string]any) map[string]any {
	imageURL := part["image_url"]
	switch value := imageURL.(type) {
	case string:
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": value}}
	case map[string]any:
		return map[string]any{"type": "image_url", "image_url": value}
	}
	if url := stringAt(part, "url"); url != "" {
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{}}
}

func responsesFilePartToChat(part map[string]any) (map[string]any, bool) {
	fileID := strings.TrimSpace(stringAt(part, "file_id"))
	if fileID == "" {
		fileID = strings.TrimSpace(stringAt(mapAt(part, "file"), "file_id"))
	}
	filename := strings.TrimSpace(stringAt(part, "filename"))
	if filename == "" {
		filename = strings.TrimSpace(stringAt(mapAt(part, "file"), "filename"))
	}
	fileData := strings.TrimSpace(stringAt(part, "file_data"))
	if fileData == "" {
		fileData = strings.TrimSpace(stringAt(mapAt(part, "file"), "file_data"))
	}
	if fileData == "" {
		fileData = strings.TrimSpace(stringAt(part, "file_url"))
	}
	if fileID == "" && fileData == "" {
		return nil, false
	}
	file := map[string]any{}
	if fileID != "" {
		file["file_id"] = fileID
	}
	if fileData != "" {
		file["file_data"] = fileData
	}
	if filename != "" {
		file["filename"] = filename
	}
	return map[string]any{"type": "file", "file": file}, true
}

func responsesToolOutputToChat(value any) (string, []any) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts, ok := value.([]any)
	if !ok {
		return stringify(value), nil
	}
	var texts []string
	media := make([]any, 0)
	labels := make([]string, 0)
	seenLabels := make(map[string]bool)
	for _, raw := range parts {
		part := asMap(raw)
		if part == nil {
			return stringify(value), nil
		}
		switch strings.TrimSpace(stringAt(part, "type")) {
		case "input_text", "output_text", "text":
			if text := stringAt(part, "text"); text != "" {
				texts = append(texts, text)
			}
		case "input_image":
			media = append(media, responsesImagePartToChat(part))
			appendLabel(&labels, seenLabels, "image")
		case "input_file":
			if filePart, ok := responsesFilePartToChat(part); ok {
				media = append(media, filePart)
			}
			appendLabel(&labels, seenLabels, "file")
		default:
			return stringify(value), nil
		}
	}
	if len(media) == 0 {
		if len(texts) == 0 && len(labels) > 0 {
			return labelsText(labels), nil
		}
		return strings.Join(texts, "\n"), nil
	}
	text := strings.Join(texts, "\n")
	if text == "" {
		text = labelsText(labels)
	}
	return text, media
}

func appendLabel(labels *[]string, seen map[string]bool, label string) {
	formatted := "[" + label + "]"
	if seen[formatted] {
		return
	}
	seen[formatted] = true
	*labels = append(*labels, formatted)
}

func labelsText(labels []string) string {
	return strings.Join(labels, " ")
}

func responsesToolsToChat(value any) []any {
	result := make([]any, 0)
	for _, raw := range asSlice(value) {
		tool := asMap(raw)
		toolType := strings.TrimSpace(stringAt(tool, "type"))
		if toolType == "" {
			toolType = "function"
		}
		if toolType != "function" && toolType != "custom" {
			continue
		}
		name := strings.TrimSpace(stringAt(tool, "name"))
		if name == "" {
			continue
		}
		parameters := tool["parameters"]
		if toolType == "custom" || parameters == nil {
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{"type": "string"},
				},
				"required": []string{"input"},
			}
		}
		function := map[string]any{"name": name, "parameters": parameters}
		if description := strings.TrimSpace(stringAt(tool, "description")); description != "" {
			function["description"] = description
		}
		if strict, ok := tool["strict"].(bool); ok {
			function["strict"] = strict
		}
		result = append(result, map[string]any{"type": "function", "function": function})
	}
	return result
}

func responsesToolChoiceToChat(value any) any {
	switch choice := value.(type) {
	case string:
		return choice
	case map[string]any:
		kind := strings.TrimSpace(stringAt(choice, "type"))
		switch kind {
		case "function", "custom":
			name := strings.TrimSpace(stringAt(choice, "name"))
			if nested := mapAt(choice, "function"); nested != nil {
				name = strings.TrimSpace(stringAt(nested, "name"))
			}
			if name == "" {
				return nil
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		case "allowed_tools":
			allowed := mapAt(choice, "allowed_tools")
			mode := strings.TrimSpace(stringAt(allowed, "mode"))
			if mode == "required" {
				return "required"
			}
			if tools := asSlice(allowed["tools"]); len(tools) == 1 {
				tool := asMap(tools[0])
				name := strings.TrimSpace(stringAt(tool, "name"))
				if nested := mapAt(tool, "function"); nested != nil {
					name = strings.TrimSpace(stringAt(nested, "name"))
				}
				if name != "" {
					return map[string]any{"type": "function", "function": map[string]any{"name": name}}
				}
			}
			return "auto"
		}
	}
	return nil
}

func responsesInstructionsText(value any) string {
	switch instructions := value.(type) {
	case nil:
		return ""
	case string:
		return instructions
	case []any:
		var text strings.Builder
		for _, raw := range instructions {
			part := asMap(raw)
			switch strings.TrimSpace(stringAt(part, "type")) {
			case "input_text", "output_text", "text":
				text.WriteString(stringAt(part, "text"))
			}
		}
		return text.String()
	default:
		return stringify(value)
	}
}

func responsesMessagesToAnthropic(input any) ([]any, error) {
	messages := make([]any, 0)
	appendMessage := func(role string, content []any) {
		if len(content) == 0 {
			return
		}
		if len(messages) > 0 {
			last := asMap(messages[len(messages)-1])
			if stringAt(last, "role") == role {
				last["content"] = append(asSlice(last["content"]), content...)
				return
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}

	switch value := input.(type) {
	case nil:
		return messages, nil
	case string:
		appendMessage("user", []any{map[string]any{"type": "text", "text": value}})
		return messages, nil
	case []any:
		for index, raw := range value {
			item := asMap(raw)
			itemType := strings.TrimSpace(stringAt(item, "type"))
			switch itemType {
			case "reasoning", "compaction", "compaction_summary", "item_reference":
				continue
			case "function_call", "custom_tool_call":
				name := strings.TrimSpace(stringAt(item, "name"))
				if name == "" {
					return nil, fmt.Errorf("input[%d] tool call is missing name", index)
				}
				callID := strings.TrimSpace(stringAt(item, "call_id"))
				if callID == "" {
					callID = newID("call_")
				}
				var inputValue any = map[string]any{}
				if itemType == "custom_tool_call" {
					inputValue = map[string]any{"input": item["input"]}
				} else if arguments := strings.TrimSpace(stringAt(item, "arguments")); arguments != "" {
					if err := json.Unmarshal([]byte(arguments), &inputValue); err != nil {
						inputValue = map[string]any{"input": arguments}
					}
				}
				appendMessage("assistant", []any{map[string]any{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": inputValue,
				}})
			case "function_call_output", "custom_tool_call_output":
				callID := strings.TrimSpace(stringAt(item, "call_id"))
				if callID == "" {
					return nil, fmt.Errorf("input[%d] is missing call_id", index)
				}
				appendMessage("user", []any{map[string]any{
					"type":        "tool_result",
					"tool_use_id": callID,
					"content":     responsesToolOutputToAnthropic(item["output"]),
					"is_error":    false,
				}})
			case "message", "":
				role := strings.TrimSpace(stringAt(item, "role"))
				if role == "" {
					role = "user"
				}
				if role == "system" || role == "developer" {
					continue
				}
				if role != "user" && role != "assistant" {
					return nil, fmt.Errorf("input[%d] has unsupported role %q", index, role)
				}
				appendMessage(role, responsesContentToAnthropic(item["content"]))
			default:
				return nil, fmt.Errorf("input[%d] has unsupported item type %q", index, itemType)
			}
		}
		return messages, nil
	default:
		return nil, errors.New("input must be a string or array")
	}
}

func responsesContentToAnthropic(content any) []any {
	switch value := content.(type) {
	case string:
		if value == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": value}}
	case []any:
		result := make([]any, 0, len(value))
		for _, raw := range value {
			part := asMap(raw)
			switch strings.TrimSpace(stringAt(part, "type")) {
			case "input_text", "output_text", "text":
				result = append(result, map[string]any{"type": "text", "text": stringAt(part, "text")})
			case "input_image":
				if image := responsesImagePartToAnthropic(part); image != nil {
					result = append(result, image)
				}
			case "input_file":
				if file := responsesFilePartToAnthropic(part); file != nil {
					result = append(result, file)
				}
			}
		}
		return result
	default:
		if text := stringify(value); text != "" {
			return []any{map[string]any{"type": "text", "text": text}}
		}
		return nil
	}
}

func responsesToolOutputToAnthropic(value any) any {
	if text, ok := value.(string); ok {
		return text
	}
	parts, ok := value.([]any)
	if !ok {
		return stringify(value)
	}
	result := make([]any, 0, len(parts))
	for _, raw := range parts {
		part := asMap(raw)
		switch strings.TrimSpace(stringAt(part, "type")) {
		case "input_text", "output_text", "text":
			result = append(result, map[string]any{"type": "text", "text": stringAt(part, "text")})
		case "input_image":
			if image := responsesImagePartToAnthropic(part); image != nil {
				result = append(result, image)
			}
		default:
			return stringify(value)
		}
	}
	if len(result) == 0 {
		return "[tool output]"
	}
	return result
}

func responsesImagePartToAnthropic(part map[string]any) map[string]any {
	imageURL := part["image_url"]
	url := ""
	if text, ok := imageURL.(string); ok {
		url = text
	} else if object := asMap(imageURL); object != nil {
		url = stringAt(object, "url")
	}
	if url == "" {
		url = stringAt(part, "url")
	}
	if url == "" {
		return nil
	}
	if strings.HasPrefix(url, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
		if ok {
			mediaType, base64 := strings.CutSuffix(header, ";base64")
			if base64 && strings.HasPrefix(mediaType, "image/") {
				return map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": mediaType, "data": data,
				}}
			}
		}
	}
	return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}}
}

func responsesFilePartToAnthropic(part map[string]any) map[string]any {
	fileData := strings.TrimSpace(stringAt(part, "file_data"))
	fileURL := strings.TrimSpace(stringAt(part, "file_url"))
	filename := strings.TrimSpace(stringAt(part, "filename"))
	fileID := strings.TrimSpace(stringAt(part, "file_id"))
	if fileData == "" && fileURL == "" && fileID == "" {
		return nil
	}
	source := map[string]any{}
	if fileURL != "" {
		source["type"] = "url"
		source["url"] = fileURL
	} else if fileData != "" {
		source["type"] = "base64"
		source["data"] = fileData
		if strings.HasPrefix(fileData, "data:") {
			header, data, ok := strings.Cut(strings.TrimPrefix(fileData, "data:"), ",")
			if ok {
				mediaType, _ := strings.CutSuffix(header, ";base64")
				source["media_type"] = mediaType
				source["data"] = data
			}
		}
	} else {
		source["type"] = "file"
		source["file_id"] = fileID
	}
	document := map[string]any{"type": "document", "source": source}
	if filename != "" {
		document["title"] = filename
	}
	return document
}

func responsesToolsToAnthropic(value any) []any {
	result := make([]any, 0)
	for _, raw := range asSlice(value) {
		tool := asMap(raw)
		toolType := strings.TrimSpace(stringAt(tool, "type"))
		if toolType == "" {
			toolType = "function"
		}
		if toolType != "function" && toolType != "custom" {
			continue
		}
		name := strings.TrimSpace(stringAt(tool, "name"))
		if name == "" {
			continue
		}
		schema := tool["parameters"]
		if toolType == "custom" || schema == nil {
			schema = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{"type": "string"},
				},
				"required": []string{"input"},
			}
		}
		item := map[string]any{"name": name, "input_schema": schema}
		if description := strings.TrimSpace(stringAt(tool, "description")); description != "" {
			item["description"] = description
		}
		result = append(result, item)
	}
	return result
}

func responsesToolChoiceToAnthropic(value any) any {
	switch choice := value.(type) {
	case string:
		switch choice {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return nil
		}
	case map[string]any:
		kind := strings.TrimSpace(stringAt(choice, "type"))
		if kind == "function" || kind == "custom" {
			name := strings.TrimSpace(stringAt(choice, "name"))
			if nested := mapAt(choice, "function"); nested != nil {
				name = strings.TrimSpace(stringAt(nested, "name"))
			}
			if name != "" {
				return map[string]any{"type": "tool", "name": name}
			}
		}
	}
	return nil
}

func isChatNoneToolChoice(value any) bool {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == "none"
	}
	return false
}

func anthropicMetadata(value any) map[string]any {
	object := asMap(value)
	if object == nil {
		return nil
	}
	if userID := strings.TrimSpace(stringAt(object, "user_id")); userID != "" {
		return map[string]any{"user_id": userID}
	}
	return nil
}
