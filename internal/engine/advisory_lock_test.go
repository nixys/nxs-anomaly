package engine

import (
	"fmt"
	"testing"
)

// The advisory-lock registry in engine.go is maintained by hand: forty constants
// whose only stated invariant is a comment saying they must be unique. Nothing
// enforced it. A duplicate value does not break the build and does not fail any
// behavioural test — it just silently serialises two unrelated operations behind
// one global lock, which shows up as a throughput cliff nobody can attribute.
// These tests are the enforcement.

// reservedLockKeys are values that were used by a previous locking scheme and
// must never be handed to a new operation: a replica running the older binary
// during a rolling upgrade still holds them for the old meaning, so reusing one
// would make two different operations block each other across versions.
var reservedLockKeys = map[int64]string{
	72544102: "global escalation xact lock (replaced by per-integration shards)",
	72544300: "single escalation gate (replaced by per-integration shards)",
	72544103: "deliveries save lock (replaced by FOR UPDATE SKIP LOCKED claims)",
	72544105: "retries save lock (replaced by FOR UPDATE SKIP LOCKED claims)",
}

func TestAdvisoryLockKeysAreUnique(t *testing.T) {
	seen := map[int64]string{}
	for name, key := range advisoryLock {
		if other, dup := seen[key]; dup {
			t.Errorf("advisory lock key %d is shared by %q and %q: unrelated operations would serialise against each other", key, other, name)
			continue
		}
		seen[key] = name
	}
}

func TestAdvisoryLockKeysAvoidHashedRanges(t *testing.T) {
	// ingestLockKey and escalationShardKey derive their keys by hashing an
	// integration ID into 1024 buckets. A named lock landing inside either
	// window would collide with whichever integration happens to hash there —
	// intermittently, on one integration out of a thousand, which is close to
	// undebuggable from the outside.
	ranges := []struct {
		name       string
		base       int64
		buckets    int64
		derivedBy  string
		sampleFunc func(string) int64
	}{
		{"ingest", 72545000, 1024, "ingestLockKey", ingestLockKey},
		{"escalation shard", 72546000, 1024, "escalationShardKey", escalationShardKey},
	}
	for _, r := range ranges {
		// Guard the constants themselves: if someone moves a base or widens the
		// bucket count, the window checked below must move with it.
		for _, id := range []string{"", "int1", "integration-with-a-much-longer-id"} {
			got := r.sampleFunc(id)
			if got < r.base || got >= r.base+r.buckets {
				t.Fatalf("%s(%q) = %d, outside its declared window [%d, %d)", r.derivedBy, id, got, r.base, r.base+r.buckets)
			}
		}
		for name, key := range advisoryLock {
			if key >= r.base && key < r.base+r.buckets {
				t.Errorf("advisory lock %q = %d falls inside the %s window [%d, %d) derived by %s", name, key, r.name, r.base, r.base+r.buckets, r.derivedBy)
			}
		}
	}
}

// TestHashedLockWindowsOverlapIsTheKnownOne pins the arithmetic behind the two
// hashed windows.
//
// They overlap today — ingest ends at 72546023, escalation starts at 72546000 —
// and that overlap is accepted rather than fixed, because every fix changes which
// key an integration maps to and a rolling upgrade would then have two replicas
// locking the same integration differently: duplicate pages, or duplicate alert
// groups. The reasoning is written out at ingestLockKey.
//
// What must not happen is the overlap growing, or a range moving, without anyone
// deciding to. So this test asserts the exact current geometry: change a base or
// a bucket count and it fails, which is the prompt to do the transitional
// double-lock release rather than to edit the constants and move on.
func TestHashedLockWindowsOverlapIsTheKnownOne(t *testing.T) {
	const (
		ingestBase      int64 = 72545000
		escalationBase  int64 = 72546000
		buckets         int64 = 1024
		knownOverlapLen int64 = 24
	)

	// The windows the two functions actually produce, derived rather than
	// restated, so a change to the hash or the modulus is caught too.
	ingestLo, ingestHi := hashedWindow(t, ingestLockKey)
	escLo, escHi := hashedWindow(t, escalationShardKey)

	if ingestLo != ingestBase || ingestHi != ingestBase+buckets-1 {
		t.Fatalf("ingest window moved: got [%d, %d], expected [%d, %d]", ingestLo, ingestHi, ingestBase, ingestBase+buckets-1)
	}
	if escLo != escalationBase || escHi != escalationBase+buckets-1 {
		t.Fatalf("escalation window moved: got [%d, %d], expected [%d, %d]", escLo, escHi, escalationBase, escalationBase+buckets-1)
	}

	overlap := ingestHi - escLo + 1
	if overlap != knownOverlapLen {
		t.Errorf("the ingest/escalation key overlap changed from %d keys to %d.\n"+
			"That is not a free edit: moving or resizing either window changes which key an\n"+
			"integration hashes to, so during a rolling upgrade the old and new replicas lock\n"+
			"the same integration under different keys and can double-escalate it. If this is\n"+
			"intended, ship the transitional release that takes both keys first, then update\n"+
			"this test.", knownOverlapLen, overlap)
	}
}

// hashedWindow returns the lowest and highest key a bucketing function emits,
// sampled widely enough to hit every bucket.
func hashedWindow(t *testing.T, keyFor func(string) int64) (lo, hi int64) {
	t.Helper()
	lo, hi = int64(1)<<62, int64(0)
	for i := 0; i < 200_000; i++ {
		k := keyFor(fmt.Sprintf("integration-%d", i))
		if k < lo {
			lo = k
		}
		if k > hi {
			hi = k
		}
	}
	return lo, hi
}

func TestAdvisoryLockKeysDoNotReuseRetiredKeys(t *testing.T) {
	for name, key := range advisoryLock {
		if why, reserved := reservedLockKeys[key]; reserved {
			t.Errorf("advisory lock %q reuses retired key %d, previously the %s", name, key, why)
		}
	}
}

func TestAdvisoryLockKeysArePositive(t *testing.T) {
	// updateCollections treats lockKey <= 0 as "take no lock at all". A named
	// operation that ended up with a zero value would therefore run unlocked
	// while reading as locked at the call site.
	for name, key := range advisoryLock {
		if key <= 0 {
			t.Errorf("advisory lock %q = %d; updateCollections skips the lock entirely for non-positive keys", name, key)
		}
	}
}

// TestAdvisoryLockNamesAreResolved catches the other half of the failure mode:
// advisoryLock is a map, so a typo'd lookup at a call site yields 0 rather than
// a compile error, and — per the test above — 0 means "no lock". Every name a
// call site asks for must exist in the registry.
func TestAdvisoryLockNamesAreResolved(t *testing.T) {
	for _, name := range advisoryLockCallSiteNames {
		if _, ok := advisoryLock[name]; !ok {
			t.Errorf("call site uses advisoryLock[%q], which is not in the registry: the lookup yields 0 and the operation runs unlocked", name)
		}
	}
}

// advisoryLockCallSiteNames lists the keys looked up by production code. It is
// maintained alongside the call sites; the check above is what makes a stale or
// misspelled entry visible.
var advisoryLockCallSiteNames = []string{
	"ingest_alert", "process_notification_batches", "publish_kafka_outbox",
	"create_user", "create_team", "create_schedule", "create_escalation_chain",
	"create_integration", "create_chatops_channel",
	"post_chatops_command", "register_mobile_device", "create_mobile_session",
	"acknowledge_group", "resolve_group", "unresolve_group", "unacknowledge_group",
	"toggle_user_duty", "update_user",
	"create_schedule_override", "update_schedule_override", "delete_schedule_override",
	"purge_user_from_schedules", "notify_schedule_shift", "duty_checkins",
	"heartbeat_state",
	"update_team", "update_schedule", "update_escalation_chain", "update_integration",
	"update_chatops_channel", "rotate_integration_key",
	"bulk_resolve_groups", "silence_group", "bulk_acknowledge_groups", "bulk_silence_groups",
	"advance_policy_runs",
	"create_maintenance_window", "update_maintenance_window",
	"generate_oncall_report",
	"record_delivery_failures",
}

// TestAdvisoryLockRegistryIsFullyExercised keeps the list above honest in the
// other direction: a registry entry no call site names is dead weight, and dead
// weight is exactly what gets reused by accident later.
func TestAdvisoryLockRegistryIsFullyExercised(t *testing.T) {
	named := make(map[string]bool, len(advisoryLockCallSiteNames))
	for _, n := range advisoryLockCallSiteNames {
		named[n] = true
	}
	var orphans []string
	for name := range advisoryLock {
		if !named[name] {
			orphans = append(orphans, fmt.Sprintf("%s=%d", name, advisoryLock[name]))
		}
	}
	if len(orphans) > 0 {
		t.Errorf("advisoryLock entries with no call site: %v — remove them, or add them to advisoryLockCallSiteNames if they are used", orphans)
	}
}
