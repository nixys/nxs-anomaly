package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/nixys/nxs-anomaly/internal/authz"
)

// An object described in Terraform is owned by the file, not by whoever has the
// UI open: an edit made here survives until the next apply and then silently
// disappears. These tests pin both halves — the mark that says so, and the
// refusal that makes it mean something.

func terraformCtx() context.Context {
	return authz.NewContext(context.Background(), authz.Actor{
		ID: "api-key-tf", Kind: authz.KindService, Role: authz.RoleAdmin, Provisioner: "terraform",
	})
}

func TestCreateMarksTheProvisioner(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)

	created, err := e.CreateEscalationChain(terraformCtx(), map[string]any{"name": "prod-pager"})
	if err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	if created[ProvisionedByField] != "terraform" {
		t.Fatalf("%s = %v, want terraform", ProvisionedByField, created[ProvisionedByField])
	}

	// The same call from a person leaves the object editable — the mark is not
	// a default, it is a statement about where the object came from.
	byHand, err := e.CreateEscalationChain(context.Background(), map[string]any{"name": "adhoc"})
	if err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	if _, marked := byHand[ProvisionedByField]; marked {
		t.Errorf("a hand-made chain was marked %v", byHand[ProvisionedByField])
	}
}

func TestProvisionedObjectsRefuseUIEdits(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	chain, err := e.CreateEscalationChain(terraformCtx(), map[string]any{"name": "prod-pager"})
	if err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	id := chain["id"].(string)

	if _, err := e.UpdateEscalationChain(context.Background(), id,
		map[string]any{"name": "edited in the UI"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update error = %v, want forbidden", err)
	}
	if _, err := e.DeleteEntity(context.Background(), "escalation_chains", id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete error = %v, want forbidden", err)
	}
	if got := ms.data["escalation_chains"][id]["name"]; got != "prod-pager" {
		t.Errorf("name = %v, want the Terraform value untouched", got)
	}
}

// Terraform itself must still be able to apply, or the mark would freeze the
// object for everyone including the tool that owns it.
func TestProvisionerMayStillChangeItsOwnObjects(t *testing.T) {
	ms := newMemStore()
	e := crudEngine(ms)
	chain, err := e.CreateEscalationChain(terraformCtx(), map[string]any{"name": "prod-pager"})
	if err != nil {
		t.Fatalf("CreateEscalationChain: %v", err)
	}
	id := chain["id"].(string)

	if _, err := e.UpdateEscalationChain(terraformCtx(), id, map[string]any{"name": "prod-pager-v2"}); err != nil {
		t.Fatalf("UpdateEscalationChain as terraform: %v", err)
	}
	if _, err := e.DeleteEntity(terraformCtx(), "escalation_chains", id); err != nil {
		t.Fatalf("DeleteEntity as terraform: %v", err)
	}
}

// Acknowledging an alert is not editing configuration. If the mark reached the
// operational paths it would make a Terraform-managed integration's alerts
// unactionable, which is the opposite of the point.
func TestProvisionedIntegrationStillPagesAndResolves(t *testing.T) {
	ms := ingestStore()
	ms.data["integrations"]["int-1"][ProvisionedByField] = "terraform"
	e := crudEngine(ms)

	group := ingestOne(t, e, "disk full")
	groupID, _ := group["id"].(string)
	if groupID == "" {
		t.Fatal("ingest produced no alert group")
	}
	if _, err := e.ResolveGroup(context.Background(), groupID); err != nil {
		t.Fatalf("ResolveGroup on a provisioned integration's alert: %v", err)
	}
}
