package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// Test-only guard for live suites: the framework's automatic destroy must never
// archive a cost center that existed before the test (or one with an empty ID).
type guardedCostCenter struct {
	*costCenterResource
	baseline map[string]bool
}

func (g guardedCostCenter) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data costCenterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Uses the actual state ID: cleanup can run before any capture check.
	if id := data.CostCenterID.ValueString(); id == "" || g.baseline[id] {
		resp.Diagnostics.AddError("Refusing to archive pre-existing cost center", fmt.Sprintf("Live test guard: cost center %q is empty or in the pre-test baseline; inspect the enterprise and Terraform state manually.", id))
		return
	}
	g.costCenterResource.Delete(ctx, req, resp)
}

type guardedProvider struct {
	*enterpriseProvider
	baseline map[string]bool
}

func (p guardedProvider) Resources(ctx context.Context) []func() resource.Resource {
	out := p.enterpriseProvider.Resources(ctx)
	for i, f := range out {
		if _, ok := f().(*costCenterResource); ok {
			out[i] = func() resource.Resource { return guardedCostCenter{&costCenterResource{}, p.baseline} }
		}
	}
	return out
}

// guardedFactories must be built after the baseline is captured.
func guardedFactories(baseline map[string]bool) map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){"github-enterprise": providerserver.NewProtocol6WithError(func() provider.Provider {
		return guardedProvider{New("test")().(*enterpriseProvider), baseline}
	}())}
}
