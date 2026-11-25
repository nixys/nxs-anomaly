package engine

import "context"

// Overview uses the same scoped list methods as the individual screens. A failed
// section fails the snapshot, so the UI never presents a partial result as fresh.
func (e *Engine) Overview(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	for _, section := range []struct {
		key, collection, status string
		limit                   int
	}{
		{"open", "alert_groups", "open", 10},
		{"acknowledged", "alert_groups", "acknowledged", 1},
		{"failed", "notifications", "failed", 1},
		{"retrying", "notifications", "retry_scheduled", 1},
		{"integrations", "integrations", "", 8},
	} {
		page, err := e.ListCollectionPage(ctx, section.collection, map[string]any{"status": section.status, "limit": section.limit})
		if err != nil {
			return nil, err
		}
		out[section.key] = page
	}
	onCall, err := e.CurrentlyOnCall(ctx)
	if err != nil {
		return nil, err
	}
	out["on_call"] = onCall
	return out, nil
}
