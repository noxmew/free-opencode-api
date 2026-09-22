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

func TestMimoChatZenGatewayFlow(t *testing.T) {
	server := newZenTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"mimo-v2.5-free","messages":[{"role":"user","content":"Reply exactly: chat-ok"}]}`))
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
	if payload.Model != "mimo-v2.5-free" || len(payload.Choices) == 0 || !strings.Contains(payload.Choices[0].Message.Content, "chat-ok") {
		t.Fatalf("response = %s", response.Body.String())
	}
}

func TestMuseResponsesZenGatewayFlow(t *testing.T) {
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
		UpstreamBaseURL: "https://opencode.ai/zen/v1",
		RequestTimeout:  2 * time.Minute,
	}
	upstream, err := provider.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, upstream, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
