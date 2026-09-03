package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestIngestAnswers503WhenTheDatabaseIsGone: the status code decides whether the
// sender retries the alert or drops it. Alertmanager and friends back off on a
// 503 and treat a 500 as "this request is broken", so answering 500 to a
// database restart turns a blip into lost alerts — measured as 11 of 60 alerts
// during a PostgreSQL restart under live ingest.
func TestIngestAnswers503WhenTheDatabaseIsGone(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"dial refused", errors.New("failed to connect: dial error: dial tcp 10.0.0.1:5432: connect: connection refused")},
		{"admin shutdown", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeIngestError(w, c.err, "webhook", "key_x")
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", w.Code)
			}
			if w.Header().Get("Retry-After") == "" {
				t.Error("no Retry-After: a sender told to come back needs to know when")
			}
		})
	}
}

// A fault in the request itself stays a 500: retrying it would only produce the
// same answer, and a 503 would invite the sender to hammer.
func TestIngestKeeps500ForRealFaults(t *testing.T) {
	w := httptest.NewRecorder()
	writeIngestError(w, &pgconn.PgError{Code: "23505", Message: "duplicate key"}, "webhook", "key_x")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if w.Header().Get("Retry-After") != "" {
		t.Error("Retry-After on a permanent fault invites a retry loop")
	}
}
