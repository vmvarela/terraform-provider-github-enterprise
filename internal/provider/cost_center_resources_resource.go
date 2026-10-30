package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/go-github/v93/github"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var (
	_ resource.ResourceWithImportState = &costCenterResourcesResource{}
	_ resource.ResourceWithModifyPlan  = &costCenterResourcesResource{}
)

type costCenterResourcesResource struct{ configuredResource }
type costCenterResourcesModel struct {
	ID              types.String `tfsdk:"id"`
	Enterprise      types.String `tfsdk:"enterprise_slug"`
	CostCenterID    types.String `tfsdk:"cost_center_id"`
	Users           types.Set    `tfsdk:"users"`
	Organizations   types.Set    `tfsdk:"organizations"`
	Repositories    types.Set    `tfsdk:"repositories"`
	EnterpriseTeams types.Set    `tfsdk:"enterprise_teams"`
}

type slot struct {
	typ   string // resource type GitHub reports when reading
	attr  string
	names *[]string
}

func slots(a *client.CostCenterAssignments) []slot {
	return []slot{{"User", "users", &a.Users}, {"Org", "organizations", &a.Organizations}, {"Repo", "repositories", &a.Repositories}, {"Team", "enterprise_teams", &a.EnterpriseTeams}}
}
func (m *costCenterResourcesModel) sets() []*types.Set {
	return []*types.Set{&m.Users, &m.Organizations, &m.Repositories, &m.EnterpriseTeams}
}
func (m *costCenterResourcesModel) desired(ctx context.Context) (a client.CostCenterAssignments, ds diag.Diagnostics) {
	for i, s := range slots(&a) {
		ds.Append(m.sets()[i].ElementsAs(ctx, s.names, false)...)
	}
	return
}
func (m *costCenterResourcesModel) setObserved(ctx context.Context, a client.CostCenterAssignments) (ds diag.Diagnostics) {
	for i, s := range slots(&a) {
		v, d := types.SetValueFrom(ctx, types.StringType, append([]string{}, *s.names...))
		ds.Append(d...)
		*m.sets()[i] = v
	}
	return
}
func emptyAssignments(a client.CostCenterAssignments) bool {
	return len(a.Users)+len(a.Organizations)+len(a.Repositories)+len(a.EnterpriseTeams) == 0
}

// assignmentsOf maps GitHub's documented read types; any other type or a
// blank name fails closed because it could not be managed or removed safely.
func assignmentsOf(cc *github.CostCenter) (a client.CostCenterAssignments, err error) {
	for _, res := range cc.Resources {
		if strings.TrimSpace(res.Name) == "" {
			return a, fmt.Errorf("cost center %s contains a resource without a name; refusing to manage it", cc.ID)
		}
		found := false
		for _, s := range slots(&a) {
			if s.typ == res.Type {
				found = true
				if !containsExact(*s.names, res.Name) {
					*s.names = append(*s.names, res.Name)
				}
			}
		}
		if !found {
			return a, fmt.Errorf("cost center %s contains a resource of unsupported type %q (%q); this provider only manages User, Org, Repo and Team assignments", cc.ID, res.Type, res.Name)
		}
	}
	return a, nil
}
func containsExact(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// planChange computes exact-name deltas. A name that only differs by case from
// one being removed is refused instead of being removed and re-added.
func planChange(current, desired client.CostCenterAssignments) (add, remove client.CostCenterAssignments, err error) {
	cur, want, a, rm := slots(&current), slots(&desired), slots(&add), slots(&remove)
	for i := range cur {
		*a[i].names, *rm[i].names = differences(*cur[i].names, *want[i].names)
		for _, n := range *a[i].names {
			for _, o := range *rm[i].names {
				if strings.EqualFold(n, o) {
					return add, remove, fmt.Errorf("%s %q differs only by case from existing %q; use the exact name GitHub returns", a[i].attr, n, o)
				}
			}
		}
	}
	return add, remove, nil
}

// preflight refuses to add a name that any other active cost center holds, so
// GitHub never silently reassigns billing. It needs enterprise-wide visibility.
func (r *costCenterResourcesResource) preflight(ctx context.Context, enterprise, self string, add client.CostCenterAssignments) error {
	if emptyAssignments(add) {
		return nil
	}
	centers, err := r.client.CostCenters(ctx, enterprise, "active")
	if err != nil {
		return fmt.Errorf("list cost centers for conflict check: %w", err)
	}
	for _, c := range centers {
		if c.ID == self {
			continue
		}
		// Any detail error (even 404 for a listed center) fails closed: its holdings are unverified.
		full, err := r.client.CostCenter(ctx, enterprise, c.ID)
		if err != nil {
			return fmt.Errorf("inspect cost center %s for conflict check (nothing was changed): %w", c.ID, err)
		}
		held, err := assignmentsOf(full) // rejects unknown types and blank names
		if err != nil {
			return fmt.Errorf("conflict check cannot trust cost center %s (nothing was changed): %w", c.ID, err)
		}
		hs := slots(&held)
		for i, s := range slots(&add) {
			for _, n := range *s.names {
				for _, h := range *hs[i].names {
					if strings.EqualFold(n, h) {
						return fmt.Errorf("%s %q is already assigned to cost center %s; remove it there first (nothing was changed)", s.attr, n, c.ID)
					}
				}
			}
		}
	}
	return nil
}

func (r *costCenterResourcesResource) observe(ctx context.Context, enterprise, id string) (client.CostCenterAssignments, error) {
	cc, err := r.client.CostCenter(ctx, enterprise, id)
	if err != nil {
		return client.CostCenterAssignments{}, err
	}
	if costCenterArchived(cc) {
		return client.CostCenterAssignments{}, errArchived
	}
	return assignmentsOf(cc)
}
func absent(err error) bool { return client.IsNotFound(err) || errors.Is(err, errArchived) }

// refresh replaces the model's sets with actual membership. On failure the
// model keeps its last known values.
func (r *costCenterResourcesResource) refresh(ctx context.Context, m *costCenterResourcesModel, ds *diag.Diagnostics) bool {
	a, err := r.observe(ctx, m.Enterprise.ValueString(), m.CostCenterID.ValueString())
	if err != nil {
		ds.AddError("Refresh cost center resources", "Could not observe the cost center after a change; state keeps the last known assignments: "+err.Error())
		return false
	}
	set := m.setObserved(ctx, a)
	ds.Append(set...)
	return !set.HasError()
}

func (r *costCenterResourcesResource) write(ctx context.Context, enterprise, id string, remove, add client.CostCenterAssignments) error {
	if !emptyAssignments(remove) {
		if err := r.client.RemoveCostCenterResources(ctx, enterprise, id, remove); err != nil {
			return fmt.Errorf("remove cost center resources: %w", err)
		}
	}
	if !emptyAssignments(add) {
		moved, err := r.client.AddCostCenterResources(ctx, enterprise, id, add)
		if err != nil {
			return fmt.Errorf("add cost center resources: %w", err)
		}
		if len(moved) > 0 {
			var parts []string
			for _, m := range moved {
				parts = append(parts, fmt.Sprintf("%s %q from cost center %s", m.ResourceType, m.Name, m.PreviousCostCenter))
			}
			return fmt.Errorf("GitHub reassigned resources from other cost centers (%s); they are not rolled back, state reflects actual membership", strings.Join(parts, ", "))
		}
	}
	return nil
}

type setRule struct{}

func (setRule) Description(context.Context) string {
	return "Names must be non-blank, at most 255 characters, without control characters, and unique ignoring case."
}
func (v setRule) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v setRule) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	seen := map[string]bool{}
	for _, e := range req.ConfigValue.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() {
			continue
		}
		n := s.ValueString()
		bad := s.IsNull() || strings.TrimSpace(n) == "" || len([]rune(n)) > 255
		for _, r := range n {
			bad = bad || unicode.IsControl(r)
		}
		if bad || seen[strings.ToLower(n)] {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid cost center resource name", v.Description(ctx))
			return
		}
		seen[strings.ToLower(n)] = true
	}
}

func (r *costCenterResourcesResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_cost_center_resources"
}
func (r *costCenterResourcesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	set := func(what string) schema.SetAttribute {
		return schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})), Validators: []validator.Set{setRule{}}, MarkdownDescription: what + " Names are opaque identifiers exactly as GitHub reports them; this provider does not check their format. Omitting the argument means none."}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Exclusively owns the **entire** assignment set of one cost center (users, organizations, repositories and enterprise teams). Anything not listed is removed, and **destroying this resource removes every assignment** of the cost center. Use one instance per cost center. Creation refuses a cost center that already has assignments: import it first (`terraform import ... enterprise_slug/cost_center_id`) so nothing is adopted implicitly. Before adding resources the provider lists all active cost centers and refuses any resource already held by another one instead of silently reassigning its billing; this check needs enterprise-wide visibility (an enterprise owner or billing manager token), because organization-owner tokens may see only part of the enterprise. Concurrent changes outside Terraform cannot be made atomic. Read maps GitHub types User, Org, Repo and Team to users, organizations, repositories and enterprise_teams; any other type or a blank name makes read fail without changing state.", Attributes: map[string]schema.Attribute{
		"id":               schema.StringAttribute{Computed: true, MarkdownDescription: "Stable identity: enterprise_slug/cost_center_id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enterprise_slug":  schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Enterprise account slug. Defaults to the provider-level enterprise_slug. Changing it replaces the resource.", Validators: []validator.String{enterpriseRule()}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"cost_center_id":   schema.StringAttribute{Required: true, MarkdownDescription: "Opaque ID of the cost center. Changing it replaces the resource.", Validators: []validator.String{costCenterIDRule()}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"users":            set("Usernames assigned to the cost center."),
		"organizations":    set("Organizations assigned to the cost center."),
		"repositories":     set("Repositories assigned to the cost center."),
		"enterprise_teams": set("Enterprise teams assigned to the cost center."),
	}}
}
func (r *costCenterResourcesResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	applyEnterpriseSlugDefault(ctx, r.enterpriseSlug, req, resp)
}

func (r *costCenterResourcesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data costCenterResourcesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ent, id := data.Enterprise.ValueString(), data.CostCenterID.ValueString()
	want, ds := data.desired(ctx)
	resp.Diagnostics.Append(ds...)
	current, err := r.observe(ctx, ent, id)
	if absent(err) {
		resp.Diagnostics.AddError("Cost center not found", "The cost center does not exist or is archived.")
	} else if err != nil {
		resp.Diagnostics.AddError("Read cost center resources", err.Error())
	} else if !emptyAssignments(current) {
		resp.Diagnostics.AddError("Existing cost center assignments", "Import this cost center's assignments before managing them. Creation will not adopt or overwrite existing assignments.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.preflight(ctx, ent, id, want); err != nil {
		resp.Diagnostics.AddError("Cost center assignment conflict", err.Error())
		return
	}
	data.ID = types.StringValue(ent + "/" + id)
	resp.Diagnostics.Append(data.setObserved(ctx, current)...) // verified-empty baseline, never the plan
	if err := r.write(ctx, ent, id, client.CostCenterAssignments{}, want); err != nil {
		resp.Diagnostics.AddError("Assign cost center resources", err.Error())
		r.refresh(ctx, &data, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}
	r.refresh(ctx, &data, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *costCenterResourcesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data costCenterResourcesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.observe(ctx, data.Enterprise.ValueString(), data.CostCenterID.ValueString())
	if absent(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read cost center resources", err.Error())
		return
	}
	resp.Diagnostics.Append(data.setObserved(ctx, a)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *costCenterResourcesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, old costCenterResourcesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ent, id := old.Enterprise.ValueString(), old.CostCenterID.ValueString()
	want, ds := data.desired(ctx)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.observe(ctx, ent, id)
	if err != nil {
		resp.Diagnostics.AddError("Read cost center resources", err.Error())
		return
	}
	add, remove, err := planChange(current, want)
	if err == nil {
		err = r.preflight(ctx, ent, id, add) // before any removal
	}
	if err != nil {
		resp.Diagnostics.AddError("Cost center assignment conflict", err.Error())
		return
	}
	if err := r.write(ctx, ent, id, remove, add); err != nil {
		resp.Diagnostics.AddError("Update cost center resources", err.Error())
		if r.refresh(ctx, &old, &resp.Diagnostics) {
			resp.Diagnostics.Append(resp.State.Set(ctx, &old)...)
		}
		return
	}
	data.ID, data.Enterprise = old.ID, old.Enterprise
	if !r.refresh(ctx, &data, &resp.Diagnostics) {
		return // prior state is kept
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *costCenterResourcesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data costCenterResourcesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ent, id := data.Enterprise.ValueString(), data.CostCenterID.ValueString()
	current, err := r.observe(ctx, ent, id)
	if absent(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read cost center resources", err.Error())
		return
	}
	werr := r.client.RemoveCostCenterResources(ctx, ent, id, current)
	after, oerr := r.observe(ctx, ent, id)
	switch {
	case absent(oerr) && (werr == nil || client.IsNotFound(werr)):
		return // parent confirmed gone or archived
	case werr != nil:
		resp.Diagnostics.AddError("Remove cost center resources", werr.Error())
	case oerr != nil:
		resp.Diagnostics.AddError("Verify cost center resources", "Removal was sent but the result could not be verified; state is kept: "+oerr.Error())
		return
	case emptyAssignments(after):
		return
	default:
		resp.Diagnostics.AddError("Cost center resources remain", "GitHub accepted the removal but assignments remain; state now reflects actual membership.")
	}
	if oerr == nil {
		resp.Diagnostics.Append(data.setObserved(ctx, after)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}
func (r *costCenterResourcesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	enterprise, id, err := parseCostCenterImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("enterprise_slug"), enterprise)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cost_center_id"), id)...)
}
