package provider

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rain/free-opencode-api/internal/config"
)

func TestHeadersUseClientProvidedOpenCodeIDs(t *testing.T) {
	client, err := New(config.Config{
		UpstreamBaseURL:   "https://example.com/zen/v1",
		OpenCodeProjectID: "prj_test",
	})
	if err != nil {
		t.Fatal(err)
	}

	inbound := make(http.Header)
	inbound.Set("x-opencode-session", "ses_client-provided")
	inbound.Set("x-opencode-request", "msg_client-provided")
	inbound.Set("x-parent-session-id", "ses_parent")
	headers := client.Headers(HeaderInput{
		Inbound: inbound,
		Stream:  true,
	})

	checks := map[string]string{
		"Content-Type":       "application/json",
		"Accept":             "text/event-stream",
		"User-Agent":         "opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
		"x-opencode-client":  "cli",
		"x-opencode-project": "prj_test",
		"x-opencode-session": "ses_client-provided",
		"x-opencode-request": "msg_client-provided",
	}
	for name, want := range checks {
		if got := headers.Get(name); got != want {
			t.Fatalf("header %s = %q, want %q", name, got, want)
		}
	}
	if got := headers.Get("x-parent-session-id"); got != "ses_parent" {
		t.Fatalf("x-parent-session-id = %q, want client-provided ID", got)
	}
}

func TestHeadersDoNotReuseIDsAcrossCalls(t *testing.T) {
	client, err := New(config.Config{
		UpstreamBaseURL: "https://example.com/zen/v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	first := client.Headers(HeaderInput{})
	second := client.Headers(HeaderInput{})
	if first.Get("x-opencode-session") == second.Get("x-opencode-session") {
		t.Fatalf("session ID was reused: %q", first.Get("x-opencode-session"))
	}
	if first.Get("x-opencode-request") == second.Get("x-opencode-request") {
		t.Fatalf("request ID was reused: %q", first.Get("x-opencode-request"))
	}
}

func TestHeadersGenerateOpenCodeIDs(t *testing.T) {
	client, err := New(config.Config{
		UpstreamBaseURL: "https://example.com/zen/v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	first := client.Headers(HeaderInput{}).Get("x-opencode-session")
	second := client.Headers(HeaderInput{}).Get("x-opencode-session")
	if first == "" || first == second {
		t.Fatalf("session ids were not independently generated: %q == %q", first, second)
	}
	if !strings.HasPrefix(first, "ses_") || len(first) != len("ses_")+26 {
		t.Fatalf("session id does not use OpenCode shape: %q", first)
	}
	request := client.Headers(HeaderInput{}).Get("x-opencode-request")
	if !strings.HasPrefix(request, "msg_") || len(request) != len("msg_")+26 {
		t.Fatalf("request id does not use OpenCode shape: %q", request)
	}
}

func TestPrepareOpenCodeBodyKeepsClientFieldsAndAddsCompatibilityFields(t *testing.T) {
	const body = `{"model":"mimo-v2.5-free","messages":[{"role":"user","content":"hello"}],"stream":false,"temperature":0.35,"top_p":0.8,"max_tokens":321,"stop":["END"]}`

	prepared, err := prepareOpenCodeBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(prepared, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"temperature", "top_p", "max_tokens", "stop"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("prepared body dropped %s", field)
		}
	}
	var stream bool
	if err := json.Unmarshal(payload["stream"], &stream); err != nil || !stream {
		t.Fatalf("stream = %s, want true", payload["stream"])
	}
	if got := string(payload["stream_options"]); got != `{"include_usage":true}` {
		t.Fatalf("stream_options = %s", got)
	}
	if got := string(payload["tool_choice"]); got != `"none"` {
		t.Fatalf("tool_choice = %s", got)
	}

	var tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(payload["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Function.Name != "bash" || tools[1].Function.Name != "read" {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestOpenCodeHeadersUseCurrentClientShape(t *testing.T) {
	client, err := New(config.Config{
		UpstreamBaseURL: "https://opencode.ai/zen/v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	headers := client.Headers(HeaderInput{})
	checks := map[string]string{
		"Accept":             "*/*",
		"User-Agent":         "opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
		"x-opencode-client":  "cli",
		"x-opencode-project": "global",
	}
	for name, want := range checks {
		if got := headers.Get(name); got != want {
			t.Fatalf("header %s = %q, want %q", name, got, want)
		}
	}
}

func TestConfiguredProxyReceivesUpstreamRequest(t *testing.T) {
	proxyCalled := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled = true
		if r.Header.Get("Authorization") != "Bearer public" {
			t.Errorf("proxy saw authorization %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-opencode-session") == "" {
			t.Error("proxy did not receive x-opencode-session")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_proxy","choices":[]}`))
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client, err := New(config.Config{
		UpstreamBaseURL: "http://upstream.invalid/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.DoChat(context.Background(), []byte(`{"model":"m","messages":[]}`), HeaderInput{})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !proxyCalled {
		t.Fatal("configured proxy was not used")
	}
}

func TestConfiguredProxyOpensNewConnectionPerRequest(t *testing.T) {
	var connections atomic.Int32
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_proxy","choices":[]}`))
	}))
	proxy.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	proxy.Start()
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)

	client, err := New(config.Config{
		UpstreamBaseURL: "http://upstream.invalid/v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		response, err := client.DoChat(context.Background(), []byte(`{"model":"m","messages":[]}`), HeaderInput{})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}

	if got := connections.Load(); got < 2 {
		t.Fatalf("proxy connections = %d, want a new connection for each request", got)
	}
}
