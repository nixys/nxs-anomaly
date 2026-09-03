package engine

import (
	"context"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// provisioning.go marks the objects an infrastructure-as-code tool created and
// keeps the UI from editing them.
//
// The problem it solves is not a technical one. Users, teams, schedules, chains,
// integrations and ChatOps channels can be described in Terraform (see the
// nxs-anomaly-terraform-provider repo), and Terraform's contract is that the
// file is the truth: anything changed underneath it is silently reverted on the
// next apply. Somebody editing an escalation chain in the web UI at 02:00 gets a
// chain that works until the next pipeline run and then quietly goes back to
// paging the wrong team — with nothing on screen ever having suggested the edit
// would not last.
//
// So the object says who owns it, and the entry points that would change it
// refuse. What is *not* done here is equally deliberate: reads are untouched,
// alert-group actions (acknowledge, resolve, silence) are untouched, and
// schedule overrides are untouched. Those are operational, they are what the
// on-call rota is for, and Terraform does not manage them.

// ProvisionedByField is the key an object carries its provisioner under. It is
// part of the API payload — the web UI reads it to disable its own edit
// controls, which is what makes the refusal visible before it is hit.
const ProvisionedByField = "provisioned_by"

// stampProvisioner records the tool a request came from onto a newly created
// object. A request from a person stamps nothing, so an object created in the
// UI stays editable there.
func stampProvisioner(ctx context.Context, item map[string]any) map[string]any {
	if p := authz.FromContext(ctx).Provisioner; p != "" {
		item[ProvisionedByField] = p
	}
	return item
}

// guardProvisioned refuses a change to an object another tool manages.
//
// The same tool may change it — that is the whole point, Terraform has to be
// able to apply — and so may a caller that declares itself as that tool, which
// is the escape hatch for an object whose Terraform definition is gone and
// which somebody now has to clean up by hand.
func guardProvisioned(ctx context.Context, collection string, item map[string]any) error {
	owner := utils.StrVal(item, ProvisionedByField)
	if owner == "" || owner == authz.FromContext(ctx).Provisioner {
		return nil
	}
	return errForbidden("this " + singular(collection) + " is provisioned by " + owner +
		" and must be changed there")
}
