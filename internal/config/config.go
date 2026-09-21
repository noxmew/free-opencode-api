package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr string

	ServiceAPIKey string

	UpstreamBaseURL string

	RequestTimeout    time.Duration
	OpenCodeProjectID string
}

const (
	defaultListenAddr      = ":8080"
	defaultUpstreamBaseURL = "https://opencode.ai/zen/v1"
)

func Load() (Config, error) {
	c := Config{
		ListenAddr:        defaultListenAddr,
		ServiceAPIKey:     strings.TrimSpace(os.Getenv("SERVICE_API_KEY")),
		UpstreamBaseURL:   defaultUpstreamBaseURL,
		OpenCodeProjectID: strings.TrimSpace(os.Getenv("OPENCODE_PROJECT_ID")),
		RequestTimeout:    5 * time.Minute,
	}

	var err error
	if c.RequestTimeout, err = durationEnv("REQUEST_TIMEOUT", c.RequestTimeout); err != nil {
		return Config{}, err
	}
	if c.RequestTimeout <= 0 {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT must be greater than zero")
	}
	return c, nil
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
