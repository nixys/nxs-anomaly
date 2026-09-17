package utils

import (
	"strings"
	"testing"
	"time"
)

func TestMakeID(t *testing.T) {
	id := MakeID("usr")
	if !strings.HasPrefix(id, "usr_") {
		t.Fatalf("id %q lacks prefix", id)
	}
	if len(id) != len("usr_")+12 {
		t.Fatalf("id %q has unexpected length", id)
	}
	if MakeID("usr") == id {
		t.Fatal("two MakeID calls returned the same id")
	}
}

func TestToISOAndParseDatetimeRoundtrip(t *testing.T) {
	ts := time.Date(2026, 5, 10, 10, 0, 0, 500_000_000, time.UTC)
	iso := ToISO(ts)
	if iso != "2026-05-10T10:00:00+00:00" {
		t.Fatalf("ToISO = %q", iso)
	}
	parsed, err := ParseDatetime(iso)
	if err != nil {
		t.Fatalf("ParseDatetime(%q) failed: %v", iso, err)
	}
	if !parsed.Equal(ts.Truncate(time.Second)) {
		t.Fatalf("roundtrip mismatch: %v vs %v", parsed, ts)
	}
}

func TestParseDatetimeFormats(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-05-10T10:00:00Z", time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)},
		{"2026-05-10T10:00:00", time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)},
		{"2026-05-10T12:00:00+02:00", time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)},
		{"2026-05-10", time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)},
		{" 2026-05-10T10:00:00Z ", time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := ParseDatetime(c.in)
		if err != nil {
			t.Fatalf("ParseDatetime(%q) failed: %v", c.in, err)
		}
		if !got.Equal(c.want) {
			t.Fatalf("ParseDatetime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := ParseDatetime("not-a-date"); err == nil {
		t.Fatal("expected error for invalid datetime")
	}
}

func TestStrVal(t *testing.T) {
	m := map[string]any{"s": "x", "n": 5, "nil": nil}
	if StrVal(m, "s") != "x" || StrVal(m, "n") != "5" {
		t.Fatal("StrVal conversion mismatch")
	}
	if StrVal(m, "nil") != "" || StrVal(m, "missing") != "" || StrVal(nil, "s") != "" {
		t.Fatal("StrVal should return empty string for nil/missing")
	}
}

func TestIntVal(t *testing.T) {
	m := map[string]any{"i": 3, "i64": int64(4), "f": 5.9, "s": "x", "nil": nil}
	if IntVal(m, "i") != 3 || IntVal(m, "i64") != 4 || IntVal(m, "f") != 5 {
		t.Fatal("IntVal numeric conversion mismatch")
	}
	if IntVal(m, "s") != 0 || IntVal(m, "nil") != 0 || IntVal(m, "missing") != 0 || IntVal(nil, "i") != 0 {
		t.Fatal("IntVal should return 0 for non-numeric/nil/missing")
	}
}

func TestBoolVal(t *testing.T) {
	m := map[string]any{"t": true, "f": false, "s": "true", "nil": nil}
	if !BoolVal(m, "t", false) || BoolVal(m, "f", true) {
		t.Fatal("BoolVal bool passthrough mismatch")
	}
	if !BoolVal(m, "s", true) || !BoolVal(m, "nil", true) || !BoolVal(m, "missing", true) || !BoolVal(nil, "t", true) {
		t.Fatal("BoolVal should return default for non-bool/nil/missing")
	}
}

func TestCoerceStringList(t *testing.T) {
	got, err := CoerceStringList([]any{"a", 1, true})
	if err != nil {
		t.Fatalf("CoerceStringList failed: %v", err)
	}
	if len(got) != 3 || got[0] != "a" || got[1] != "1" || got[2] != "true" {
		t.Fatalf("CoerceStringList = %#v", got)
	}
	if got, err := CoerceStringList(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil input: got %#v, err %v", got, err)
	}
	if _, err := CoerceStringList("not-a-list"); err == nil {
		t.Fatal("expected error for non-list input")
	}
}

func TestCoerceLabelMap(t *testing.T) {
	got, err := CoerceLabelMap(map[string]any{"a": "x", "b": 2})
	if err != nil {
		t.Fatalf("CoerceLabelMap failed: %v", err)
	}
	if got["a"] != "x" || got["b"] != "2" {
		t.Fatalf("CoerceLabelMap = %#v", got)
	}
	// JSON numbers arrive as float64; %v printed large ones as 1e+06.
	nums, _ := CoerceLabelMap(map[string]any{"big": float64(1000000), "frac": 0.25, "none": nil})
	if nums["big"] != "1000000" || nums["frac"] != "0.25" || nums["none"] != "" {
		t.Fatalf("numeric labels = %#v, want 1000000, 0.25 and empty", nums)
	}
	if got := StrVal(map[string]any{"hits": float64(2500000)}, "hits"); got != "2500000" {
		t.Fatalf("StrVal(2500000) = %q", got)
	}
	if got, err := CoerceLabelMap(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil input: got %#v, err %v", got, err)
	}
	if _, err := CoerceLabelMap([]any{"x"}); err == nil {
		t.Fatal("expected error for non-map input")
	}
}

func TestEnsureRequired(t *testing.T) {
	data := map[string]any{"a": "x", "b": "", "c": nil}
	if err := EnsureRequired(data, []string{"a"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := EnsureRequired(data, []string{"a", "b", "c", "d"})
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
	for _, k := range []string{"b", "c", "d"} {
		if !strings.Contains(err.Error(), k) {
			t.Fatalf("error %q does not mention %s", err.Error(), k)
		}
	}
}

func TestCloneMap(t *testing.T) {
	src := map[string]any{"a": 1}
	clone := CloneMap(src)
	clone["a"] = 2
	if src["a"] != 1 {
		t.Fatal("CloneMap did not copy")
	}
	if got := CloneMap(nil); got == nil || len(got) != 0 {
		t.Fatalf("CloneMap(nil) = %#v, want empty map", got)
	}
}

func TestPickFirst(t *testing.T) {
	if got := PickFirst([]*string{nil, StringPtr(""), StringPtr("x")}, "def"); got != "x" {
		t.Fatalf("PickFirst = %q", got)
	}
	if got := PickFirst([]*string{nil}, "def"); got != "def" {
		t.Fatalf("PickFirst default = %q", got)
	}
}

func TestJSONDumps(t *testing.T) {
	if got := JSONDumps(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Fatalf("JSONDumps = %q", got)
	}
}
