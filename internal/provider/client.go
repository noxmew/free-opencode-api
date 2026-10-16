package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/noxmew/free-opencode-api/internal/config"
)

type Client struct {
	endpoint          string
	responsesEndpoint string
	modelsEndpoint    string
	http              *http.Client
	identity          HeaderIdentity
	openCodeZen       bool
}

type HeaderIdentity struct {
	UserAgent         string
	OpenCodeProjectID string
}

type HeaderInput struct {
	Inbound http.Header
	Stream  bool
}

const openCodeClientName = "cli"

const openCodeUserAgent = "opencode/latest/2.0.29/cli"

//go:embed opencode_tools.json
var openCodeToolsJSON []byte

type openCodeToolDefinition struct {
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

var openCodeToolDefinitions = func() map[string]openCodeToolDefinition {
	definitions := make(map[string]openCodeToolDefinition)
	if err := json.Unmarshal(openCodeToolsJSON, &definitions); err != nil {
		panic(fmt.Sprintf("invalid embedded OpenCode tool definitions: %v", err))
	}
	return definitions
}()

var openCodeCoreToolNames = []string{"shell", "edit", "glob", "grep", "read"}

var canonicalOpenCodeSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{9}f[c-f][0-9a-f][0-9A-Za-z]{14}$`)

func New(cfg config.Config) (*Client, error) {
	httpClient, err := newHTTPClient()
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimRight(cfg.UpstreamBaseURL, "/")
	return &Client{
		endpoint:          baseURL + "/chat/completions",
		responsesEndpoint: baseURL + "/responses",
		modelsEndpoint:    baseURL + "/models",
		http:              httpClient,
		identity: HeaderIdentity{
			UserAgent:         openCodeUserAgent,
			OpenCodeProjectID: strings.TrimSpace(cfg.OpenCodeProjectID),
		},
		openCodeZen: isOpenCodeZenURL(cfg.UpstreamBaseURL),
	}, nil
}

func (c *Client) DoModels(ctx context.Context, input HeaderInput) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsEndpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Close = true
	request.Header = c.Headers(input)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer public")
	return c.http.Do(request)
}

func (c *Client) DoChat(ctx context.Context, body []byte, input HeaderInput) (*http.Response, error) {
	body = ensureFreeModel(body)
	if c.openCodeZen {
		var err error
		body, err = prepareOpenCodeBody(body)
		if err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Close = true
	request.Header = c.Headers(input)
	request.Header.Set("Authorization", "Bearer public")
	return c.http.Do(request)
}

func (c *Client) DoResponses(ctx context.Context, body []byte, input HeaderInput) (*http.Response, error) {
	body = ensureFreeModel(body)
	if c.openCodeZen {
		var err error
		body, err = prepareOpenCodeResponsesBody(body)
		if err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Close = true
	request.Header = c.Headers(input)
	request.Header.Set("Authorization", "Bearer public")
	return c.http.Do(request)
}

const freeModelSuffix = "-free"

func ensureFreeModel(body []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return body
	}

	var model string
	modelRaw, ok := raw["model"]
	if !ok || json.Unmarshal(modelRaw, &model) != nil || model == "" {
		return body
	}
	if strings.HasSuffix(model, freeModelSuffix) {
		return body
	}

	updatedModel, err := json.Marshal(model + freeModelSuffix)
	if err != nil {
		return body
	}
	raw["model"] = updatedModel
	updatedBody, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return updatedBody
}

func (c *Client) Headers(input HeaderInput) http.Header {
	headers := make(http.Header)
	for name, values := range input.Inbound {
		if isHopByHopHeader(name) {
			continue
		}
		for _, value := range values {
			headers.Add(name, value)
		}
	}

	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	if headers.Get("Accept") == "" {
		if c.openCodeZen {
			headers.Set("Accept", "application/json, text/event-stream")
		} else if input.Stream {
			headers.Set("Accept", "text/event-stream")
		} else {
			headers.Set("Accept", "application/json")
		}
	}
	if c.openCodeZen || headers.Get("User-Agent") == "" {
		// Match the User-Agent built by OpenCode's App.useragent().
		headers.Set("User-Agent", c.identity.UserAgent)
	}
	if c.openCodeZen || headers.Get("x-opencode-client") == "" {
		headers.Set("x-opencode-client", openCodeClientName)
	}

	if c.openCodeZen {
		// Zen's free-tier gate requires matching session and affinity IDs, and
		// rejects the request-correlation header used by older gateway clients.
		session := zenSessionID(input)
		headers.Set("x-opencode-session", session)
		headers.Set("x-opencode-session-id", session)
		headers.Set("x-session-affinity", session)
		headers.Set("X-Session-Id", session)
		headers.Del("x-opencode-request")
		headers.Del("x-opencode-request-id")
	} else {
		headers.Set("x-opencode-session", sessionID(input))
		if value := cleanHeaderValue(headers.Get("x-opencode-request")); value != "" {
			headers.Set("x-opencode-request", value)
		} else if value := cleanHeaderValue(headers.Get("x-opencode-request-id")); value != "" {
			headers.Set("x-opencode-request", value)
		} else {
			headers.Set("x-opencode-request", requestID(input))
		}
	}

	projectID := cleanHeaderValue(headers.Get("x-opencode-project"))
	if projectID == "" {
		projectID = cleanHeaderValue(c.identity.OpenCodeProjectID)
	}
	if c.openCodeZen {
		if projectID == "" {
			projectID = "global"
		}
		projectHash := sha256.Sum256([]byte("opencode-project\x00" + projectID))
		projectID = hex.EncodeToString(projectHash[:20])
	}
	if projectID != "" {
		headers.Set("x-opencode-project", projectID)
	}

	// The gateway's public upstream credential is independent from the
	// optional credential used to authorize requests to the gateway itself.
	headers.Set("Authorization", "Bearer public")
	return headers
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "accept-encoding":
		// Let the Go transport negotiate compression and decode it before the
		// response is written back to the client.
		return true
	case "connection", "content-length", "host", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func isOpenCodeZenURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "opencode.ai") &&
		strings.HasPrefix(strings.TrimRight(parsed.Path, "/"), "/zen/")
}

func prepareOpenCodeBody(body []byte) ([]byte, error) {
	return prepareOpenCodeRequestBody(body, false)
}

func prepareOpenCodeResponsesBody(body []byte) ([]byte, error) {
	return prepareOpenCodeRequestBody(body, true)
}

func sessionID(input HeaderInput) string {
	for _, name := range []string{"x-opencode-session-id", "x-session-id", "x-opencode-session"} {
		if value := cleanHeaderValue(input.Inbound.Get(name)); value != "" {
			return value
		}
	}
	return openCodeID("ses", true)
}

func requestID(input HeaderInput) string {
	for _, name := range []string{"x-opencode-request", "x-opencode-request-id"} {
		if value := cleanHeaderValue(input.Inbound.Get(name)); value != "" {
			return value
		}
	}
	return openCodeID("msg", false)
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randomID(prefix string) string {
	var value [12]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return openCodeID(strings.TrimSuffix(prefix, "_"), false)
	}
	return prefix + hex.EncodeToString(value[:])
}

var openCodeIDState struct {
	sync.Mutex
	timestamp int64
	counter   uint64
}

func openCodeID(prefix string, descending bool) string {
	now := time.Now().UnixMilli()
	openCodeIDState.Lock()
	if openCodeIDState.timestamp != now {
		openCodeIDState.timestamp = now
		openCodeIDState.counter = 0
	}
	openCodeIDState.counter++
	value := uint64(now)*0x1000 + openCodeIDState.counter
	openCodeIDState.Unlock()
	if descending {
		value = ^value
	}

	var timestamp [6]byte
	for index := len(timestamp) - 1; index >= 0; index-- {
		timestamp[index] = byte(value)
		value >>= 8
	}
	var random [14]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return prefix + "_" + hex.EncodeToString(timestamp[:]) + strings.Repeat("0", len(random))
	}
	suffix := make([]byte, len(random))
	for index, item := range random {
		suffix[index] = base62[int(item)%len(base62)]
	}
	return prefix + "_" + hex.EncodeToString(timestamp[:]) + string(suffix)
}

func NewRequestID() string {
	return randomID("req_")
}

func cleanHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func newHTTPClient() (*http.Client, error) {
	proxy := func(_ *http.Request) (*url.URL, error) {
		return nil, nil
	}
	if proxyRaw := strings.TrimSpace(os.Getenv("HTTP_PROXY")); proxyRaw != "" {
		proxyURL, err := url.Parse(proxyRaw)
		if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
			return nil, fmt.Errorf("HTTP_PROXY must be an http or https URL")
		}
		proxy = http.ProxyURL(proxyURL)
	}
	transport := &http.Transport{
		Proxy:                 proxy,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: transport}, nil
}
