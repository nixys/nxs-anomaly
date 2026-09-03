package engine

import (
	"fmt"
	"testing"
)

func TestEscalationShardKey(t *testing.T) {
	// Deterministic: same integration always maps to the same shard.
	first := escalationShardKey("int-1")
	if first != escalationShardKey("int-1") {
		t.Fatal("escalationShardKey is not deterministic")
	}
	// Keys stay in the reserved range [72546000, 72546000+1024), distinct from
	// ingestLockKey (72545000+) and the fixed operation locks.
	for _, id := range []string{"", "a", "int-xyz", "a-much-longer-integration-id-value"} {
		k := escalationShardKey(id)
		if k < 72546000 || k >= 72546000+1024 {
			t.Errorf("escalationShardKey(%q)=%d out of range", id, k)
		}
	}
	// Reasonable distribution: 100 integrations should not all collide.
	seen := map[int64]bool{}
	for i := 0; i < 100; i++ {
		seen[escalationShardKey(fmt.Sprintf("int-%d", i))] = true
	}
	if len(seen) < 10 {
		t.Errorf("poor shard distribution: %d buckets for 100 integrations", len(seen))
	}
}
