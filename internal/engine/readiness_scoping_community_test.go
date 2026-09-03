package engine

import (
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/utils"
)

// TestReadinessScopingSaysWhereTheBoundaryWent is the community half of the
// team-boundary check. What it guards is not the severity but the advice: the
// check used to tell every reader to set NXS_ANOMALY_TEAM_SCOPING=true, and in
// this edition that flag is read by nothing. Advice that does nothing is worse
// than silence, because the reader spends a trip finding that out.
func TestReadinessScopingSaysWhereTheBoundaryWent(t *testing.T) {
	e, _ := readinessEngine(t)
	// Set the flag to show it changes nothing here: the edition decides, not
	// the environment.
	t.Setenv("NXS_ANOMALY_TEAM_SCOPING", "true")

	c := check(t, mustReadiness(t, e), "team_scoping")

	if got := utils.StrVal(c, "severity"); got != ReadinessOK {
		t.Errorf("severity = %q, want ok: there is nothing here for this reader to act on", got)
	}
	detail := utils.StrVal(c, "detail")
	if !strings.Contains(detail, "every operator") {
		t.Errorf("detail does not state the boundary in force: %q", detail)
	}
	if !strings.Contains(detail, "enterprise") {
		t.Errorf("detail does not say where the boundary went: %q", detail)
	}
	if strings.Contains(detail, "NXS_ANOMALY_TEAM_SCOPING") {
		t.Errorf("detail still advises a flag this build does not read: %q", detail)
	}
}
