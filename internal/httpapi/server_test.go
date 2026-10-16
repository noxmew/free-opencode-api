package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noxmew/free-opencode-api/internal/config"
	"github.com/noxmew/free-opencode-api/internal/provider"
)

func TestChatCompletionsGatewayFlow(t *testing.T) {
	server := newZenTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"user","content":"Reply exactly: chat-ok"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response: %v; body = %s", err, response.Body.String())
	}
	if payload.Model != "mimo-v2.6-flash-free" || len(payload.Choices) == 0 || !strings.Contains(payload.Choices[0].Message.Content, "chat-ok") {
		t.Fatalf("response = %s", response.Body.String())
	}
}

const chatCompletionsToolTestModel = "mimo-v2.6-flash-free"

func TestChatCompletionsToolCallFlow(t *testing.T) {
	server := newZenTestServer(t)
	requestBody, err := json.Marshal(map[string]any{
		"model": chatCompletionsToolTestModel,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "Use the lookup_item function to look up the item named widget. Call the function now.",
		}},
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "lookup_item",
				"description": "Look up an item by name.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"item": map[string]string{"type": "string"},
					},
					"required":             []string{"item"},
					"additionalProperties": false,
				},
			},
		}},
		"tool_choice": map[string]any{
			"type":     "function",
			"function": map[string]string{"name": "lookup_item"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(requestBody)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", response.Header().Get("Content-Type"))
	}
	type streamChunk struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	var toolCallID, toolCallType, finishReason string
	var toolCallName, toolCallArguments strings.Builder
	toolCallIndices := make(map[int]bool)
	for _, line := range strings.Split(response.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("invalid stream event: %v; data = %s", err, data)
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
			for _, toolCall := range choice.Delta.ToolCalls {
				toolCallIndices[toolCall.Index] = true
				if toolCall.ID != "" {
					toolCallID = toolCall.ID
				}
				if toolCall.Type != "" {
					toolCallType = toolCall.Type
				}
				toolCallName.WriteString(toolCall.Function.Name)
				toolCallArguments.WriteString(toolCall.Function.Arguments)
			}
		}
	}
	if len(toolCallIndices) != 1 || finishReason != "tool_calls" {
		t.Fatalf("tool call indices = %v, finish reason = %q; stream = %s", toolCallIndices, finishReason, response.Body.String())
	}
	if toolCallID == "" || toolCallType != "function" || toolCallName.String() != "lookup_item" {
		t.Errorf("tool call = %q %q %q, want lookup_item function call", toolCallID, toolCallType, toolCallName.String())
	}
	var arguments struct {
		Item string `json:"item"`
	}
	if err := json.Unmarshal([]byte(toolCallArguments.String()), &arguments); err != nil {
		t.Fatalf("invalid tool arguments %q: %v", toolCallArguments.String(), err)
	}
	if arguments.Item != "widget" {
		t.Errorf("tool argument item = %q, want widget", arguments.Item)
	}
}

func TestResponsesGatewayFlow(t *testing.T) {
	server := newZenTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"muse-spark-1.3-contributor-free","input":"Reply exactly: response-ok"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Model  string `json:"model"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid response: %v; body = %s", err, response.Body.String())
	}
	if payload.Model != "muse-spark-1.3-contributor-free" || payload.Status != "completed" {
		t.Fatalf("response = %s", response.Body.String())
	}
}

func newZenTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Config{
		UpstreamBaseURL:   "https://opencode.ai/zen/v1",
		RequestTimeout:    2 * time.Minute,
		OpenCodeProjectID: "global",
	}
	upstream, err := provider.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, upstream, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
