package engine

import (
	"context"
	"testing"
)

func TestSanitizeLocale(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		want string
	}{
		{name: "russian", raw: "ru-RU", want: "ru-RU"},
		{name: "english", raw: "en-US", want: "en-US"},
		{name: "case normalized", raw: "RU-ru", want: "ru-RU"},
		{name: "not chosen", raw: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SanitizeLocale(tc.raw)
			if err != nil {
				t.Fatalf("SanitizeLocale: %v", err)
			}
			if got != tc.want {
				t.Fatalf("locale = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := SanitizeLocale("fr-FR"); err == nil {
		t.Fatal("unsupported locale accepted")
	}
}

func TestUpdateUserPreferencesOnlyChangesPresentationFields(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, err := e.CreateUser(context.Background(), map[string]any{
		"name": "Alice",
		"role": "viewer",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	id := created["id"].(string)

	updated, err := e.UpdateUserPreferences(context.Background(), id, map[string]any{
		"locale":   "en-US",
		"timezone": "Asia/Novosibirsk",
		"role":     "admin",
	})
	if err != nil {
		t.Fatalf("UpdateUserPreferences: %v", err)
	}
	if updated["locale"] != "en-US" || updated["timezone"] != "Asia/Novosibirsk" {
		t.Fatalf("preferences not updated: %v", updated)
	}
	if updated["role"] != "viewer" {
		t.Fatalf("self-service preferences changed role to %v", updated["role"])
	}
}

func TestUpdateUserPreferencesRejectsUnknownTimezone(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	created, err := e.CreateUser(context.Background(), map[string]any{"name": "Alice"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := e.UpdateUserPreferences(context.Background(), created["id"].(string), map[string]any{
		"timezone": "Mars/Olympus",
	}); err == nil {
		t.Fatal("unknown timezone accepted")
	}
}
