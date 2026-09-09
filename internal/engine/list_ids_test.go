package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// A page that shows one fact about each of fifty rows should ask once, not
// fifty times — and must never fall back to listing everything.
func TestListCollectionPageByIDs(t *testing.T) {
	st := storetest.New()
	st.Seed("alert_groups",
		map[string]any{"id": "grp_1", "title": "one", "status": "open"},
		map[string]any{"id": "grp_2", "title": "two", "status": "open"},
		map[string]any{"id": "grp_3", "title": "three", "status": "open"},
	)
	eng := New(st)

	page, err := eng.ListCollectionPage(context.Background(), "alert_groups", map[string]any{
		"ids": "grp_1, grp_3",
	})
	if err != nil {
		t.Fatalf("list by ids: %v", err)
	}
	items, _ := page["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("got %d items, want the two that were named", len(items))
	}
	for _, item := range items {
		if item["id"] != "grp_1" && item["id"] != "grp_3" {
			t.Errorf("unexpected item %v", item["id"])
		}
	}
}

// The dangerous case: an explicit but empty set must return nothing rather than
// quietly becoming an unfiltered listing.
func TestListCollectionPageByEmptyIDsReturnsNothing(t *testing.T) {
	st := storetest.New()
	st.Seed("alert_groups", map[string]any{"id": "grp_1", "status": "open"})
	eng := New(st)

	page, err := eng.ListCollectionPage(context.Background(), "alert_groups", map[string]any{
		"ids": " , ",
	})
	if err != nil {
		t.Fatalf("list by ids: %v", err)
	}
	items, _ := page["items"].([]map[string]any)
	if len(items) != 0 {
		t.Fatalf("got %d items, want none", len(items))
	}
}
