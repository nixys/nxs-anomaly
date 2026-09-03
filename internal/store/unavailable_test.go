package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Both observed while restarting PostgreSQL under live ingest.
		{"dial refused", errors.New("failed to connect to `user=x database=y`: dial error: dial tcp 10.0.0.1:5432: connect: connection refused"), true},
		{"admin shutdown", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, true},

		{"crash shutdown", &pgconn.PgError{Code: "57P02"}, true},
		{"cannot connect now", &pgconn.PgError{Code: "57P03"}, true},
		{"connection exception", &pgconn.PgError{Code: "08006"}, true},
		{"reset mid-handshake", errors.New("failed to receive message: read tcp: read: connection reset by peer"), true},
		{"wrapped", fmt.Errorf("ingest: %w", &pgconn.PgError{Code: "57P01"}), true},

		// The caller's problem: retrying produces the same answer.
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"syntax error", &pgconn.PgError{Code: "42601"}, false},
		{"permission denied", &pgconn.PgError{Code: "42501"}, false},
		{"plain error", errors.New("no such alert group"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsUnavailable(c.err); got != c.want {
			t.Errorf("%s: IsUnavailable = %v, want %v", c.name, got, c.want)
		}
	}
}
