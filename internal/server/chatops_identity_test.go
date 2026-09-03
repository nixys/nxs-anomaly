package server

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/storetest"
)

// An inbound ChatOps command used to run as one service principal for everyone,
// so an acknowledge was attributed to "telegram" rather than to the engineer who
// sent it. chatopsPrincipal resolves the sender when it can; these tests pin
// which of the three outcomes each situation gets, because the difference
// between them is who ends up holding rights.

func seedTelegramUser(t *testing.T, st *storetest.Store, id, telegramID, role string) {
	t.Helper()
	if err := st.UpsertItem(context.Background(), "users", map[string]any{
		"id":          id,
		"username":    id,
		"telegram_id": telegramID,
		"role":        role,
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func TestChatopsPrincipalIdentifiesKnownSender(t *testing.T) {
	srv, st := newTestServer()
	seedTelegramUser(t, st, "usr-bob", "4242", string(authz.RoleResponder))

	actor, ok := srv.chatopsPrincipal(context.Background(), "telegram", "4242")
	if !ok {
		t.Fatal("a known sender with a valid role must be accepted")
	}
	if actor.ID != "usr-bob" {
		t.Errorf("actor id = %q, want the person behind the command", actor.ID)
	}
	if actor.Kind != authz.KindUser {
		t.Errorf("actor kind = %q, want a user so the audit trail names a person", actor.Kind)
	}
	if actor.Role != authz.RoleResponder {
		t.Errorf("actor role = %q, want the role the user actually holds", actor.Role)
	}
}

// A shared team chat is a legitimate way to run these commands, so an unknown
// sender keeps working exactly as before. Refusing here would break every
// installation that uses one.
func TestChatopsPrincipalFallsBackForUnknownSender(t *testing.T) {
	srv, _ := newTestServer()

	actor, ok := srv.chatopsPrincipal(context.Background(), "telegram", "999")
	if !ok {
		t.Fatal("an unknown sender must fall back, not be refused")
	}
	if actor.ID != "chatops:telegram" || actor.Kind != authz.KindService {
		t.Errorf("actor = %s/%s, want the platform service principal", actor.ID, actor.Kind)
	}
}

// Slack sends no identifier this deployment stores, so it keeps the service
// principal without a lookup.
func TestChatopsPrincipalWithoutPlatformIDIsService(t *testing.T) {
	srv, _ := newTestServer()

	actor, ok := srv.chatopsPrincipal(context.Background(), "slack", "")
	if !ok || actor.ID != "chatops:slack" {
		t.Fatalf("actor = %s (ok=%v), want the slack service principal", actor.ID, ok)
	}
}

// The one case where falling back is worse than failing: the sender is known,
// but their role was cleared. Handing them the service principal would grant
// rights they no longer have.
func TestChatopsPrincipalRefusesKnownSenderWithoutRole(t *testing.T) {
	srv, st := newTestServer()
	seedTelegramUser(t, st, "usr-carol", "7777", "")

	if _, ok := srv.chatopsPrincipal(context.Background(), "telegram", "7777"); ok {
		t.Fatal("a known sender whose role was cleared must be refused, not downgraded to the service principal")
	}
}

func TestTelegramIDAcceptsBothJSONShapes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"number as telegram sends it", float64(-1001234567890), "-1001234567890"},
		{"string as an operator may have typed it", "-1001234567890", "-1001234567890"},
		{"absent", nil, ""},
		{"unexpected type", []any{1}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := telegramID(c.in); got != c.want {
				t.Errorf("telegramID(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
