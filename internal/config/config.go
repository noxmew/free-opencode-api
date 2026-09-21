package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr string

	UpstreamBaseURL string
	UpstreamProxy   string

	RequestTimeout    time.Duration
	OpenCodeProjectID string
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:        envOr("LISTEN_ADDR", ":8080"),
		UpstreamBaseURL:   strings.TrimRight(envOr("UPSTREAM_BASE_URL", "https://opencode.ai/zen/v1"), "/"),
		UpstreamProxy:     strings.TrimSpace(os.Getenv("UPSTREAM_PROXY")),
		OpenCodeProjectID: strings.TrimSpace(os.Getenv("OPENCODE_PROJECT_ID")),
		RequestTimeout:    5 * time.Minute,
	}

	var err error
	if c.RequestTimeout, err = durationEnv("REQUEST_TIMEOUT", c.RequestTimeout); err != nil {
		return Config{}, err
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
