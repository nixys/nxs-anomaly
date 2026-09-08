package store

import (
	"strings"
	"testing"
)

// The bug this pins: an alert stored as "high" ranked 0 — below "debug" — so a
// list sorted by severity put the loudest incident at the bottom, silently.
func TestSeverityRankKnowsTheSpellingsSourcesActuallySend(t *testing.T) {
	cases := []struct{ higher, lower string }{
		{"high", "debug"},
		{"high", "warning"},
		{"critical", "high"},
		{"p1", "p2"},
		{"medium", "low"},
		{"disaster", "average"},
		{"debug", "wobbly"}, // an unmodelled word still ranks below everything modelled
	}
	for _, c := range cases {
		if SeverityRank(c.higher) <= SeverityRank(c.lower) {
			t.Errorf("SeverityRank(%q)=%d must outrank %q=%d",
				c.higher, SeverityRank(c.higher), c.lower, SeverityRank(c.lower))
		}
	}
}

// Every alias of one level ranks identically — otherwise two words that mean
// the same thing sort into two different places in the same page.
func TestSeverityAliasesOfOneLevelRankTogether(t *testing.T) {
	for level, aliases := range severityAliases {
		want := SeverityRank(level)
		for _, alias := range aliases {
			if got := SeverityRank(alias); got != want {
				t.Errorf("%q ranks %d, but its level %q ranks %d", alias, got, level, want)
			}
		}
	}
}

// The SQL is generated from the same table, so a spelling added to the map must
// appear in the ORDER BY without anybody remembering to edit a second place.
func TestSeverityRankSQLCoversEveryAlias(t *testing.T) {
	sql := severityRankSQL()
	for _, aliases := range severityAliases {
		for _, alias := range aliases {
			if !strings.Contains(sql, "'"+alias+"'") {
				t.Errorf("severityRankSQL() does not mention %q: %s", alias, sql)
			}
		}
	}
}

// SeverityLevels is what the UI offers in its filter. A level with no aliases
// would be an option that can never match anything.
func TestEverySeverityLevelHasAFamily(t *testing.T) {
	for _, level := range SeverityLevels {
		family := SeverityFamily(level)
		if len(family) == 0 || family[0] != level {
			t.Errorf("SeverityFamily(%q) = %v, want the level itself first", level, family)
		}
	}
}
