package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// Fuzz targets for the ingest normalizers.
//
// These functions are the first thing an untrusted payload touches: every field
// they read comes from whatever Alertmanager, PagerDuty, VictorOps, Grafana or a
// legacy nxs-alert pool decided to POST, and the map they return is written
// straight into a jsonb column. The table tests next door check the shapes we
// expect; these check the shapes we don't.
//
// Corpus lives in testdata/fuzz/<FuzzName>/. The seeds below are deliberately
// hostile — wrong types where an object is expected, nested nulls, oversized and
// multi-byte strings — because the normalizers are written in terms of
// type-asserting helpers whose failure mode is a zero value rather than an error.

// checkNormalized asserts the invariants every normalizer output must hold,
// whatever the input was.
func checkNormalized(t *testing.T, source string, out map[string]any) {
	t.Helper()

	if out == nil {
		t.Fatalf("%s: normalizer returned a nil map", source)
	}

	// The result is written to a jsonb column. Anything that cannot be marshalled
	// would surface as an ingest 500 at runtime, not here.
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("%s: result is not JSON-serialisable: %v", source, err)
	}
	// json.Marshal escapes invalid UTF-8 into U+FFFD rather than failing, so the
	// encoded form cannot prove the strings were clean. Walk the values instead.
	assertUTF8(t, source, "", out)
	_ = encoded

	// The engine indexes on these downstream; an empty title produces an alert
	// nobody can identify in the timeline.
	title, ok := out["title"].(string)
	if !ok || title == "" {
		t.Fatalf("%s: title must be a non-empty string, got %#v", source, out["title"])
	}
	if got, ok := out["source"].(string); !ok || got == "" {
		t.Fatalf("%s: source must be a non-empty string, got %#v", source, out["source"])
	}
	if _, ok := out["status"].(string); !ok {
		t.Fatalf("%s: status must be a string, got %#v", source, out["status"])
	}
	if _, ok := out["severity"].(string); !ok {
		t.Fatalf("%s: severity must be a string, got %#v", source, out["severity"])
	}

	// labels and annotations are consumed as maps by routing and templating. A
	// payload that turned either into a scalar would panic there, not here.
	for _, key := range []string{"labels", "annotations"} {
		if _, ok := out[key].(map[string]any); !ok {
			t.Fatalf("%s: %s must be an object, got %#v", source, key, out[key])
		}
	}
}

// assertUTF8 walks a decoded-JSON value and fails on any string that is not valid
// UTF-8. PostgreSQL rejects such a string on insert, so producing one turns a
// merely weird alert into a failed ingest.
func assertUTF8(t *testing.T, source, path string, v any) {
	t.Helper()
	switch x := v.(type) {
	case string:
		if !utf8.ValidString(x) {
			t.Fatalf("%s: invalid UTF-8 at %s: %q", source, path, x)
		}
	case map[string]any:
		for k, val := range x {
			if !utf8.ValidString(k) {
				t.Fatalf("%s: invalid UTF-8 in key at %s: %q", source, path, k)
			}
			assertUTF8(t, source, path+"."+k, val)
		}
	case []any:
		for i, val := range x {
			assertUTF8(t, source, path+"[]", val)
			_ = i
		}
	}
}

// decodeObject turns a fuzz-supplied byte slice into a JSON object, skipping the
// input if it is not one. Fuzzing the raw bytes rather than individual fields is
// what exercises the type assertions: json.Unmarshal happily produces a float64
// or a []any where the normalizer asserts map[string]any.
func decodeObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	if len(data) > 1<<16 {
		t.Skip("input larger than any realistic webhook body")
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		t.Skip("not a JSON object")
	}
	return m
}

func FuzzNormalizeAlertmanagerAlert(f *testing.F) {
	f.Add([]byte(`{"status":"firing","receiver":"web","groupKey":"g1","groupLabels":{"alertname":"Down"},"commonLabels":{"severity":"critical"},"alerts":[{"status":"firing","labels":{"alertname":"Down","severity":"critical"},"annotations":{"summary":"host down"},"fingerprint":"abc","startsAt":"2026-01-01T00:00:00Z"}]}`))
	// Nothing is the type it should be.
	f.Add([]byte(`{"status":42,"receiver":null,"groupLabels":[1,2],"commonLabels":"x","alerts":[{"labels":7,"annotations":null,"fingerprint":{"a":1}}]}`))
	// Multi-byte everywhere — the truncation-safety case.
	f.Add([]byte(`{"status":"firing","alerts":[{"labels":{"alertname":"Сервер недоступен","severity":"критический"},"annotations":{"summary":"Хост не отвечает уже пять минут подряд"}}]}`))
	// Empty strings must not produce an empty title.
	f.Add([]byte(`{"alerts":[{"labels":{"alertname":""},"annotations":{"summary":"","description":""}}]}`))
	// Deeply nested values inside a label map.
	f.Add([]byte(`{"alerts":[{"labels":{"a":{"b":{"c":[null,true,1.5]}}}}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		envelope := decodeObject(t, data)
		alert, _ := envelope["alerts"].([]any)
		var first map[string]any
		if len(alert) > 0 {
			first, _ = alert[0].(map[string]any)
		}
		if first == nil {
			// Still worth normalizing: the envelope-only path is what a malformed
			// alerts[] entry falls back to in the handler.
			first = map[string]any{}
		}
		checkNormalized(t, "alertmanager", normalizeAlertmanagerAlert(envelope, first))
	})
}

func FuzzNormalizePagerDutyAlert(f *testing.F) {
	f.Add([]byte(`{"event_action":"trigger","routing_key":"r1","dedup_key":"d1","client":"mon","payload":{"summary":"disk full","severity":"critical","source":"db-1","component":"disk","custom_details":{"free":"1%"}}}`))
	f.Add([]byte(`{"event_action":["resolve"],"payload":"not-an-object","links":{"a":1}}`))
	f.Add([]byte(`{"payload":{"summary":"Диск переполнен","severity":"КРИТИЧЕСКИЙ","custom_details":{"свободно":"1%"}}}`))
	f.Add([]byte(`{"event_action":"","payload":{"summary":"","severity":""}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		checkNormalized(t, "pagerduty", normalizePagerDutyAlert(decodeObject(t, data)))
	})
}

func FuzzNormalizeVictorOpsAlert(f *testing.F) {
	f.Add([]byte(`{"message_type":"CRITICAL","entity_id":"e1","entity_display_name":"Down","state_message":"host down","host_name":"db-1","service":"pg"}`))
	f.Add([]byte(`{"message_type":99,"entity_id":null,"state_message":{"a":1}}`))
	f.Add([]byte(`{"message_type":"RECOVERY","state_message":"Хост восстановился"}`))
	f.Add([]byte(`{"message_type":"UNKNOWN_TYPE","entity_id":""}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		checkNormalized(t, "victorops", normalizeVictorOpsAlert(decodeObject(t, data)))
	})
}

func FuzzNormalizeGrafanaAlertingAlert(f *testing.F) {
	f.Add([]byte(`{"receiver":"web","status":"firing","state":"alerting","title":"[FIRING:1] Down","orgId":1,"groupLabels":{"alertname":"Down"},"alerts":[{"status":"firing","labels":{"alertname":"Down","severity":"critical"},"annotations":{"summary":"host down"},"valueString":"[ var='B' value=1 ]","values":{"B":1}}]}`))
	f.Add([]byte(`{"state":["ok"],"title":null,"orgId":"x","alerts":[{"labels":null,"values":[1,2],"valueString":{"a":1}}]}`))
	f.Add([]byte(`{"state":"ok","alerts":[{"annotations":{"summary":"Правило сработало"}}]}`))
	f.Add([]byte(`{"alerts":[{}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		envelope := decodeObject(t, data)
		alerts, _ := envelope["alerts"].([]any)
		var first map[string]any
		if len(alerts) > 0 {
			first, _ = alerts[0].(map[string]any)
		}
		if first == nil {
			first = map[string]any{}
		}
		checkNormalized(t, "grafana_alerting", normalizeGrafanaAlertingAlert(envelope, first))
	})
}

// FuzzLegacyTitle guards the one place ingest shortens operator-supplied text.
// The cap must never split a rune: the title goes into a text column, and
// PostgreSQL rejects an invalid byte sequence outright — so on a long Cyrillic
// triggerMessage a byte-wise cap turns a valid alert into a failed insert.
func FuzzLegacyTitle(f *testing.F) {
	f.Add("host db-1 is down")
	f.Add("")
	f.Add("first line\nsecond line")
	f.Add(strings.Repeat("Сервер недоступен ", 40))
	f.Add(strings.Repeat("a", 500))
	f.Add(strings.Repeat("💥", 200))
	f.Add("\n")
	f.Add(strings.Repeat("Ы", 159) + "\nx")

	f.Fuzz(func(t *testing.T, msg string) {
		title := legacyTitle(msg)

		// The cap must not *create* invalid UTF-8. It is not asked to repair it:
		// a real triggerMessage arrives through json.Unmarshal, which already
		// replaces malformed bytes with U+FFFD, so a garbage-in case only has to
		// stay garbage-out rather than become a sanitiser nothing calls for.
		if utf8.ValidString(msg) && !utf8.ValidString(title) {
			t.Fatalf("legacyTitle turned valid UTF-8 into invalid: %q", title)
		}
		if n := utf8.RuneCountInString(title); n > 160 {
			t.Fatalf("legacyTitle returned %d runes, want <= 160", n)
		}
		if strings.Contains(title, "\n") {
			t.Fatalf("legacyTitle kept a newline: %q", title)
		}
		// It must be a prefix of the input, never invented text.
		if !strings.HasPrefix(msg, title) {
			t.Fatalf("legacyTitle %q is not a prefix of %q", title, msg)
		}
		// Nothing was dropped unless there was a reason to drop it.
		firstLine := msg
		if nl := strings.Index(msg, "\n"); nl >= 0 {
			firstLine = msg[:nl]
		}
		if utf8.RuneCountInString(firstLine) <= 160 && title != firstLine {
			t.Fatalf("legacyTitle shortened a title that fit: %q -> %q", firstLine, title)
		}
	})
}

// FuzzParseLegacyAlertChannels covers the channel-selection defaults. The legacy
// nxs-alert pool sends a free-form string, and the fallbacks decide who gets
// woken up — an input that produced an empty channel set would silently page
// nobody.
func FuzzParseLegacyAlertChannels(f *testing.F) {
	f.Add("telegram,call")
	f.Add("")
	f.Add("email")
	f.Add("telegram telegram telegram")
	f.Add(",,, ,\t\n")
	f.Add("TELEGRAM,Call")
	f.Add("webhook,unknown-channel,call")
	f.Add(strings.Repeat("email,", 1000))

	f.Fuzz(func(t *testing.T, in string) {
		got := parseLegacyAlertChannels(in)

		if len(got) == 0 {
			t.Fatal("parseLegacyAlertChannels returned no channels: nobody would be paged")
		}
		seen := map[string]bool{}
		for _, ch := range got {
			if !supportedNotificationTargets[ch] {
				t.Fatalf("unsupported channel %q in result %v", ch, got)
			}
			if seen[ch] {
				t.Fatalf("duplicate channel %q in result %v", ch, got)
			}
			seen[ch] = true
		}
		// email is unconditional, and telegram+call are the pair the legacy pool
		// falls back to when it named neither.
		if !seen["email"] {
			t.Fatalf("email missing from %v", got)
		}
		if !seen["telegram"] && !seen["call"] {
			t.Fatalf("neither telegram nor call in %v", got)
		}
	})
}

// FuzzTruncateRunes pins the helper the truncation fixes above depend on.
func FuzzTruncateRunes(f *testing.F) {
	f.Add("hello", 3)
	f.Add("", 0)
	f.Add("Привет", 3)
	f.Add("💥💥💥", 2)
	f.Add("abc", -1)
	f.Add("abc", 100)

	f.Fuzz(func(t *testing.T, s string, n int) {
		if n > 1<<20 {
			t.Skip("absurd cap")
		}
		got := utils.TruncateRunes(s, n)

		if !strings.HasPrefix(s, got) {
			t.Fatalf("TruncateRunes(%q, %d) = %q, not a prefix", s, n, got)
		}
		if n > 0 && utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("TruncateRunes(%q, %d) = %q, invalid UTF-8 from valid input", s, n, got)
		}
		if n <= 0 {
			if got != "" {
				t.Fatalf("TruncateRunes(%q, %d) = %q, want empty", s, n, got)
			}
			return
		}
		if c := utf8.RuneCountInString(got); c > n {
			t.Fatalf("TruncateRunes(%q, %d) kept %d runes", s, n, c)
		}
		// Only shorten when there was something to cut.
		if utf8.RuneCountInString(s) <= n && got != s {
			t.Fatalf("TruncateRunes(%q, %d) = %q, should be unchanged", s, n, got)
		}
	})
}
