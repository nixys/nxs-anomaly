package engine

import (
	"context"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

// An integration carries two credentials: the HMAC secret that authenticates a
// sender and the routing key that lets anyone holding it page people. Both were
// returned verbatim to every role, a viewer included.

func actorCtx(role authz.Role) context.Context {
	return authz.NewContext(context.Background(), authz.Actor{Kind: authz.KindService, ID: "k", Role: role})
}

func seededIntegrationEngine(secret string) *Engine {
	ms := newMemStore()
	ms.seed("integrations", map[string]any{
		"id": "int-1", "name": "prom", "key": "key_abcdef123456", "routing_key": "key_abcdef123456",
		"webhook_secret": secret,
	})
	return crudEngine(ms)
}

func TestIntegrationSecretIsNeverReturned(t *testing.T) {
	e := seededIntegrationEngine("s3cr3t-hmac-value")
	for _, role := range []authz.Role{authz.RoleViewer, authz.RoleResponder, authz.RoleEditor, authz.RoleAdmin} {
		item, err := e.GetItem(actorCtx(role), "integrations", "int-1")
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if item["webhook_secret"] != nil {
			t.Errorf("%s reads webhook_secret %v, want it hidden", role, item["webhook_secret"])
		}
		if item["webhook_secret_set"] != true {
			t.Errorf("%s: webhook_secret_set = %v, want true", role, item["webhook_secret_set"])
		}
		page, err := e.ListCollectionPage(actorCtx(role), "integrations", map[string]any{})
		if err != nil {
			t.Fatalf("%s list: %v", role, err)
		}
		for _, row := range page["items"].([]map[string]any) {
			if row["webhook_secret"] != nil {
				t.Errorf("%s list returns webhook_secret %v", role, row["webhook_secret"])
			}
		}
	}
}

func TestRoutingKeyIsOnlyForThoseWhoWireSenders(t *testing.T) {
	e := seededIntegrationEngine("")
	for role, visible := range map[authz.Role]bool{
		authz.RoleViewer: false, authz.RoleResponder: false, authz.RoleEditor: true, authz.RoleAdmin: true,
	} {
		item, err := e.GetItem(actorCtx(role), "integrations", "int-1")
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		gotVisible := item["routing_key"] == "key_abcdef123456" && item["key"] == "key_abcdef123456"
		if gotVisible != visible {
			t.Errorf("%s: key=%v routing_key=%v, visible=%v want %v", role, item["key"], item["routing_key"], gotVisible, visible)
		}
	}
}

func TestSecretReferenceStaysVisible(t *testing.T) {
	e := seededIntegrationEngine("env:NXS_ANOMALY_PROM_HMAC")
	item, err := e.GetItem(actorCtx(authz.RoleAdmin), "integrations", "int-1")
	if err != nil {
		t.Fatal(err)
	}
	if item["webhook_secret"] != "env:NXS_ANOMALY_PROM_HMAC" {
		t.Errorf("webhook_secret = %v, want the env: reference, which names a variable and holds no secret", item["webhook_secret"])
	}
}
