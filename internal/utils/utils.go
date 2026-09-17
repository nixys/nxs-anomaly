package utils

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TruncateRunes caps s at n runes, never splitting one.
//
// The obvious `s[:n]` caps at n *bytes*, which cuts a multi-byte rune in half and
// yields invalid UTF-8. PostgreSQL rejects that outright ("invalid byte sequence
// for encoding UTF8"), and the Telegram API rejects it too — so on Cyrillic text,
// which is two bytes per letter here, a byte cap turns a long alert into a failed
// insert or an undelivered notification. It also silently halves the visible
// limit. Every cap on externally supplied text goes through this.
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	// Fast path: a string can never hold more runes than bytes.
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// MakeID creates a prefixed random ID like "usr_abc123def456"
func MakeID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// UTCNow returns current UTC time.
func UTCNow() time.Time {
	return time.Now().UTC()
}

// ToISO formats time as RFC3339 without sub-seconds e.g. "2026-05-10T10:00:00+00:00"
func ToISO(t time.Time) string {
	t = t.UTC().Truncate(time.Second)
	return t.Format("2006-01-02T15:04:05+00:00")
}

// ParseDatetime parses an ISO8601 datetime string to UTC time.
// Handles "Z" suffix and naive strings (assumed UTC).
func ParseDatetime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "Z") {
		s = s[:len(s)-1] + "+00:00"
	}
	formats := []string{
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02",
	}
	var lastErr error
	for _, f := range formats {
		t, err := time.Parse(f, s)
		if err == nil {
			if t.Location() == time.UTC || t.Location().String() == "UTC" {
				return t.UTC(), nil
			}
			return t.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("cannot parse datetime %q: %w", s, lastErr)
}

// JSONDumps serializes v to compact JSON.
func JSONDumps(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// CloneMap returns a shallow copy of m.
func CloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// CoerceStringList validates and converts []any to []string.
func CoerceStringList(v any) ([]string, error) {
	if v == nil {
		return []string{}, nil
	}
	sl, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a list of strings")
	}
	result := make([]string, len(sl))
	for i, item := range sl {
		result[i] = fmt.Sprintf("%v", item)
	}
	return result, nil
}

// CoerceLabelMap validates and converts map[string]any to map[string]string.
func CoerceLabelMap(v any) (map[string]string, error) {
	if v == nil {
		return map[string]string{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected an object with string labels")
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		result[k] = scalarString(val)
	}
	return result, nil
}

// scalarString renders a decoded JSON value as text. JSON numbers decode as
// float64, and %v prints those in %g form, so a label of 1000000 became
// "1e+06" — a different string from what the source sent, in labels that
// group, route and are shown to people.
func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	}
	return fmt.Sprintf("%v", v)
}

// EnsureRequired checks that all keys are present and non-empty in data.
func EnsureRequired(data map[string]any, keys []string) error {
	var missing []string
	for _, k := range keys {
		v, ok := data[k]
		if !ok || v == nil || v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// PickFirst returns first non-empty string pointer value, or defaultVal.
func PickFirst(values []*string, defaultVal string) string {
	for _, v := range values {
		if v != nil && *v != "" {
			return *v
		}
	}
	return defaultVal
}

// StrVal safely gets a string value from a map.
func StrVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return scalarString(v)
}

// IntVal safely gets an int value from a map.
func IntVal(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}

// BoolVal safely gets a bool value from a map.
func BoolVal(m map[string]any, key string, defaultVal bool) bool {
	if m == nil {
		return defaultVal
	}
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return defaultVal
}

// StringPtr returns a pointer to a string, for use with PickFirst.
func StringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
