package utils

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvInt reads an integer setting from the environment.
//
// Unset (or empty) means def. Anything else is taken as given when it parses
// and is at least min — including 0 where min allows it, because for some
// settings 0 is the documented way to turn something off
// (NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS=0 sends no statement_timeout, which
// PgBouncer requires). The previous helpers accepted only positive values, so
// an explicit 0 silently became the default and the opt-out could not be
// expressed at all.
//
// A value that does not parse, or is below min, still falls back to def — a
// deployment that has run with a typo must not stop starting on upgrade — but
// it is logged: a timeout quietly replaced by its default is a misconfiguration
// nobody finds.
//
// min is per setting, not a global floor: for several of them 0 is not "off"
// but a broken value (a pool of zero connections, a session that expires on
// creation, a pgx health-check ticker that panics).
func EnvInt(key string, def, min int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	switch {
	case err != nil:
		slog.Warn("invalid_setting", "key", key, "value", v, "reason", "not an integer", "using", def)
		return def
	case n < min:
		slog.Warn("invalid_setting", "key", key, "value", v, "reason", "below minimum", "min", min, "using", def)
		return def
	}
	return n
}

// EnvSeconds is EnvInt for a whole number of seconds; see EnvInt for how
// unset, 0, invalid and below-min values are treated.
func EnvSeconds(key string, def time.Duration, min int) time.Duration {
	return time.Duration(EnvInt(key, int(def/time.Second), min)) * time.Second
}
