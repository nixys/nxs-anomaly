package engine

import (
	"context"
	"errors"
	"testing"
)

// A listing that is not ordered by anything a person can name is not a listing:
// ids are random hex, so "ORDER BY id" put the group that just fired wherever
// its random id happened to fall. These tests pin the order the API answers in.

func sortStore() *memStore {
	ms := newMemStore()
	ms.seed("alert_groups",
		map[string]any{"id": "grp_ccc", "title": "middle", "last_received_at": "2026-08-02T00:00:00+00:00"},
		map[string]any{"id": "grp_aaa", "title": "newest", "last_received_at": "2026-08-03T00:00:00+00:00"},
		map[string]any{"id": "grp_bbb", "title": "oldest", "last_received_at": "2026-08-01T00:00:00+00:00"},
	)
	return ms
}

func listedIDs(t *testing.T, page map[string]any) []string {
	t.Helper()
	items, ok := page["items"].([]map[string]any)
	if !ok {
		t.Fatalf("items has type %T, want []map[string]any", page["items"])
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item["id"].(string))
	}
	return ids
}

func TestAlertGroupsListDefaultsToNewestFirst(t *testing.T) {
	e := crudEngine(sortStore())

	page, err := e.ListCollectionPage(context.Background(), "alert_groups", map[string]any{})
	if err != nil {
		t.Fatalf("ListCollectionPage: %v", err)
	}
	got := listedIDs(t, page)
	want := []string{"grp_aaa", "grp_ccc", "grp_bbb"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default order = %v, want %v (newest last_received_at first)", got, want)
		}
	}
}

func TestAlertGroupsListHonoursRequestedOrder(t *testing.T) {
	e := crudEngine(sortStore())

	page, err := e.ListCollectionPage(context.Background(), "alert_groups",
		map[string]any{"sort": "last_received_at", "order": "asc"})
	if err != nil {
		t.Fatalf("ListCollectionPage: %v", err)
	}
	got := listedIDs(t, page)
	want := []string{"grp_bbb", "grp_ccc", "grp_aaa"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ascending order = %v, want %v", got, want)
		}
	}
}

// An unknown column is refused rather than ignored: a page that silently drops
// the sort looks exactly like a page that applied it.
func TestListRefusesUnknownSortField(t *testing.T) {
	e := crudEngine(sortStore())

	if _, err := e.ListCollectionPage(context.Background(), "alert_groups",
		map[string]any{"sort": "title; DROP TABLE"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if _, err := e.ListCollectionPage(context.Background(), "alert_groups",
		map[string]any{"order": "sideways"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want a validation error", err)
	}
}

// Sorting by severity means "most severe first", not "alphabetically": the
// spelling puts critical between alert and debug, which would bury the alerts
// somebody has to answer now in the middle of the page.
func TestSeveritySortIsRankedNotAlphabetical(t *testing.T) {
	ms := newMemStore()
	ms.seed("alert_groups",
		map[string]any{"id": "grp_1", "severity": "warning"},
		map[string]any{"id": "grp_2", "severity": "critical"},
		map[string]any{"id": "grp_3", "severity": "debug"},
	)
	e := crudEngine(ms)

	page, err := e.ListCollectionPage(context.Background(), "alert_groups",
		map[string]any{"sort": "severity", "order": "desc"})
	if err != nil {
		t.Fatalf("ListCollectionPage: %v", err)
	}
	got := listedIDs(t, page)
	want := []string{"grp_2", "grp_1", "grp_3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("severity order = %v, want %v (critical, warning, debug)", got, want)
		}
	}
}
