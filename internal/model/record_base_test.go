package model

import (
	"testing"

	"github.com/nixys/nxs-anomaly/internal/store"
)

// TestEveryEntityTableHasRecordWrapper guards the invariant the whole record
// architecture depends on: the load/save path has no untyped fallback, so every
// collection in store.EntityTables must have a wrapper registered from a model
// init(). A missing registration would panic at runtime (recordsOf) or error on
// load/save — this test fails at build time instead.
func TestEveryEntityTableHasRecordWrapper(t *testing.T) {
	for col := range store.EntityTables {
		if !store.RecordWrapperRegistered(col) {
			t.Errorf("no record wrapper registered for collection %q", col)
		}
	}
}
