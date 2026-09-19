package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// refCacheTTL is the default staleness bound for cached reference
// collections. Writes made by this process invalidate the cache immediately
// (see refInvalidatingStore); the TTL only limits how long writes from OTHER
// replicas stay invisible, and matches the default worker poll interval.
// SetReferenceCacheTTL aligns it with a non-default poll interval.
const refCacheTTL = 5 * time.Second

// refCache caches full reference collections (users, teams, schedules, ...)
// for the hot ingest/escalation paths, which previously reloaded all of them
// from the database on every single alert.
type refCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]refCacheEntry
}

type refCacheEntry struct {
	loadedAt time.Time
	items    map[string]map[string]any
	// verified marks a load made because an id was missing (refSet.require).
	// What such a load still lacks is gone, so another miss on it does not
	// reload again until the entry expires.
	verified bool
}

func newRefCache() *refCache {
	return &refCache{ttl: refCacheTTL, entries: map[string]refCacheEntry{}}
}

func (c *refCache) setTTL(d time.Duration) {
	c.mu.Lock()
	c.ttl = d
	c.mu.Unlock()
}

func (c *refCache) getTTL() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ttl
}

func (c *refCache) get(name string, now time.Time) (map[string]map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	if !ok || now.Sub(e.loadedAt) > c.ttl {
		return nil, false
	}
	return e.items, true
}

func (c *refCache) put(name string, items map[string]map[string]any, now time.Time) {
	c.putEntry(name, refCacheEntry{loadedAt: now, items: items})
}

func (c *refCache) putEntry(name string, entry refCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[name] = entry
}

// verifiedFresh reports whether name was loaded to resolve a miss and has not
// expired since.
func (c *refCache) verifiedFresh(name string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	return ok && e.verified && now.Sub(e.loadedAt) <= c.ttl
}

func (c *refCache) invalidate(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range names {
		delete(c.entries, name)
	}
}

// refCollection returns the named collection as an id→item map, served from
// the short-TTL cache. The returned map and its items are shared across
// goroutines and MUST be treated as read-only by callers and mutators.
func (e *Engine) refCollection(ctx context.Context, name string) (map[string]map[string]any, error) {
	now := time.Now()
	if e.refCache != nil {
		if items, ok := e.refCache.get(name, now); ok {
			return items, nil
		}
	}
	items, err := e.loadRefCollection(ctx, name)
	if err != nil {
		return nil, err
	}
	if e.refCache != nil {
		e.refCache.put(name, items, now)
	}
	return items, nil
}

func (e *Engine) loadRefCollection(ctx context.Context, name string) (map[string]map[string]any, error) {
	list, err := e.store.ListCollection(ctx, name)
	if err != nil {
		return nil, err
	}
	items := make(map[string]map[string]any, len(list))
	for _, item := range list {
		if id := utils.StrVal(item, "id"); id != "" {
			items[id] = item
		}
	}
	return items, nil
}

// refSet loads several reference collections while remembering the first
// failure, so a caller needing six of them stays readable without dropping any
// error on the floor.
//
// Dropping them was not hypothetical. The hot paths used to read
//
//	usersMap, _ := e.refCollection(ctx, "users")
//
// and a failed read yields a nil map, not an empty one — but nothing downstream
// can tell those apart. An escalation step then finds no schedule, writes
// "No active user found in current schedule window" into the group timeline,
// advances current_step and clears next_run_at. The step is consumed for good:
// a transient database error is rendered as the considered judgement that
// nobody was on call, nobody is paged, no error is returned, no metric moves,
// and the incident timeline carries a plausible-looking explanation. A
// reference collection that could not be read is a failure, not an empty set.
type refSet struct {
	e    *Engine
	ctx  context.Context
	err  error
	name string // the collection that failed, for the error message
}

func (e *Engine) newRefSet(ctx context.Context) *refSet {
	return &refSet{e: e, ctx: ctx}
}

// get returns the named collection, or nil once an earlier get has failed.
// Callers check Err() once, after the last get.
func (r *refSet) get(name string) map[string]map[string]any {
	if r.err != nil {
		return nil
	}
	items, err := r.e.refCollection(r.ctx, name)
	if err != nil {
		r.err, r.name = err, name
	}
	return items
}

// require returns items, re-read from the database when any of ids is missing
// from it.
//
// Every replica caches references for refCacheTTL, and a write only drops the
// cache of the replica that made it. A chain or user created on another replica
// a moment ago is therefore missing from a warm cache, and the paging decision
// that reads it is final: "Escalation chain not found" or "Skipped recipient(s)
// that no longer exist", and nobody is ever paged for that group. So a miss is
// checked against the database before it is believed. A load made this way is
// marked verified, so an id that really is gone costs one reload per TTL, not
// one per alert.
func (r *refSet) require(items map[string]map[string]any, name string, ids []string) map[string]map[string]any {
	if r.err != nil {
		return items
	}
	missing := false
	for _, id := range ids {
		if _, ok := items[id]; id != "" && !ok {
			missing = true
			break
		}
	}
	if !missing {
		return items
	}
	now := time.Now()
	if r.e.refCache != nil && r.e.refCache.verifiedFresh(name, now) {
		return items
	}
	fresh, err := r.e.loadRefCollection(r.ctx, name)
	if err != nil {
		r.err, r.name = err, name
		return items
	}
	if r.e.refCache != nil {
		r.e.refCache.putEntry(name, refCacheEntry{loadedAt: now, items: fresh, verified: true})
	}
	return fresh
}

// requirePaging applies require to everything a paging decision reads: the
// chains in chainIDs, the schedules and teams their steps name, and every user
// those steps, schedules and teams page, plus userIDs. The maps are replaced in
// place.
func (r *refSet) requirePaging(chains, scheds, teams, users *map[string]map[string]any, chainIDs, userIDs []string) {
	*chains = r.require(*chains, "escalation_chains", chainIDs)
	var schedIDs, teamIDs []string
	for _, id := range chainIDs {
		for _, step := range asMaps((*chains)[id]["steps"]) {
			userIDs = append(userIDs, anyToStringSlice(step["user_ids"])...)
			userIDs = append(userIDs, utils.StrVal(step, "user_id"))
			schedIDs = append(schedIDs, utils.StrVal(step, "schedule_id"))
			teamIDs = append(teamIDs, utils.StrVal(step, "team_id"))
		}
	}
	*scheds = r.require(*scheds, "schedules", schedIDs)
	*teams = r.require(*teams, "teams", teamIDs)
	for _, id := range schedIDs {
		sched := (*scheds)[id]
		for _, key := range []string{"shifts", "overrides"} {
			for _, entry := range asMaps(sched[key]) {
				userIDs = append(userIDs, utils.StrVal(entry, "user_id"))
			}
		}
		if rot, ok := sched["rotation"].(map[string]any); ok {
			userIDs = append(userIDs, anyToStringSlice(rot["participant_ids"])...)
		}
	}
	for _, id := range teamIDs {
		userIDs = append(userIDs, anyToStringSlice((*teams)[id]["member_ids"])...)
	}
	*users = r.require(*users, "users", userIDs)
}

// policyUserIDs names the users an integration's notification policy pages.
func policyUserIDs(integration map[string]any) []string {
	policy, _ := integration["notification_policy"].(map[string]any)
	return []string{utils.StrVal(policy, "emergency_user_id"), utils.StrVal(policy, "epic_user_id")}
}

// asMaps reads a JSON list of objects, whichever concrete slice type holds it.
func asMaps(v any) []map[string]any {
	switch xs := v.(type) {
	case []map[string]any:
		return xs
	case []any:
		out := make([]map[string]any, 0, len(xs))
		for _, x := range xs {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// Err reports the first failure, naming the collection that caused it.
func (r *refSet) Err() error {
	if r.err == nil {
		return nil
	}
	return fmt.Errorf("load reference collection %q: %w", r.name, r.err)
}

// refInvalidatingStore wraps the store so that any write touching a collection
// drops that collection from the reference cache. This keeps the cache
// consistent with same-process CRUD (create user → next ingest sees it);
// cross-replica writes become visible after at most refCacheTTL.
type refInvalidatingStore struct {
	store.PostgreSQLStore
	cache *refCache
}

func (w refInvalidatingStore) UpdateCollections(ctx context.Context, loadCollections, saveCollections []string, mutator func(*store.State) (any, error), lockKey int64) (any, error) {
	result, err := w.PostgreSQLStore.UpdateCollections(ctx, loadCollections, saveCollections, mutator, lockKey)
	if err == nil {
		w.cache.invalidate(saveCollections...)
	}
	return result, err
}

func (w refInvalidatingStore) UpdateCollectionsFiltered(ctx context.Context, loads []store.LoadSpec, saveCollections []string, mutator func(*store.State) (any, error), lockKey int64) (any, error) {
	result, err := w.PostgreSQLStore.UpdateCollectionsFiltered(ctx, loads, saveCollections, mutator, lockKey)
	if err == nil {
		w.cache.invalidate(saveCollections...)
	}
	return result, err
}

func (w refInvalidatingStore) UpdateCollectionsWriteAll(ctx context.Context, loads []store.LoadSpec, saveCollections, writeAll []string, mutator func(*store.State) (any, error), lockKey int64) (any, error) {
	result, err := w.PostgreSQLStore.UpdateCollectionsWriteAll(ctx, loads, saveCollections, writeAll, mutator, lockKey)
	if err == nil {
		w.cache.invalidate(saveCollections...)
	}
	return result, err
}

func (w refInvalidatingStore) UpsertItem(ctx context.Context, collection string, item map[string]any) error {
	err := w.PostgreSQLStore.UpsertItem(ctx, collection, item)
	if err == nil {
		w.cache.invalidate(collection)
	}
	return err
}

func (w refInvalidatingStore) DeleteItem(ctx context.Context, collection, id string) (map[string]any, error) {
	item, err := w.PostgreSQLStore.DeleteItem(ctx, collection, id)
	if err == nil {
		w.cache.invalidate(collection)
	}
	return item, err
}

func (w refInvalidatingStore) SoftDeleteItem(ctx context.Context, collection, id string) (map[string]any, error) {
	item, err := w.PostgreSQLStore.SoftDeleteItem(ctx, collection, id)
	if err == nil {
		w.cache.invalidate(collection)
	}
	return item, err
}

func (w refInvalidatingStore) ClearCollections(ctx context.Context, collections []string) error {
	err := w.PostgreSQLStore.ClearCollections(ctx, collections)
	if err == nil {
		w.cache.invalidate(collections...)
	}
	return err
}
