package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/model"
)

// A CREATE_ISSUE step with an inline tracker token copies it onto the
// notification, where delivery needs it for retries. /notifications and
// /history returned that payload to anyone who could see the group, and the
// chain itself returned the step to every reader.
func TestIssueTokenIsMaskedOnReadsButDelivered(t *testing.T) {
	const token = "redmine-api-key-9f3c"
	var gotAuth string
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ISSUE-1"}`))
	}))
	defer tracker.Close()

	ms := newMemStore()
	e := crudEngine(ms)
	chain, err := e.CreateEscalationChain(context.Background(), map[string]any{
		"name": "tickets",
		"steps": []any{map[string]any{
			"kind": StepCreateIssue, "url": tracker.URL, "tracker_type": "generic", "token": token,
		}},
	})
	if err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}

	s := newState(chain["steps"].([]any))
	e.advanceGroupLocked(s, model.WrapAlertGroup(newGroup(0, 0)), "2026-09-22T10:00:00+00:00")
	if len(s.Notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(s.Notifications))
	}
	var ntf map[string]any
	for _, rec := range s.Notifications {
		ntf = notificationMap(rec)
	}
	ms.seed("notifications", ntf)

	for _, role := range []authz.Role{authz.RoleViewer, authz.RoleResponder, authz.RoleEditor, authz.RoleAdmin} {
		ctx := actorCtx(role)
		n, err := e.GetItem(ctx, "notifications", ntf["id"].(string))
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if strings.Contains(stringify(n), token) {
			t.Errorf("%s reads the tracker token on the notification", role)
		}
		page, err := e.ListCollectionPage(ctx, "notifications", map[string]any{})
		if err != nil {
			t.Fatalf("%s list: %v", role, err)
		}
		if strings.Contains(stringify(page), token) {
			t.Errorf("%s reads the tracker token in the notification list", role)
		}

		c, err := e.GetItem(ctx, "escalation_chains", chain["id"].(string))
		if err != nil {
			t.Fatalf("%s chain: %v", role, err)
		}
		editor := authz.FromContext(ctx).Can(authz.ActionEdit)
		// Editors round-trip the steps they read; everyone else gets a mask.
		if visible := strings.Contains(stringify(c), token); visible != editor {
			t.Errorf("%s reading the chain: token visible = %v, want %v", role, visible, editor)
		}
	}

	// The stored row is untouched, and delivery still sends the real token.
	res := honestyEngine(ms, DeliveryConfig{}).deliverNotificationViaAdapter(context.Background(), ntf)
	if res.Status != deliveryDelivered {
		t.Fatalf("status = %q (%s), want delivered", res.Status, res.Err)
	}
	if gotAuth != "Bearer "+token {
		t.Errorf("tracker got Authorization %q, want the real token", gotAuth)
	}
}
