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

	"github.com/rain/free-opencode-api/internal/config"
	"github.com/rain/free-opencode-api/internal/provider"
)

func TestChatCompletionForwardsOpenCodeHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer public" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		for _, name := range []string{"x-opencode-session", "x-opencode-request", "x-opencode-client", "User-Agent"} {
			if r.Header.Get(name) == "" {
				t.Errorf("missing upstream header %s", name)
			}
		}
		if got := r.Header.Get("x-opencode-session"); got != "ses_from_opencode" {
			t.Errorf("session = %q, want client-provided ID", got)
		}
		if got := r.Header.Get("x-opencode-request"); got != "msg_from_opencode" {
			t.Errorf("request = %q, want client-provided ID", got)
		}
		if got := r.Header.Get("x-parent-session-id"); got != "ses_parent_from_opencode" {
			t.Errorf("parent session = %q, want client-provided ID", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("invalid upstream JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_test","object":"chat.completion","choices":[]}`))
	}))
	defer upstream.Close()

	server := newTestServer(t, upstream.URL+"/v1")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
        "model":"free-model",
        "user":"conversation-one",
        "messages":[{"role":"user","content":"hello"}]
    }`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-opencode-session", "ses_from_opencode")
	request.Header.Set("x-opencode-request", "msg_from_opencode")
	request.Header.Set("x-parent-session-id", "ses_parent_from_opencode")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("response did not include X-Request-ID")
	}
	if !strings.Contains(response.Body.String(), "chatcmpl_test") {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestOptionalServiceAPIKey(t *testing.T) {
	server := &Server{cfg: config.Config{}}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	if !server.authorize(response, request) {
		t.Fatal("empty service key should allow requests")
	}

	server.cfg.ServiceAPIKey = "service-secret"
	response = httptest.NewRecorder()
	if server.authorize(response, request) {
		t.Fatal("missing service key should be rejected when configured")
	}
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request.Header.Set("Authorization", "Bearer service-secret")
	response = httptest.NewRecorder()
	if !server.authorize(response, request) {
		t.Fatal("valid service key should be accepted")
	}
}

func TestChatCompletionForwardsSupportedFieldsVerbatim(t *testing.T) {
	const requestBody = `{
        "model":"free-model",
        "messages":[{"role":"user","content":"hello"}],
        "stream":false,
        "temperature":0.35,
        "top_p":0.8,
        "max_tokens":321,
        "stop":["END","STOP"],
        "provider_option":{"enabled":true}
    }`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) != requestBody {
			t.Errorf("upstream body was rewritten:\n got: %s\nwant: %s", body, requestBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_passthrough","object":"chat.completion","choices":[]}`))
	}))
	defer upstream.Close()

	server := newTestServer(t, upstream.URL+"/v1")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer service-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestChatCompletionStreamsSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	server := newTestServer(t, upstream.URL+"/v1")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
        "model":"free-model",
        "stream":true,
        "messages":[{"role":"user","content":"hello"}]
    }`))
	request.Header.Set("Authorization", "Bearer service-secret")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	if !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestChatCompletionAggregatesUpstreamSSEForNonStreamingClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_aggregate\",\"created\":1,\"model\":\"free-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_aggregate\",\"created\":1,\"model\":\"free-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"total_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	server := newTestServer(t, upstream.URL+"/v1")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
        "model":"free-model",
        "messages":[{"role":"user","content":"hello"}]
    }`))
	request.Header.Set("Authorization", "Bearer service-secret")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Choices) != 1 || payload.Choices[0].Message.Content != "hello" {
		t.Fatalf("payload = %s", response.Body.String())
	}
}

func TestModelsProxyUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer public" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"any-model"}]}`))
	}))
	defer upstream.Close()
	server := newTestServer(t, upstream.URL+"/v1")

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "any-model") {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func newTestServer(t *testing.T, upstreamBaseURL string) *Server {
	t.Helper()
	cfg := config.Config{
		UpstreamBaseURL: upstreamBaseURL,
		RequestTimeout:  time.Second,
	}
	upstream, err := provider.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, upstream, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
