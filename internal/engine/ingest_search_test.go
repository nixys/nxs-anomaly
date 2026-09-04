package engine

import "testing"

// The OpenSearch and Elasticsearch normalizers translate two things no template
// can express: a severity scale and what counts as one alert over time. Both are
// silent when wrong — a mis-mapped severity sorts an incident to the bottom of
// the on-call queue, a mis-built dedupe key opens a new group per notification
// instead of closing the old one — so they are asserted field by field here
// rather than only through the handler.

func TestNormalizeOpenSearchAlertSeverity(t *testing.T) {
	// 1 is the plugin's highest severity, and the two scales have the same five
	// levels; a value outside the scale is carried through as itself.
	cases := map[string]string{
		"1": "critical", "2": "error", "3": "warning", "4": "info", "5": "debug",
		"": "warning", "99": "99", "critical": "critical",
	}
	for in, want := range cases {
		out := normalizeOpenSearchAlert(map[string]any{
			"trigger": map[string]any{"severity": in},
		}, nil)
		if got := out["severity"]; got != want {
			t.Errorf("severity %q → %v, want %q", in, got, want)
		}
	}
}

func TestNormalizeOpenSearchAlertStatus(t *testing.T) {
	envelope := map[string]any{"status": "firing", "monitor": map[string]any{"id": "m1"}}

	if got := normalizeOpenSearchAlert(envelope, nil)["status"]; got != "firing" {
		t.Errorf("envelope firing → %v, want firing", got)
	}
	// COMPLETED is the plugin's own word for a recovered alert and is not one of
	// the statuses the engine already treats as a closure.
	if got := normalizeOpenSearchAlert(map[string]any{"status": "COMPLETED"}, nil)["status"]; got != "resolved" {
		t.Errorf("COMPLETED → %v, want resolved", got)
	}
	// One recovered bucket inside a firing envelope closes only its own group.
	entry := map[string]any{"bucket_keys": "host-a", "status": "COMPLETED"}
	if got := normalizeOpenSearchAlert(envelope, entry)["status"]; got != "resolved" {
		t.Errorf("bucket COMPLETED in a firing envelope → %v, want resolved", got)
	}
}

func TestNormalizeOpenSearchAlertDedupeKey(t *testing.T) {
	monitor := map[string]any{"id": "m1", "name": "5xx rate"}
	trigger := map[string]any{"id": "t1", "name": "too many 5xx"}

	cases := []struct {
		name     string
		envelope map[string]any
		entry    map[string]any
		want     string
	}{
		{
			name:     "ids",
			envelope: map[string]any{"monitor": monitor, "trigger": trigger},
			want:     "m1:t1",
		},
		{
			name:     "bucket keys make each bucket its own group",
			envelope: map[string]any{"monitor": monitor, "trigger": trigger},
			entry:    map[string]any{"bucket_keys": "host-a"},
			want:     "m1:t1:host-a",
		},
		{
			// A template that omitted the ids still has to group: names are stable
			// enough to be the fallback, and an empty part must not survive as an
			// empty segment — "m1::host-a" and "m1:host-a" would be two groups for
			// one alert.
			name: "names when ids are unfilled",
			envelope: map[string]any{
				"monitor": map[string]any{"name": "5xx rate"},
				"trigger": map[string]any{"name": ""},
			},
			entry: map[string]any{"bucket_keys": "host-a"},
			want:  "5xx rate:host-a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeOpenSearchAlert(tc.envelope, tc.entry)
			if got := out["dedupe_key"]; got != tc.want {
				t.Errorf("dedupe_key = %v, want %q", got, tc.want)
			}
			if got := out["fingerprint"]; got != tc.want {
				t.Errorf("fingerprint = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeOpenSearchAlertFields(t *testing.T) {
	out := normalizeOpenSearchAlert(map[string]any{
		"monitor":      map[string]any{"id": "m1", "name": "5xx rate"},
		"trigger":      map[string]any{"id": "t1", "name": "too many 5xx", "severity": "1"},
		"period_start": "2026-01-01T00:00:00Z",
		"period_end":   "2026-01-01T00:05:00Z",
		"hits":         "42",
		"url":          "https://os.example.com/app/alerting",
	}, map[string]any{"bucket_keys": "host-a"})

	if got := out["title"]; got != "too many 5xx (host-a)" {
		t.Errorf("title = %v", got)
	}
	if got := out["starts_at"]; got != "2026-01-01T00:00:00Z" {
		t.Errorf("starts_at = %v", got)
	}
	if got := out["ends_at"]; got != "2026-01-01T00:05:00Z" {
		t.Errorf("ends_at = %v", got)
	}
	if got := out["generator_url"]; got != "https://os.example.com/app/alerting" {
		t.Errorf("generator_url = %v", got)
	}
	labels, _ := out["labels"].(map[string]any)
	for k, want := range map[string]string{
		"monitor": "5xx rate", "trigger": "too many 5xx", "hits": "42", "bucket_keys": "host-a",
	} {
		if labels[k] != want {
			t.Errorf("labels[%q] = %v, want %q", k, labels[k], want)
		}
	}
	// An unfilled field must not become an empty label: routing matches on
	// presence, and `hits: ""` is a match nobody wrote a rule for.
	bare, _ := normalizeOpenSearchAlert(map[string]any{}, nil)["labels"].(map[string]any)
	if len(bare) != 0 {
		t.Errorf("empty payload produced labels %v", bare)
	}
}

func TestNormalizeElasticsearchAlert(t *testing.T) {
	kibana := map[string]any{
		"status":   "active",
		"severity": "critical",
		"rule":     map[string]any{"id": "r1", "name": "disk full"},
		"alert":    map[string]any{"id": "a1", "actionGroup": "default"},
		"message":  "disk 95%",
		"url":      "https://kibana.example.com/app/o11y",
		"date":     "2026-01-01T00:00:00Z",
	}
	out := normalizeElasticsearchAlert(kibana)
	if got := out["title"]; got != "disk full" {
		t.Errorf("title = %v", got)
	}
	if got := out["status"]; got != "firing" {
		t.Errorf("status = %v, want firing", got)
	}
	if got := out["severity"]; got != "critical" {
		t.Errorf("severity = %v", got)
	}
	if got := out["dedupe_key"]; got != "r1:a1" {
		t.Errorf("dedupe_key = %v, want r1:a1", got)
	}
	if got := out["starts_at"]; got != "2026-01-01T00:00:00Z" {
		t.Errorf("starts_at = %v", got)
	}

	// Kibana calls its recovery action group `recovered`; the same alert instance
	// must keep the dedupe key that opened the group, or the recovery closes
	// nothing.
	recovered := normalizeElasticsearchAlert(map[string]any{
		"rule":  map[string]any{"id": "r1", "name": "disk full"},
		"alert": map[string]any{"id": "a1", "actionGroup": "recovered"},
	})
	if got := recovered["status"]; got != "resolved" {
		t.Errorf("recovered → %v, want resolved", got)
	}
	if got := recovered["dedupe_key"]; got != "r1:a1" {
		t.Errorf("recovered dedupe_key = %v, want r1:a1", got)
	}

	// Watcher names an alert by its watch; metadata is the only place a severity
	// can come from there.
	watcher := normalizeElasticsearchAlert(map[string]any{
		"watch_id":       "w1",
		"execution_time": "2026-01-01T00:00:00Z",
		"hits":           "7",
		"metadata":       map[string]any{"severity": "error", "team": "db"},
	})
	if got := watcher["title"]; got != "w1" {
		t.Errorf("watcher title = %v", got)
	}
	if got := watcher["dedupe_key"]; got != "w1" {
		t.Errorf("watcher dedupe_key = %v", got)
	}
	if got := watcher["severity"]; got != "error" {
		t.Errorf("watcher severity = %v", got)
	}
	if got := watcher["starts_at"]; got != "2026-01-01T00:00:00Z" {
		t.Errorf("watcher starts_at = %v", got)
	}
	labels, _ := watcher["labels"].(map[string]any)
	if labels["team"] != "db" || labels["watch"] != "w1" || labels["hits"] != "7" {
		t.Errorf("watcher labels = %v", labels)
	}

	// Neither product has a severity of its own. The default is `warning` rather
	// than `unknown`, which ranks below every known severity and would sink these
	// alerts to the bottom of the on-call queue.
	bare := normalizeElasticsearchAlert(map[string]any{"rule": map[string]any{"name": "x"}})
	if got := bare["severity"]; got != "warning" {
		t.Errorf("default severity = %v, want warning", got)
	}
}
