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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[name] = refCacheEntry{loadedAt: now, items: items}
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
	if e.refCache != nil {
		e.refCache.put(name, items, now)
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
