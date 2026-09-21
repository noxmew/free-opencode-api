package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rain/free-opencode-api/internal/config"
)

type Client struct {
	endpoint          string
	responsesEndpoint string
	modelsEndpoint    string
	http              *http.Client
	identity          HeaderIdentity
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

const openCodeUserAgent = "opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14"

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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Close = true
	request.Header = c.Headers(input)
	request.Header.Set("Authorization", "Bearer public")
	return c.http.Do(request)
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
		if input.Stream {
			headers.Set("Accept", "text/event-stream")
		} else {
			headers.Set("Accept", "application/json")
		}
	}
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", c.identity.UserAgent)
	}
	if headers.Get("x-opencode-client") == "" {
		headers.Set("x-opencode-client", openCodeClientName)
	}

	if value := cleanHeaderValue(headers.Get("x-opencode-session")); value != "" {
		headers.Set("x-opencode-session", value)
	} else if value := cleanHeaderValue(headers.Get("x-session-id")); value != "" {
		headers.Set("x-opencode-session", value)
	} else {
		headers.Set("x-opencode-session", sessionID(input))
	}
	if value := cleanHeaderValue(headers.Get("x-opencode-request")); value != "" {
		headers.Set("x-opencode-request", value)
	} else {
		headers.Set("x-opencode-request", requestID(input))
	}

	projectID := cleanHeaderValue(headers.Get("x-opencode-project"))
	if projectID == "" {
		projectID = cleanHeaderValue(c.identity.OpenCodeProjectID)
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
	case "connection", "content-length", "host", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func sessionID(input HeaderInput) string {
	for _, name := range []string{"x-opencode-session", "x-session-id"} {
		if value := cleanHeaderValue(input.Inbound.Get(name)); value != "" {
			return value
		}
	}
	return openCodeID("ses", true)
}

func requestID(input HeaderInput) string {
	if value := cleanHeaderValue(input.Inbound.Get("x-opencode-request")); value != "" {
		return value
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
