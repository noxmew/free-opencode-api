package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noxmew/free-opencode-api/internal/config"
)

func TestClientEnsuresFreeModelSuffix(t *testing.T) {
	type testCase struct {
		name  string
		path  string
		body  string
		model string
	}
	tests := []testCase{
		{
			name:  "chat model without suffix",
			path:  "/chat/completions",
			body:  `{"model":"mimo-v2.6-flash","messages":[]}`,
			model: "mimo-v2.6-flash-free",
		},
		{
			name:  "responses model without suffix",
			path:  "/responses",
			body:  `{"model":"muse-spark-1.3-contributor","input":"hello"}`,
			model: "muse-spark-1.3-contributor-free",
		},
		{
			name:  "chat model with suffix",
			path:  "/chat/completions",
			body:  `{"model":"mimo-v2.6-flash-free","messages":[]}`,
			model: "mimo-v2.6-flash-free",
		},
		{
			name:  "responses model with suffix",
			path:  "/responses",
			body:  `{"model":"muse-spark-1.3-contributor-free","input":"hello"}`,
			model: "muse-spark-1.3-contributor-free",
		},
	}

	type capturedRequest struct {
		path string
		body []byte
	}
	requests := make(chan capturedRequest, len(tests))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "unable to read request", http.StatusBadRequest)
			return
		}
		requests <- capturedRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client, err := New(config.Config{UpstreamBaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response *http.Response
			var err error
			if test.path == "/chat/completions" {
				response, err = client.DoChat(context.Background(), []byte(test.body), HeaderInput{})
			} else {
				response, err = client.DoResponses(context.Background(), []byte(test.body), HeaderInput{})
			}
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()

			captured := <-requests
			if captured.path != test.path {
				t.Fatalf("path = %q, want %q", captured.path, test.path)
			}
			var payload struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(captured.body, &payload); err != nil {
				t.Fatalf("invalid forwarded body: %v", err)
			}
			if payload.Model != test.model {
				t.Errorf("forwarded model = %q, want %q", payload.Model, test.model)
			}
		})
	}
}

func TestOpenCodeHeadersMatchModelRequest(t *testing.T) {
	client, err := New(config.Config{
		UpstreamBaseURL:   "https://opencode.ai/zen/v1",
		OpenCodeProjectID: "prj_test",
	})
	if err != nil {
		t.Fatal(err)
	}

	headers := client.Headers(HeaderInput{Inbound: http.Header{
		"X-Opencode-Session":    []string{"ses_current"},
		"X-Opencode-Request-Id": []string{"msg_current"},
	}})

	want := map[string]string{
		"User-Agent":         openCodeUserAgent,
		"x-opencode-client":  openCodeClientName,
		"x-opencode-session": "ses_current",
		"x-opencode-request": "msg_current",
		"x-opencode-project": "prj_test",
		"Authorization":      "Bearer public",
	}
	for name, expected := range want {
		if got := headers.Get(name); got != expected {
			t.Errorf("%s = %q, want %q", name, got, expected)
		}
	}
}
