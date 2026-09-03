package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/storetest"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

func pipelineEngine(t *testing.T, pipelineJSON string, extra map[string]any) (*Engine, string) {
	t.Helper()
	eng := New(storetest.New())
	var pipe any
	if pipelineJSON != "" {
		if err := json.Unmarshal([]byte(pipelineJSON), &pipe); err != nil {
			t.Fatalf("bad test json: %v", err)
		}
	}
	spec := map[string]any{
		"name":     "pipe",
		"pipeline": pipe,
		"routes": []any{map[string]any{
			"name": "all", "match_type": "all", "is_default": true,
		}},
	}
	for k, v := range extra {
		spec[k] = v
	}
	integ, err := eng.CreateIntegration(context.Background(), spec)
	if err != nil {
		t.Fatalf("create integration: %v", err)
	}
	return eng, utils.StrVal(integ, "key")
}

// The point of running before routing and grouping: cutting a build id out of
// the title makes repeated failures collapse into one incident instead of
// opening a new group every night.
func TestPipelineChangesGrouping(t *testing.T) {
	eng, key := pipelineEngine(t,
		`[{"gsub":{"field":"title","pattern":"\\s+run-[0-9a-f]+$","replace":""}}]`, nil)
	ctx := context.Background()

	first, err := eng.IngestAlert(ctx, key, map[string]any{"title": "Nightly failed run-1a2b3c"})
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	second, err := eng.IngestAlert(ctx, key, map[string]any{"title": "Nightly failed run-99ff00"})
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	g1 := utils.StrVal(first["group"].(map[string]any), "id")
	g2 := utils.StrVal(second["group"].(map[string]any), "id")
	if g1 != g2 {
		t.Fatalf("two runs of the same job opened %s and %s; the pipeline did not reach the dedupe key", g1, g2)
	}
	if got := utils.StrVal(first["group"].(map[string]any), "title"); got != "Nightly failed" {
		t.Errorf("stored title = %q", got)
	}
}

// Routing is decided from what the source sent, and the pipeline must not move
// it. Otherwise every enrichment becomes a routing change nobody asked for:
// adding a team label for a dashboard would silently repoint the escalation.
func TestPipelineDoesNotChangeRouting(t *testing.T) {
	routes := []any{
		map[string]any{"name": "payments", "match_type": "labels", "labels": map[string]any{"team": "payments"}},
		map[string]any{"name": "default", "match_type": "all", "is_default": true},
	}
	eng, key := pipelineEngine(t,
		`[{"extract":{"from":"title","pattern":"^\\[(?P<team>[a-z]+)\\]"}}]`,
		map[string]any{"routes": routes})

	res, err := eng.IngestAlert(context.Background(), key, map[string]any{"title": "[payments] checkout down"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	g := res["group"].(map[string]any)

	// The label the pipeline produced is on the group...
	labels, _ := utils.CoerceLabelMap(g["labels"])
	if labels["team"] != "payments" {
		t.Fatalf("the extracted label did not reach the group: %v", labels)
	}
	// ...and the route is still the one the original alert matched, because it
	// carried no team label when the decision was made.
	integ := integrationByKey(t, eng, key)
	name := routeNameByID(integ, utils.StrVal(g, "route_id"))
	if name != "default" {
		t.Fatalf("route = %q, want default: enrichment moved the routing decision", name)
	}
}

// The same alert, when the source itself states the label, does take the
// specific route — so the previous test is about ordering, not about the route
// being unreachable.
func TestRoutingStillFollowsLabelsTheSourceSent(t *testing.T) {
	routes := []any{
		map[string]any{"name": "payments", "match_type": "labels", "labels": map[string]any{"team": "payments"}},
		map[string]any{"name": "default", "match_type": "all", "is_default": true},
	}
	eng, key := pipelineEngine(t, `[{"set":{"enriched":"yes"}}]`, map[string]any{"routes": routes})

	res, err := eng.IngestAlert(context.Background(), key, map[string]any{
		"title": "checkout down", "labels": map[string]any{"team": "payments"},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	g := res["group"].(map[string]any)
	integ := integrationByKey(t, eng, key)
	if name := routeNameByID(integ, utils.StrVal(g, "route_id")); name != "payments" {
		t.Fatalf("route = %q, want payments", name)
	}
}

func integrationByKey(t *testing.T, eng *Engine, key string) map[string]any {
	t.Helper()
	integ, err := eng.FindIntegrationByKeyForTest(context.Background(), key)
	if err != nil || integ == nil {
		t.Fatalf("integration %s not found: %v", key, err)
	}
	return integ
}

func routeNameByID(integ map[string]any, id string) string {
	routes, _ := integ["routes"].([]any)
	for _, r := range routes {
		m, ok := r.(map[string]any)
		if ok && utils.StrVal(m, "id") == id {
			return utils.StrVal(m, "name")
		}
	}
	return ""
}

func TestPipelineDropIsReportedNotHidden(t *testing.T) {
	eng, key := pipelineEngine(t,
		`[{"if":{"field":"label:severity","equals":"info"},"drop":true}]`, nil)
	ctx := context.Background()

	res, err := eng.IngestAlert(ctx, key, map[string]any{
		"title": "chatty", "labels": map[string]any{"severity": "info"},
	})
	if err != nil {
		t.Fatalf("a dropped alert must not be an error: %v", err)
	}
	if utils.StrVal(res, "result") != "dropped_by_pipeline" {
		t.Fatalf("result = %v; a drop has to be visible, not look like a vanished alert", res["result"])
	}
	if res["group"] != nil {
		t.Error("a dropped alert opened a group")
	}
}

// A whole Alertmanager envelope of dropped alerts must still answer, and must
// not open a transaction for nothing.
func TestPipelineDropsWholeEnvelope(t *testing.T) {
	eng, key := pipelineEngine(t,
		`[{"if":{"field":"label:severity","equals":"info"},"drop":true}]`, nil)
	res, err := eng.IngestAlertmanager(context.Background(), key, map[string]any{
		"alerts": []any{
			map[string]any{"status": "firing", "labels": map[string]any{"alertname": "a", "severity": "info"}},
			map[string]any{"status": "firing", "labels": map[string]any{"alertname": "b", "severity": "info"}},
		},
	})
	if err != nil {
		t.Fatalf("envelope of drops must not error: %v", err)
	}
	items, _ := res["results"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(items), res)
	}
	for _, it := range items {
		if utils.StrVal(it.(map[string]any), "result") != "dropped_by_pipeline" {
			t.Errorf("result = %v", it)
		}
	}
}

// A bad pipeline is a 400 when the integration is saved, not a surprise at 3am.
func TestBadPipelineIsRefusedAtSaveTime(t *testing.T) {
	eng := New(storetest.New())
	var pipe any
	_ = json.Unmarshal([]byte(`[{"gsub":{"field":"title","pattern":"([","replace":""}}]`), &pipe)
	_, err := eng.CreateIntegration(context.Background(), map[string]any{
		"name": "bad", "pipeline": pipe,
		"routes": []any{map[string]any{"name": "all", "match_type": "all", "is_default": true}},
	})
	if err == nil {
		t.Fatal("an integration with an uncompilable pipeline was accepted")
	}
}

// An integration without a pipeline must behave exactly as before.
func TestNoPipelineIsUnchanged(t *testing.T) {
	eng, key := pipelineEngine(t, "", nil)
	res, err := eng.IngestAlert(context.Background(), key, map[string]any{"title": "plain"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if utils.StrVal(res, "result") != "ingested" {
		t.Fatalf("result = %v", res["result"])
	}
}

// A rule that can only be tested by sending a real alert gets edited blind, and
// this pipeline decides routing and grouping.
func TestDebugRouteShowsWhatThePipelineChanged(t *testing.T) {
	eng, key := pipelineEngine(t, `[
		{"extract":{"from":"title","pattern":"in (?P<namespace>\\S+)"}},
		{"rename":{"ns":"zone"}},
		{"gsub":{"field":"title","pattern":"\\s+run-\\w+$","replace":""}}
	]`, nil)

	res, err := eng.DebugRoute(context.Background(), key, map[string]any{
		"title":  "CrashLoop in payments run-abc",
		"labels": map[string]any{"ns": "eu"},
	})
	if err != nil {
		t.Fatalf("debug: %v", err)
	}
	prev, ok := res["pipeline"].(map[string]any)
	if !ok {
		t.Fatalf("no pipeline preview in the answer: %v", res)
	}
	added, _ := prev["labels_added"].(map[string]any)
	if added["namespace"] != "payments" {
		t.Errorf("extraction not reported as added: %v", added)
	}
	if added["zone"] != "eu" {
		t.Errorf("rename not reported: %v", added)
	}
	removed, _ := prev["labels_removed"].([]string)
	if len(removed) != 1 || removed[0] != "ns" {
		t.Errorf("removed = %v", removed)
	}
	if prev["title_before"] != "CrashLoop in payments run-abc" || prev["title_after"] != "CrashLoop in payments" {
		t.Errorf("title before/after = %v / %v", prev["title_before"], prev["title_after"])
	}
}

func TestDebugRouteReportsADrop(t *testing.T) {
	eng, key := pipelineEngine(t, `[{"if":{"field":"label:severity","equals":"info"},"drop":true}]`, nil)
	res, err := eng.DebugRoute(context.Background(), key, map[string]any{
		"title": "noise", "labels": map[string]any{"severity": "info"},
	})
	if err != nil {
		t.Fatalf("debug: %v", err)
	}
	if utils.StrVal(res, "result") != "dropped_by_pipeline" {
		t.Fatalf("a drop is invisible in the preview: %v", res)
	}
}
