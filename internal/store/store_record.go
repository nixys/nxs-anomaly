package store

import (
	"encoding/json"
	"fmt"
)

// Record is the persisted form of one collection row. It knows its id, its
// jsonb `data` serialization, and its typed-column values (in the order of
// TypedColumns[collection]). Every collection has a registered wrapper that
// turns an already-decoded map into the collection's Record type; the same
// wrapper backs both the load path (decodeRecord parses JSON, then wraps)
// and the save path (wrapRecord wraps an in-memory map).
type Record interface {
	RecordID() string
	MarshalData() ([]byte, error)
	TypedValues() []any
}

// RecordWrapper builds the collection's Record type from an already-decoded
// JSONB map. Registered from the model package's init so the store stays free
// of any model import (model depends on store for the Record interface, not
// vice versa). Every collection in EntityTables must have a wrapper — the
// load/save path panics on an unknown collection rather than falling back to
// an untyped map representation.
type RecordWrapper func(m map[string]any) Record

var recordWrappers = map[string]RecordWrapper{}

// RegisterRecordWrapper registers the wrapper for a collection. Idempotent:
// re-registering is allowed (last write wins), but normally each collection
// registers exactly once from its model file's init().
func RegisterRecordWrapper(collection string, w RecordWrapper) {
	recordWrappers[collection] = w
}

// RecordWrapperRegistered reports whether a Record wrapper is registered for the
// collection. The load/save path has no untyped fallback, so every collection in
// EntityTables must have one; model tests assert this invariant holds.
func RecordWrapperRegistered(collection string) bool {
	_, ok := recordWrappers[collection]
	return ok
}

// wrapRecord wraps an already-decoded JSONB map as the collection's Record.
// Returns an error when no wrapper is registered — by construction every
// EntityTables collection must have one (model.init covers them all).
func wrapRecord(collection string, m map[string]any) (Record, error) {
	w, ok := recordWrappers[collection]
	if !ok {
		return nil, fmt.Errorf("store: no record wrapper registered for collection %q", collection)
	}
	return w(m), nil
}

// decodeRecord parses a jsonb row and wraps it via the collection's
// registered wrapper. Used by every load path (loadPartialStateTx, ReadCollections).
func decodeRecord(collection, id string, data []byte) (Record, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return wrapRecord(collection, m)
}
