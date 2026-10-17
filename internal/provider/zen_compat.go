package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"

	"github.com/noxmew/free-opencode-api/internal/protocol"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func prepareOpenCodeRequestBody(body []byte, target protocol.Protocol) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return nil, fmt.Errorf("upstream body must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("upstream body must contain one JSON value")
	}

	raw["stream"] = json.RawMessage("true")
	switch target {
	case protocol.Chat:
		var options map[string]json.RawMessage
		if json.Unmarshal(raw["stream_options"], &options) != nil || options == nil {
			options = make(map[string]json.RawMessage)
		}
		var includeUsage bool
		if json.Unmarshal(options["include_usage"], &includeUsage) != nil || !includeUsage {
			options["include_usage"] = json.RawMessage("true")
		}
		raw["stream_options"] = mustJSON(options)
	case protocol.Anthropic:
		var maxTokens float64
		if json.Unmarshal(raw["max_tokens"], &maxTokens) != nil || maxTokens <= 0 {
			raw["max_tokens"] = json.RawMessage("4096")
		}
	}

	tools, err := decodeOpenCodeTools(raw["tools"])
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(tools))
	for index, tool := range tools {
		name := openCodeRequestToolName(tool, target)
		if name == "" {
			continue
		}
		present[name] = true
		if _, known := openCodeToolDefinitions[name]; known {
			tools[index], err = openCodeToolForProtocol(name, target)
			if err != nil {
				return nil, err
			}
		}
	}
	for _, name := range openCodeCoreToolNames {
		if present[name] {
			continue
		}
		tool, err := openCodeToolForProtocol(name, target)
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	raw["tools"] = mustJSON(tools)
	return json.Marshal(raw)
}

func decodeOpenCodeTools(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(trimmed, &tools); err != nil {
		return nil, fmt.Errorf("tools must be a JSON array: %w", err)
	}
	return tools, nil
}

func openCodeRequestToolName(raw json.RawMessage, target protocol.Protocol) string {
	var tool struct {
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &tool) != nil {
		return ""
	}
	if target == protocol.Chat {
		return tool.Function.Name
	}
	return tool.Name
}

func openCodeToolForProtocol(name string, target protocol.Protocol) (json.RawMessage, error) {
	definition, ok := openCodeToolDefinitions[name]
	if !ok {
		return nil, fmt.Errorf("missing OpenCode tool definition %q", name)
	}
	switch target {
	case protocol.Responses:
		return json.Marshal(map[string]any{
			"type":        "function",
			"name":        name,
			"description": definition.Description,
			"parameters":  definition.Parameters,
		})
	case protocol.Anthropic:
		return json.Marshal(map[string]any{
			"name":         name,
			"description":  definition.Description,
			"input_schema": definition.Parameters,
		})
	default:
		return json.Marshal(map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": definition.Description,
				"parameters":  definition.Parameters,
			},
		})
	}
}

func zenSessionID(input HeaderInput) string {
	for _, name := range []string{"x-opencode-session", "x-opencode-session-id", "x-session-affinity", "X-Session-Id", "x-session-id", "conversation-id"} {
		if value := cleanHeaderValue(input.Inbound.Get(name)); value != "" {
			return canonicalOpenCodeSessionID(value)
		}
	}
	return canonicalOpenCodeSessionID(openCodeID("ses", true))
}

func canonicalOpenCodeSessionID(signal string) string {
	if canonicalOpenCodeSessionPattern.MatchString(signal) {
		return signal
	}
	sum := sha256.Sum256([]byte("ses\x00" + signal))
	timePart := descendingTimePart(sum[:6])
	randomPart := base62Fixed(new(big.Int).SetBytes(sum[6:16]), 14)
	return "ses_" + timePart + randomPart
}

func descendingTimePart(hash []byte) string {
	current := (uint64(hash[0])<<28 | uint64(hash[1])<<20 | uint64(hash[2])<<12 |
		uint64(hash[3])<<4 | uint64(hash[4])>>4) << 12
	current |= 1
	inverted := ^current & 0xFFFFFFFFFFFF
	var timestamp [6]byte
	for index := len(timestamp) - 1; index >= 0; index-- {
		timestamp[index] = byte(inverted)
		inverted >>= 8
	}
	return hex.EncodeToString(timestamp[:])
}

func base62Fixed(value *big.Int, width int) string {
	base := big.NewInt(62)
	out := make([]byte, width)
	remainder := new(big.Int)
	for index := width - 1; index >= 0; index-- {
		value.DivMod(value, base, remainder)
		out[index] = base62Alphabet[remainder.Int64()]
	}
	return string(out)
}
