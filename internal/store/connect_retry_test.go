package store

import (
	"errors"
	"testing"
)

func TestIsRetryableConnectError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"dial tcp 10.0.0.5:5432: connect: connection refused", true},
		{"failed to connect: dial error", true},
		{"lookup db.internal: no such host", true},
		{"the database system is starting up", true},
		{"read tcp: connect: connection reset", true},
		// The reset a PostgreSQL that is listening but not yet serving sends
		// during the startup handshake. It reads differently from the dial-time
		// reset above and used not to match, so a load-test profile died at
		// store init with no report at all rather than waiting a second.
		{"failed to receive message: read tcp 127.0.0.1:47994->127.0.0.1:55460: read: connection reset by peer", true},
		{`ERROR: syntax error at or near "SELCT"`, false},
		{"migration 0099: relation already exists", false},
	}
	for _, c := range cases {
		if got := isRetryableConnectError(errors.New(c.msg)); got != c.want {
			t.Errorf("isRetryableConnectError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}
