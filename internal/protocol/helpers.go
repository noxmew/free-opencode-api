package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func asBool(value any) bool {
	valueBool, _ := value.(bool)
	return valueBool
}

func asNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok
}

func stringAt(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	return asString(object[key])
}

func mapAt(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	return asMap(object[key])
}

func sliceAt(object map[string]any, key string) []any {
	if object == nil {
		return nil
	}
	return asSlice(object[key])
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringify(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func cloneMap(object map[string]any) map[string]any {
	if object == nil {
		return nil
	}
	cloned := make(map[string]any, len(object))
	for key, value := range object {
		cloned[key] = value
	}
	return cloned
}

func newID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return prefix + "000000000000000000000000"
	}
	return prefix + hex.EncodeToString(value[:])
}

func NewResponseID() string {
	return newID("resp_")
}

func normalizeResponseID(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "resp_") {
		return value
	}
	if value == "" {
		return NewResponseID()
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return -1
		}
	}, value)
	if cleaned == "" {
		return NewResponseID()
	}
	return "resp_" + cleaned
}

func copyIfPresent(dst, src map[string]any, sourceKey, destinationKey string) {
	if value, ok := src[sourceKey]; ok {
		dst[destinationKey] = value
	}
}

func firstMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func nonEmpty(text string) bool {
	return strings.TrimSpace(text) != ""
}
