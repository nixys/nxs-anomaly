package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
)

func TestRefCacheTTLAndInvalidate(t *testing.T) {
	c := newRefCache()
	now := time.Now()
	items := map[string]map[string]any{"u1": {"id": "u1"}}

	if _, ok := c.get("users", now); ok {
		t.Fatal("empty cache must miss")
	}
	c.put("users", items, now)
	if got, ok := c.get("users", now.Add(refCacheTTL)); !ok || len(got) != 1 {
		t.Fatal("entry within TTL must hit")
	}
	if _, ok := c.get("users", now.Add(refCacheTTL+time.Second)); ok {
		t.Fatal("entry past TTL must miss")
	}

	c.put("users", items, now)
	c.put("teams", items, now)
	c.invalidate("users")
	if _, ok := c.get("users", now); ok {
		t.Fatal("invalidated entry must miss")
	}
	if _, ok := c.get("teams", now); !ok {
		t.Fatal("other entry must survive invalidation")
	}
}

// refStoreStub counts ListCollection calls; other methods come from the
// embedded nil interface and must not be called by the code under test.
type refStoreStub struct {
	store.PostgreSQLStore
	listCalls int
}

func (s *refStoreStub) ListCollection(_ context.Context, _ string) ([]map[string]any, error) {
	s.listCalls++
	return []map[string]any{{"id": "u1", "username": "alice"}}, nil
}

func (s *refStoreStub) UpsertItem(_ context.Context, _ string, _ map[string]any) error {
	return nil
}

func TestRefCollectionCachesAndInvalidatesOnWrite(t *testing.T) {
	stub := &refStoreStub{}
	eng := New(stub)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		users, err := eng.refCollection(ctx, "users")
		if err != nil {
			t.Fatalf("refCollection failed: %v", err)
		}
		if users["u1"] == nil {
			t.Fatal("cached collection must contain u1")
		}
	}
	if stub.listCalls != 1 {
		t.Fatalf("ListCollection calls = %d, want 1 (cached)", stub.listCalls)
	}

	// A write through the engine's store must invalidate the cached collection.
	if err := eng.store.UpsertItem(ctx, "users", map[string]any{"id": "u2"}); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if _, err := eng.refCollection(ctx, "users"); err != nil {
		t.Fatalf("refCollection after write failed: %v", err)
	}
	if stub.listCalls != 2 {
		t.Fatalf("ListCollection calls = %d, want 2 (reloaded after write)", stub.listCalls)
	}
}

func TestSetReferenceCacheTTL(t *testing.T) {
	c := newRefCache()
	now := time.Now()
	items := map[string]map[string]any{"u1": {"id": "u1"}}
	c.put("users", items, now)

	// Default TTL: expired after refCacheTTL.
	if _, ok := c.get("users", now.Add(refCacheTTL+time.Second)); ok {
		t.Fatal("entry should be expired at default TTL")
	}
	// Raised TTL keeps the same entry alive past the default bound.
	c.setTTL(10 * time.Second)
	if _, ok := c.get("users", now.Add(refCacheTTL+time.Second)); !ok {
		t.Fatal("entry should be alive under raised TTL")
	}
	if _, ok := c.get("users", now.Add(11*time.Second)); ok {
		t.Fatal("entry should be expired past raised TTL")
	}

	// Engine-level setter: nil cache (unit-test engines) and non-positive
	// durations must be no-ops.
	eng := &Engine{}
	eng.SetReferenceCacheTTL(time.Second)
	eng = &Engine{refCache: c}
	eng.SetReferenceCacheTTL(0)
	if _, ok := c.get("users", now.Add(9*time.Second)); !ok {
		t.Fatal("non-positive duration must not change the TTL")
	}
}
