package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/storetest"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func mustPipeline(t *testing.T, jsonSrc string) *AlertPipeline {
	t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(jsonSrc), &raw); err != nil {
		t.Fatalf("bad test json: %v", err)
	}
	p, err := CompileAlertPipeline(raw)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func doc(title string, labels map[string]string) *pipelineDoc {
	if labels == nil {
		labels = map[string]string{}
	}
	return &pipelineDoc{title: title, labels: labels}
}

// The grok analogue: an alert whose source states nothing but a sentence.
func TestExtractPullsLabelsOutOfText(t *testing.T) {
	p := mustPipeline(t, `[{"extract":{"from":"title","pattern":"pod (?P<pod>\\S+) in (?P<namespace>\\S+)"}}]`)
	d := doc("CrashLoop: pod checkout-7d9f in payments", nil)
	p.Apply(d, "int_1")
	if d.labels["pod"] != "checkout-7d9f" || d.labels["namespace"] != "payments" {
		t.Fatalf("labels = %v", d.labels)
	}
}

// Extraction adds context; it does not overwrite what the source stated.
func TestExtractDoesNotOverwriteExistingLabels(t *testing.T) {
	p := mustPipeline(t, `[{"extract":{"from":"title","pattern":"ns (?P<namespace>\\S+)"}}]`)
	d := doc("ns guessed", map[string]string{"namespace": "authoritative"})
	p.Apply(d, "int_1")
	if d.labels["namespace"] != "authoritative" {
		t.Fatalf("extraction overwrote the source's own label: %v", d.labels)
	}
}

// Cutting a build id so repeated failures collapse into one incident. This is a
// dedupe-key change, which is why the pipeline has to run before grouping.
func TestGsubCutsNoiseFromTheTitle(t *testing.T) {
	p := mustPipeline(t, `[{"gsub":{"field":"title","pattern":"\\s+run-[0-9a-f]{8}$","replace":""}}]`)
	d := doc("Nightly job failed run-1a2b3c4d", nil)
	p.Apply(d, "int_1")
	if d.title != "Nightly job failed" {
		t.Fatalf("title = %q", d.title)
	}
}

func TestSetRenameRemove(t *testing.T) {
	p := mustPipeline(t, `[
		{"set":{"team":"payments"}},
		{"rename":{"ns":"namespace"}},
		{"remove":["run_id"]}
	]`)
	d := doc("t", map[string]string{"ns": "prod", "run_id": "x"})
	p.Apply(d, "int_1")
	if d.labels["team"] != "payments" {
		t.Errorf("set did not apply: %v", d.labels)
	}
	if d.labels["namespace"] != "prod" || d.labels["ns"] != "" {
		t.Errorf("rename did not apply: %v", d.labels)
	}
	if _, ok := d.labels["run_id"]; ok {
		t.Errorf("remove did not apply: %v", d.labels)
	}
}

func TestConditionGatesTheStage(t *testing.T) {
	src := `[{"if":{"field":"label:namespace","equals":"payments"},"set":{"team":"payments"}}]`
	p := mustPipeline(t, src)

	hit := doc("t", map[string]string{"namespace": "payments"})
	p.Apply(hit, "int_1")
	if hit.labels["team"] != "payments" {
		t.Errorf("condition should have matched: %v", hit.labels)
	}

	miss := doc("t", map[string]string{"namespace": "search"})
	p.Apply(miss, "int_1")
	if _, ok := miss.labels["team"]; ok {
		t.Errorf("condition should not have matched: %v", miss.labels)
	}
}

func TestDropDiscardsTheAlert(t *testing.T) {
	p := mustPipeline(t, `[{"if":{"field":"label:severity","equals":"info"},"drop":true}]`)
	d := doc("noise", map[string]string{"severity": "info"})
	if res := p.Apply(d, "int_1"); !res.Dropped {
		t.Fatal("alert was not dropped")
	}
	keep := doc("real", map[string]string{"severity": "critical"})
	if res := p.Apply(keep, "int_1"); res.Dropped {
		t.Fatal("a critical alert was dropped")
	}
}

// An unconditional drop silences an integration while it keeps answering 202.
func TestUnconditionalDropIsRefused(t *testing.T) {
	var raw any
	_ = json.Unmarshal([]byte(`[{"drop":true}]`), &raw)
	if _, err := CompileAlertPipeline(raw); err == nil {
		t.Fatal("an unconditional drop was accepted")
	}
}

// Stages run in order, and the order has to be visible in the config.
func TestTwoActionsInOneStageAreRefused(t *testing.T) {
	var raw any
	_ = json.Unmarshal([]byte(`[{"set":{"a":"1"},"remove":["b"]}]`), &raw)
	_, err := CompileAlertPipeline(raw)
	if err == nil || !strings.Contains(err.Error(), "separate stages") {
		t.Fatalf("err = %v", err)
	}
}

func TestOrderIsRespected(t *testing.T) {
	// Rename first, then match on the new name: only correct if order holds.
	p := mustPipeline(t, `[
		{"rename":{"ns":"namespace"}},
		{"if":{"field":"label:namespace","exists":true},"set":{"enriched":"yes"}}
	]`)
	d := doc("t", map[string]string{"ns": "prod"})
	p.Apply(d, "int_1")
	if d.labels["enriched"] != "yes" {
		t.Fatalf("stages did not run in order: %v", d.labels)
	}
}

func TestValidationRejectsBadRules(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"not a list", `{"set":{"a":"b"}}`},
		{"stage not an object", `["set"]`},
		{"no action", `[{"if":{"field":"title","equals":"x"}}]`},
		{"unknown field", `[{"gsub":{"field":"payload","pattern":"x","replace":""}}]`},
		{"extract without named groups", `[{"extract":{"from":"title","pattern":"(\\S+)"}}]`},
		{"bad regex", `[{"gsub":{"field":"title","pattern":"([","replace":""}}]`},
		{"truncate without max", `[{"truncate":{"field":"title"}}]`},
		{"empty set", `[{"set":{}}]`},
		{"condition without a test", `[{"if":{"field":"title"},"set":{"a":"b"}}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var raw any
			if err := json.Unmarshal([]byte(c.src), &raw); err != nil {
				t.Fatalf("bad test json: %v", err)
			}
			if _, err := CompileAlertPipeline(raw); err == nil {
				t.Error("accepted an invalid pipeline")
			}
		})
	}
}

// Fail-open: a stage that cannot run is skipped, and the alert survives. An
// alert delivered unenriched is a cosmetic problem; an alert not delivered is a
// missed incident.
func TestOversizedFieldSkipsTheStageAndKeepsTheAlert(t *testing.T) {
	p := mustPipeline(t, `[{"gsub":{"field":"title","pattern":"a","replace":"b"}}]`)
	long := strings.Repeat("a", maxFieldLen+1)
	d := doc(long, nil)
	res := p.Apply(d, "int_1")
	if res.Dropped {
		t.Fatal("an oversized field dropped the alert")
	}
	if d.title != long {
		t.Fatal("an oversized field was rewritten anyway")
	}
}

func TestTruncateCutsOnARuneBoundary(t *testing.T) {
	p := mustPipeline(t, `[{"truncate":{"field":"title","max":5}}]`)
	d := doc("привет", nil) // 2 bytes per rune
	p.Apply(d, "int_1")
	if !isValidUTF8(d.title) {
		t.Fatalf("truncated mid-rune: %q", d.title)
	}
	if len(d.title) > 5 {
		t.Fatalf("not truncated: %q", d.title)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestStageLimitIsEnforced(t *testing.T) {
	stages := make([]string, maxPipelineStages+1)
	for i := range stages {
		stages[i] = `{"set":{"a":"b"}}`
	}
	var raw any
	_ = json.Unmarshal([]byte("["+strings.Join(stages, ",")+"]"), &raw)
	if _, err := CompileAlertPipeline(raw); err == nil {
		t.Fatal("stage limit not enforced")
	}
}

// FindIntegrationByKeyForTest exposes the store lookup the ingest path uses, so
// a test can check which route an alert actually took.
func (e *Engine) FindIntegrationByKeyForTest(ctx context.Context, key string) (map[string]any, error) {
	return e.store.FindIntegrationByKey(ctx, key)
}

// TestPipelineSeverityLabelSetsGroupSeverity: a rule that rewrites the severity
// label means the severity. Before this the group kept the value the source
// sent while the label said something else, and the two disagreed everywhere
// they were shown together. The label: prefix is accepted because the rest of a
// pipeline addresses labels that way.
func TestPipelineSeverityLabelSetsGroupSeverity(t *testing.T) {
	for _, key := range []string{"severity", "label:severity"} {
		t.Run(key, func(t *testing.T) {
			eng := New(storetest.New())
			integration := map[string]any{
				"id": "int-1",
				"pipeline": []any{map[string]any{
					"if":  map[string]any{"field": "label:env", "equals": "prod"},
					"set": map[string]any{key: "critical"},
				}},
			}
			payload := map[string]any{
				"title":    "disk full",
				"severity": "warning",
				"labels":   map[string]any{"env": "prod"},
			}
			dropped, err := eng.applyAlertPipeline(integration, payload)
			if err != nil || dropped {
				t.Fatalf("dropped=%v err=%v", dropped, err)
			}
			if got := utils.StrVal(payload, "severity"); got != "critical" {
				t.Errorf("severity = %q, want critical", got)
			}
			labels, _ := utils.CoerceLabelMap(payload["labels"])
			if labels["severity"] != "critical" {
				t.Errorf("severity label = %q, want critical", labels["severity"])
			}
			if _, ok := labels["label:severity"]; ok {
				t.Error(`a label literally named "label:severity" was created`)
			}
		})
	}
}

// A rule that does not fire leaves the source's severity alone.
func TestPipelineLeavesSeverityWhenRuleDoesNotFire(t *testing.T) {
	eng := New(storetest.New())
	integration := map[string]any{
		"id": "int-1",
		"pipeline": []any{map[string]any{
			"if":  map[string]any{"field": "label:env", "equals": "prod"},
			"set": map[string]any{"severity": "critical"},
		}},
	}
	payload := map[string]any{"title": "disk full", "severity": "warning", "labels": map[string]any{"env": "dev"}}
	if _, err := eng.applyAlertPipeline(integration, payload); err != nil {
		t.Fatal(err)
	}
	if got := utils.StrVal(payload, "severity"); got != "warning" {
		t.Errorf("severity = %q, want warning", got)
	}
}
