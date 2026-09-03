package main

import (
	"bytes"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// The CLI is what an operator reaches for when the UI is the thing that is
// broken, so its output is a debugging surface in its own right. These tests
// cover dispatch, the table renderer and the small value helpers — everything
// that does not need a database.

func TestRunWithoutArgsPrintsUsage(t *testing.T) {
	var stderr bytes.Buffer
	code := run(nil, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	out := stderr.String()
	if !strings.Contains(out, "usage: nxs-anomaly <command>") {
		t.Errorf("usage line missing from %q", out)
	}
	// Every dispatchable command must be discoverable from the usage line — the
	// two used to be maintained separately and drifted.
	for name := range commands {
		if !strings.Contains(out, name) {
			t.Errorf("command %q is dispatchable but absent from usage output", name)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"definitely-not-a-command", "--limit", "5"}, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command: definitely-not-a-command") {
		t.Errorf("stderr = %q, want it to name the unknown command", got)
	}
}

// The usage list and the dispatch table are two views of the same set. A command
// in one and not the other is either undiscoverable or advertised and missing.
func TestCommandOrderMatchesDispatchTable(t *testing.T) {
	if len(commandOrder) != len(commands) {
		t.Fatalf("commandOrder has %d entries, commands has %d", len(commandOrder), len(commands))
	}
	seen := map[string]bool{}
	for _, name := range commandOrder {
		if _, ok := commands[name]; !ok {
			t.Errorf("commandOrder lists %q, which has no handler", name)
		}
		if seen[name] {
			t.Errorf("commandOrder lists %q twice", name)
		}
		seen[name] = true
	}
	for name := range commands {
		if !seen[name] {
			t.Errorf("command %q has a handler but is not in commandOrder", name)
		}
	}
}

func TestSetupLogging(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		format    string
		wantDebug bool
		wantJSON  bool
	}{
		{name: "default is info, text", wantDebug: false, wantJSON: false},
		{name: "debug level", level: "debug", wantDebug: true},
		{name: "level is case-insensitive", level: "DEBUG", wantDebug: true},
		{name: "warning is an alias for warn", level: "warning", wantDebug: false},
		{name: "unrecognised level falls back to info", level: "loud", wantDebug: false},
		{name: "json format", format: "json", wantJSON: true},
		{name: "format is case-insensitive", format: "JSON", wantJSON: true},
	}

	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tc.level)
			t.Setenv("LOG_FORMAT", tc.format)

			// setupLogging binds the handler to whatever os.Stderr is at the moment
			// it runs, so the redirect has to be in place before it is called —
			// wrapping only the probe would leave the handler pointed at the real
			// stderr and capture nothing.
			line := captureStderr(t, func() {
				setupLogging()
				slog.Error("probe", "key", "value")
			})

			if got := slog.Default().Enabled(t.Context(), slog.LevelDebug); got != tc.wantDebug {
				t.Errorf("debug enabled = %v, want %v", got, tc.wantDebug)
			}
			isJSON := strings.HasPrefix(strings.TrimSpace(line), "{")
			if isJSON != tc.wantJSON {
				t.Errorf("json output = %v, want %v (line: %q)", isJSON, tc.wantJSON, line)
			}
		})
	}
}

func TestPrintTableEmpty(t *testing.T) {
	out := captureStdout(t, func() { printTable(nil, []string{"id", "title"}) })

	if strings.TrimSpace(out) != "(no items)" {
		t.Errorf("output = %q, want %q", out, "(no items)")
	}
}

func TestPrintTableAligns(t *testing.T) {
	rows := []map[string]string{
		{"id": "grp_1", "title": "short"},
		{"id": "grp_22", "title": "a much longer title"},
	}
	out := captureStdout(t, func() { printTable(rows, []string{"id", "title"}) })

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// separator, header, separator, 2 rows, separator
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d: %q", len(lines), out)
	}
	assertUniformWidth(t, lines)
	if !strings.Contains(lines[1], "id") || !strings.Contains(lines[1], "title") {
		t.Errorf("header line missing column names: %q", lines[1])
	}
	if !strings.Contains(lines[4], "a much longer title") {
		t.Errorf("longest cell was not printed in full: %q", lines[4])
	}
}

// Alert titles in this deployment are routinely Cyrillic. Widths computed in
// bytes make every such column two characters too wide and the table stops
// lining up, which is exactly when an operator stops trusting the output.
func TestPrintTableAlignsMultiByte(t *testing.T) {
	rows := []map[string]string{
		{"id": "grp_1", "title": "Сервер недоступен"},
		{"id": "grp_2", "title": "ok"},
	}
	out := captureStdout(t, func() { printTable(rows, []string{"id", "title"}) })

	assertUniformWidth(t, strings.Split(strings.TrimRight(out, "\n"), "\n"))
}

// assertUniformWidth checks every rendered line is the same number of runes wide.
func assertUniformWidth(t *testing.T, lines []string) {
	t.Helper()
	want := utf8.RuneCountInString(lines[0])
	for i, line := range lines {
		if got := utf8.RuneCountInString(line); got != want {
			t.Errorf("line %d is %d runes wide, want %d:\n%s", i, got, want, strings.Join(lines, "\n"))
		}
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "pads ascii", in: "ab", n: 4, want: "ab  "},
		{name: "exact width is untouched", in: "abcd", n: 4, want: "abcd"},
		{name: "longer than width is untouched", in: "abcdef", n: 4, want: "abcdef"},
		{name: "empty", in: "", n: 3, want: "   "},
		// Six runes, twelve bytes: a byte-counting implementation pads none.
		{name: "pads by rune, not byte", in: "Привет", n: 8, want: "Привет  "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := padRight(tc.in, tc.n); got != tc.want {
				t.Errorf("padRight(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "shorter than limit", in: "abc", n: 10, want: "abc"},
		{name: "exactly at limit", in: "abcd", n: 4, want: "abcd"},
		{name: "cuts ascii", in: "abcdef", n: 3, want: "abc"},
		{name: "zero", in: "abc", n: 0, want: ""},
		// The bug this replaced: cutting "Привет" at 3 bytes yields "П\xd1".
		{name: "cuts by rune, not byte", in: "Привет", n: 3, want: "При"},
		{name: "multi-byte under limit is untouched", in: "Привет", n: 6, want: "Привет"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncate(%q, %d) produced invalid UTF-8: %q", tc.in, tc.n, got)
			}
		})
	}
}

func TestStrVal(t *testing.T) {
	m := map[string]any{
		"str":   "value",
		"int":   42,
		"float": 1.5,
		"bool":  true,
		"nil":   nil,
		"map":   map[string]any{"a": 1},
	}
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{name: "string", m: m, key: "str", want: "value"},
		{name: "int is formatted", m: m, key: "int", want: "42"},
		{name: "float is formatted", m: m, key: "float", want: "1.5"},
		{name: "bool is formatted", m: m, key: "bool", want: "true"},
		{name: "explicit nil is empty", m: m, key: "nil", want: ""},
		{name: "missing key is empty", m: m, key: "absent", want: ""},
		{name: "nil map is empty", m: nil, key: "str", want: ""},
		{name: "nested map is formatted", m: m, key: "map", want: "map[a:1]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := strVal(tc.m, tc.key); got != tc.want {
				t.Errorf("strVal(%v, %q) = %q, want %q", tc.m, tc.key, got, tc.want)
			}
		})
	}
}

// nilIfEmpty feeds the history filters, where a nil means "no filter" and an
// empty string would mean "match the empty value" — a filter nothing matches.
func TestNilIfEmpty(t *testing.T) {
	if got := nilIfEmpty(""); got != nil {
		t.Errorf("nilIfEmpty(\"\") = %#v, want nil", got)
	}
	if got := nilIfEmpty("critical"); got != "critical" {
		t.Errorf("nilIfEmpty(%q) = %#v, want the string back", "critical", got)
	}
}

func TestPrintJSON(t *testing.T) {
	out := captureStdout(t, func() {
		printJSON(map[string]any{"alerts": []any{map[string]any{"id": "a1"}}})
	})

	want := "{\n  \"alerts\": [\n    {\n      \"id\": \"a1\"\n    }\n  ]\n}\n"
	if out != want {
		t.Errorf("printJSON output =\n%q\nwant\n%q", out, want)
	}
}

// parseFlags exits the process on a bad flag, so only the success path is
// exercised here; the failure path is a single os.Exit(2).
func TestParseFlags(t *testing.T) {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "")
	severity := fs.String("severity", "", "")

	parseFlags(fs, []string{"--limit", "5", "--severity", "critical"})

	if *limit != 5 {
		t.Errorf("limit = %d, want 5", *limit)
	}
	if *severity != "critical" {
		t.Errorf("severity = %q, want %q", *severity, "critical")
	}
}

// --- helpers ---

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stdout, fn)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stderr, fn)
}

func capture(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	original := *target
	*target = w
	defer func() { *target = original }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return out
}
