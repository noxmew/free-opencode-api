package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/noxmew/free-opencode-api/internal/protocol"
)

const (
	defaultModelCatalogURL = "https://models.opencode.ai/api.json"
	protocolCacheTTL       = 10 * time.Minute
	protocolFailureTTL     = time.Minute
)

type capabilityProvider struct {
	ID     string                     `json:"id"`
	NPM    string                     `json:"npm"`
	API    string                     `json:"api"`
	Models map[string]capabilityModel `json:"models"`
}

type capabilityModel struct {
	ID       string                   `json:"id"`
	Provider *capabilityModelProvider `json:"provider"`
}

type capabilityModelProvider struct {
	NPM string `json:"npm"`
}

func (c *Client) ProtocolForModel(ctx context.Context, model string) protocol.Protocol {
	if !c.openCodeZen {
		return protocol.Chat
	}
	keys := modelLookupKeys(model)
	now := time.Now()

	c.protocolMu.RLock()
	if now.Before(c.protocolsExpires) {
		if result := lookupProtocol(c.protocols, keys); result.Valid() {
			c.protocolMu.RUnlock()
			return result
		}
		c.protocolMu.RUnlock()
		return protocol.Chat
	}
	c.protocolMu.RUnlock()

	c.protocolMu.Lock()
	defer c.protocolMu.Unlock()
	if now.Before(c.protocolsExpires) {
		if result := lookupProtocol(c.protocols, keys); result.Valid() {
			return result
		}
		return protocol.Chat
	}
	if err := c.refreshProtocolsLocked(ctx); err != nil {
		slog.Default().Warn("model protocol discovery failed; falling back to chat", "error", err)
		c.protocolsExpires = time.Now().Add(protocolFailureTTL)
	}
	if result := lookupProtocol(c.protocols, keys); result.Valid() {
		return result
	}
	return protocol.Chat
}

func modelLookupKeys(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if strings.HasSuffix(model, "-free") {
		keys := []string{model}
		if trimmed := strings.TrimSuffix(model, "-free"); trimmed != "" {
			keys = append(keys, trimmed)
		}
		return keys
	}
	// The gateway appends -free before forwarding, so the free model's own
	// protocol is authoritative even when a paid variant shares the name.
	return []string{model + "-free", model}
}

func lookupProtocol(protocols map[string]protocol.Protocol, keys []string) protocol.Protocol {
	for _, key := range keys {
		if value := protocols[key]; value.Valid() {
			return value
		}
	}
	return ""
}

func (c *Client) refreshProtocolsLocked(ctx context.Context) error {
	fetchContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchContext, http.MethodGet, c.catalogURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.identity.UserAgent)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("model catalog returned HTTP %d", response.StatusCode)
	}
	var providers map[string]capabilityProvider
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	if err := decoder.Decode(&providers); err != nil {
		return err
	}
	discovered := make(map[string]protocol.Protocol)
	providerKeys := zenCapabilityProviders(providers)
	for _, providerID := range providerKeys {
		provider := providers[providerID]
		for modelID, model := range provider.Models {
			if strings.TrimSpace(model.ID) != "" {
				modelID = model.ID
			}
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			npm := provider.NPM
			if model.Provider != nil && strings.TrimSpace(model.Provider.NPM) != "" {
				npm = model.Provider.NPM
			}
			discovered[modelID] = protocolFromSDK(npm)
		}
	}
	if len(discovered) == 0 {
		return fmt.Errorf("model catalog contained no OpenCode models")
	}
	if c.protocols == nil {
		c.protocols = make(map[string]protocol.Protocol)
	}
	for model, value := range discovered {
		c.protocols[model] = value
	}
	c.protocolsExpires = time.Now().Add(protocolCacheTTL)
	return nil
}

func zenCapabilityProviders(providers map[string]capabilityProvider) []string {
	if _, ok := providers["opencode"]; ok {
		return []string{"opencode"}
	}
	keys := make([]string, 0)
	for providerID := range providers {
		value := strings.ToLower(strings.TrimSpace(providerID))
		if strings.Contains(value, "opencode") && !strings.Contains(value, "go") {
			keys = append(keys, providerID)
		}
	}
	sort.Strings(keys)
	return keys
}

func protocolFromSDK(npm string) protocol.Protocol {
	value := strings.ToLower(strings.TrimSpace(npm))
	switch {
	case strings.Contains(value, "anthropic"):
		return protocol.Anthropic
	case strings.Contains(value, "openai-compatible"):
		return protocol.Chat
	case strings.Contains(value, "openai"):
		return protocol.Responses
	default:
		return protocol.Chat
	}
}

func modelCatalogURL(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		return configured
	}
	return defaultModelCatalogURL
}
