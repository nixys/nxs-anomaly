package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
)

func TestInvalidateTemplateCache(t *testing.T) {
	e := newEngine()
	e.templateCache.Store("int1:telegram", templateCacheEntry{value: "old telegram template", loadedAt: time.Now()})
	e.templateCache.Store("int1:email", templateCacheEntry{value: "old email template", loadedAt: time.Now()})
	e.templateCache.Store("int2:telegram", templateCacheEntry{value: "other integration template", loadedAt: time.Now()})

	e.invalidateTemplateCache("int1")

	if _, ok := e.templateCache.Load("int1:telegram"); ok {
		t.Error("int1:telegram must be evicted")
	}
	if _, ok := e.templateCache.Load("int1:email"); ok {
		t.Error("int1:email must be evicted")
	}
	if _, ok := e.templateCache.Load("int2:telegram"); !ok {
		t.Error("int2:telegram must survive invalidation of int1")
	}
}

func TestInvalidateTemplateCachePrefixSafety(t *testing.T) {
	// An integration whose ID is a prefix of another must not evict the longer one.
	e := newEngine()
	e.templateCache.Store("int1:telegram", templateCacheEntry{value: "short id", loadedAt: time.Now()})
	e.templateCache.Store("int12:telegram", templateCacheEntry{value: "longer id", loadedAt: time.Now()})

	e.invalidateTemplateCache("int1")

	if _, ok := e.templateCache.Load("int12:telegram"); !ok {
		t.Error("int12:telegram must survive invalidation of int1")
	}
}

// templateStoreStub serves one integration's templates and counts reads, so a
// test can tell a cache hit from a re-read. failNext makes the next read fail.
type templateStoreStub struct {
	store.PostgreSQLStore
	template string
	getCalls int
	failNext bool
}

func (s *templateStoreStub) GetItem(_ context.Context, _, _ string) (map[string]any, error) {
	s.getCalls++
	if s.failNext {
		s.failNext = false
		return nil, errors.New("connection reset")
	}
	return map[string]any{
		"id":        "int1",
		"templates": map[string]any{"telegram": s.template},
	}, nil
}

// The bug this guards: invalidateTemplateCache only reaches the process that
// handled the write, so a worker replica never learns that the API updated a
// template. Expiry is what makes the new text arrive without a restart.
func TestNotificationTemplateExpiresWithoutInvalidation(t *testing.T) {
	stub := &templateStoreStub{template: "old {{ title }}"}
	eng := New(stub)
	eng.SetReferenceCacheTTL(50 * time.Millisecond)
	ctx := context.Background()

	if got := eng.getNotificationTemplate(ctx, "int1", "telegram"); got != "old {{ title }}" {
		t.Fatalf("first read = %q", got)
	}
	// Within the TTL the store is not consulted again.
	eng.getNotificationTemplate(ctx, "int1", "telegram")
	if stub.getCalls != 1 {
		t.Fatalf("cached read must not hit the store, getCalls = %d", stub.getCalls)
	}

	// Another replica rewrites the template. No invalidation reaches us.
	stub.template = "new {{ title }}"
	if got := eng.getNotificationTemplate(ctx, "int1", "telegram"); got != "old {{ title }}" {
		t.Fatalf("still within TTL, want the old template, got %q", got)
	}

	time.Sleep(60 * time.Millisecond)
	if got := eng.getNotificationTemplate(ctx, "int1", "telegram"); got != "new {{ title }}" {
		t.Fatalf("past TTL the cross-replica write must be visible, got %q", got)
	}
}

// Re-reading is how an entry expires, so a database blip must not silently
// downgrade a configured template to the built-in default text.
func TestNotificationTemplateKeepsStaleCopyWhenStoreFails(t *testing.T) {
	stub := &templateStoreStub{template: "configured {{ title }}"}
	eng := New(stub)
	eng.SetReferenceCacheTTL(50 * time.Millisecond)
	ctx := context.Background()

	eng.getNotificationTemplate(ctx, "int1", "telegram")
	time.Sleep(60 * time.Millisecond)

	stub.failNext = true
	if got := eng.getNotificationTemplate(ctx, "int1", "telegram"); got != "configured {{ title }}" {
		t.Fatalf("failed re-read must fall back to the expired copy, got %q", got)
	}
	// The next delivery retries and succeeds.
	if got := eng.getNotificationTemplate(ctx, "int1", "telegram"); got != "configured {{ title }}" {
		t.Fatalf("recovered read = %q", got)
	}
}

// With no integration in hand there is nothing to cache and nothing to read.
func TestNotificationTemplateEmptyIntegrationID(t *testing.T) {
	stub := &templateStoreStub{template: "x"}
	eng := New(stub)
	if got := eng.getNotificationTemplate(context.Background(), "", "telegram"); got != "" {
		t.Fatalf("empty integration id = %q", got)
	}
	if stub.getCalls != 0 {
		t.Fatalf("empty integration id must not hit the store, getCalls = %d", stub.getCalls)
	}
}
