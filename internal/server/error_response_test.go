package server

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

// TestWriteEngineErrorGeneric500 verifies an internal engine error is not echoed
// back to the client: the 500 body is a generic message (the detail is logged).
func TestWriteEngineErrorGeneric500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeEngineError(rec, errors.New("pq: connection to 10.0.0.5:5432 failed: secret detail"))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "internal error" {
		t.Errorf("body error = %q, want generic 'internal error' (no leak)", body["error"])
	}
}

// TestWriteIngestErrorGeneric500 verifies the same for the ingest error path.
func TestWriteIngestErrorGeneric500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeIngestError(rec, errors.New("internal sql detail"), "alertmanager", "key-1")
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "internal error" {
		t.Errorf("body error = %q, want generic 'internal error'", body["error"])
	}
}
