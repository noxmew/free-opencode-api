package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr string

	ServiceAPIKey string

	UpstreamBaseURL string
	ModelCatalogURL string

	RequestTimeout       time.Duration
	OpenCodeProjectID    string
	StripFreeModelSuffix bool
}

const (
	defaultListenAddr      = ":8080"
	defaultUpstreamBaseURL = "https://opencode.ai/zen/v1"
	defaultModelCatalogURL = "https://models.opencode.ai/api.json"
)

func Load() (Config, error) {
	c := Config{
		ListenAddr:        defaultListenAddr,
		ServiceAPIKey:     strings.TrimSpace(os.Getenv("SERVICE_API_KEY")),
		UpstreamBaseURL:   defaultUpstreamBaseURL,
		ModelCatalogURL:   defaultModelCatalogURL,
		OpenCodeProjectID: strings.TrimSpace(os.Getenv("OPENCODE_PROJECT_ID")),
		RequestTimeout:    5 * time.Minute,
	}

	if value := strings.TrimSpace(os.Getenv("MODEL_CATALOG_URL")); value != "" {
		c.ModelCatalogURL = value
	}

	var err error
	if c.RequestTimeout, err = durationEnv("REQUEST_TIMEOUT", c.RequestTimeout); err != nil {
		return Config{}, err
	}
	if c.RequestTimeout <= 0 {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT must be greater than zero")
	}
	if c.StripFreeModelSuffix, err = boolEnv("STRIP_FREE_MODEL_SUFFIX", false); err != nil {
		return Config{}, err
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

func boolEnv(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}
