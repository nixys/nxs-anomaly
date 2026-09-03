package model

import (
	"encoding/json"
	"fmt"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// mapBacked is the embedded base for collections whose State representation is
// still a raw map (everything except the four typed-fields collections —
// AlertGroup/Alert/Notification/NotificationBatch). It supplies Raw,
// RecordID and MarshalData; concrete types only contribute a TypedValues()
// matching their store.TypedColumns row.
//
// All mapBacked-derived wrappers share one byte-stability guarantee:
// MarshalData = json.Marshal(raw), so a row that wasn't touched between load
// and save round-trips to identical bytes (snapshot-diff skips it).
type mapBacked struct{ raw map[string]any }

// Raw returns the wrapped map by reference (not a copy). Mutations through the
// returned map are visible to anyone else holding the same wrapper.
func (r mapBacked) Raw() map[string]any { return r.raw }

// RecordID reads the canonical id field. Returns "" if absent or wrong type.
func (r mapBacked) RecordID() string { return utils.StrVal(r.raw, "id") }

// MarshalData serializes the row's JSONB representation.
func (r mapBacked) MarshalData() ([]byte, error) { return json.Marshal(r.raw) }

// TypedValues helpers shared by every wrapper's TypedValues method. They
// reproduce the previous store.typedValues semantics byte-for-byte: missing
// or nil values become NULL, strings are emitted via %v, ints/bools pass
// through as-is. Naming mirrors the historical local helpers (sv/bv/iv).
func tvStr(m map[string]any, k string) any {
	v, ok := m[k]
	if !ok || v == nil || v == "" {
		return nil
	}
	return fmt.Sprintf("%v", v)
}

func tvBool(m map[string]any, k string) any {
	v, _ := m[k].(bool)
	return v
}

func tvAny(m map[string]any, k string) any {
	v, ok := m[k]
	if !ok || v == nil || v == "" {
		return nil
	}
	return v
}

// registerMapBacked is the boilerplate registration for a mapBacked wrapper:
// it registers a single wrapper function used by both the load path
// (store.decodeRecord parses JSON then calls the wrapper) and the save path
// (store.wrapRecord wraps an already-decoded map).
func registerMapBacked(collection string, wrap func(map[string]any) store.Record) {
	store.RegisterRecordWrapper(collection, wrap)
}
