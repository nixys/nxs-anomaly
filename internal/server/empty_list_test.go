package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// A list endpoint that matched nothing must answer with an empty array, never
// with null.
//
// This is not style. `scanRows` returns a nil slice when a query matches no
// rows, and encoding/json turns a nil slice into `null`. On a fresh
// installation every collection is empty, so every list page received
// `{"items": null, …}`, and the web UI — which does `data.items.length` in the
// shared QueryState component — threw a TypeError that React escalated into
// unmounting the whole application. A brand-new install showed a blank page:
// no navigation, no "Add team" button, nothing. The e2e setup flow caught it.
//
// The gate walks the documented paths instead of a hand-written list, so an
// endpoint added later is covered without anyone remembering this file.
func TestEmptyListEndpointsReturnArraysNotNull(t *testing.T) {
	doc := loadOpenAPI(t)
	srv, _ := newTestServer() // an empty in-memory store: every collection is empty

	var paths []string
	for path, ops := range doc.Paths {
		if _, ok := ops["get"]; !ok {
			continue
		}
		// Only collection endpoints: a path parameter would need a real object.
		if strings.Contains(path, "{") {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)

	checked := 0
	for _, path := range paths {
		w := srv.do(http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			continue // not readable without setup (403/404/501) — not this test's subject
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			continue // not a JSON object envelope
		}
		raw, ok := body["items"]
		if !ok {
			continue // not a list endpoint
		}
		checked++
		if string(raw) == "null" {
			t.Errorf("GET %s returned \"items\": null on an empty installation; "+
				"it must be [] — null crashes every consumer that iterates the list", path)
		}
	}

	// Without this the test would pass silently if the harness stopped serving
	// list endpoints at all, which is exactly how a gate rots.
	if checked < 5 {
		t.Fatalf("only %d list endpoints were exercised; the gate is not testing what it claims", checked)
	}
}
