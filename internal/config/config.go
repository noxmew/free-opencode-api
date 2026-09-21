package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr string

	ServiceAPIKey string

	UpstreamBaseURL string
	UpstreamProxy   string

	RequestTimeout    time.Duration
	MaxBodyBytes      int64
	ClientVersion     string
	UpstreamUserAgent string
	OpenCodeProjectID string
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:        envOr("LISTEN_ADDR", ":8080"),
		ServiceAPIKey:     strings.TrimSpace(os.Getenv("SERVICE_API_KEY")),
		UpstreamBaseURL:   strings.TrimRight(envOr("UPSTREAM_BASE_URL", "https://opencode.ai/zen/v1"), "/"),
		UpstreamProxy:     strings.TrimSpace(os.Getenv("UPSTREAM_PROXY")),
		ClientVersion:     envOr("UPSTREAM_CLIENT_VERSION", "1.18.31"),
		UpstreamUserAgent: strings.TrimSpace(os.Getenv("UPSTREAM_USER_AGENT")),
		OpenCodeProjectID: strings.TrimSpace(os.Getenv("OPENCODE_PROJECT_ID")),
		RequestTimeout:    5 * time.Minute,
		MaxBodyBytes:      1 << 20,
	}

	var err error
	if c.RequestTimeout, err = durationEnv("REQUEST_TIMEOUT", c.RequestTimeout); err != nil {
		return Config{}, err
	}
	if c.MaxBodyBytes, err = int64Env("MAX_BODY_BYTES", c.MaxBodyBytes); err != nil {
		return Config{}, err
	}

	if c.ServiceAPIKey == "" {
		return Config{}, fmt.Errorf("SERVICE_API_KEY is required")
	}
	if err := validateHTTPURL("UPSTREAM_BASE_URL", c.UpstreamBaseURL); err != nil {
		return Config{}, err
	}
	if c.UpstreamProxy != "" {
		if err := validateProxyURL(c.UpstreamProxy); err != nil {
			return Config{}, err
		}
	}
	if c.RequestTimeout <= 0 {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT must be greater than zero")
	}
	if c.MaxBodyBytes <= 0 {
		return Config{}, fmt.Errorf("MAX_BODY_BYTES must be greater than zero")
	}
	return c, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

func int64Env(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

func validateHTTPURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an http or https URL", name)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain credentials or a fragment", name)
	}
	return nil
}

func validateProxyURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("UPSTREAM_PROXY must be an http or https URL")
	}
	return nil
}
