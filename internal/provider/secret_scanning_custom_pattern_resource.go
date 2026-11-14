package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var (
	_ resource.Resource                = &customPatternResource{}
	_ resource.ResourceWithImportState = &customPatternResource{}
	_ resource.ResourceWithModifyPlan  = &customPatternResource{}
)

// defaultPostDeleteAction resolves the pattern's alerts on destroy. GitHub's
// own default is delete_alerts, which removes them irreversibly.
const defaultPostDeleteAction = "resolve_alerts"

type customPatternResource struct{ configuredResource }

type customPatternModel struct {
	ID                    types.String `tfsdk:"id"`
	Enterprise            types.String `tfsdk:"enterprise_slug"`
	PatternID             types.Int64  `tfsdk:"pattern_id"`
	Name                  types.String `tfsdk:"name"`
	Pattern               types.String `tfsdk:"pattern"`
	StartDelimiter        types.String `tfsdk:"start_delimiter"`
	EndDelimiter          types.String `tfsdk:"end_delimiter"`
	MustMatch             types.List   `tfsdk:"must_match"`
	MustNotMatch          types.List   `tfsdk:"must_not_match"`
	Slug                  types.String `tfsdk:"slug"`
	State                 types.String `tfsdk:"state"`
	PushProtectionEnabled types.Bool   `tfsdk:"push_protection_enabled"`
	CustomPatternVersion  types.String `tfsdk:"custom_pattern_version"`
	PostDeleteAction      types.String `tfsdk:"post_delete_action"`
}

func (m *customPatternModel) refresh(ctx context.Context, p *client.CustomPattern) diag.Diagnostics {
	m.ID = types.StringValue(joinID(m.Enterprise.ValueString(), strconv.FormatInt(p.ID, 10)))
	m.PatternID = types.Int64Value(p.ID)
	m.Name = types.StringValue(p.Name)
	m.Pattern = types.StringValue(p.Pattern)
	m.Slug = types.StringValue(p.Slug)
	m.State = types.StringValue(p.State)
	m.PushProtectionEnabled = types.BoolValue(p.PushProtectionEnabled)
	m.CustomPatternVersion = types.StringValue(p.Version)
	m.StartDelimiter = types.StringPointerValue(p.StartDelimiter)
	m.EndDelimiter = types.StringPointerValue(p.EndDelimiter)
	var diags diag.Diagnostics
	must, d := stringListOrNull(ctx, p.MustMatch)
	diags.Append(d...)
	m.MustMatch = must
	mustNot, d := stringListOrNull(ctx, p.MustNotMatch)
	diags.Append(d...)
	m.MustNotMatch = mustNot
	return diags
}

// stringListOrNull keeps an absent API list null and an explicitly emptied one
// as an empty list, so the two stay distinguishable across reads.
func stringListOrNull(ctx context.Context, s []string) (types.List, diag.Diagnostics) {
	if s == nil {
		return types.ListNull(types.StringType), nil
	}
	return types.ListValueFrom(ctx, types.StringType, s)
}

// keepConfigured restores the planned configuration after a refresh from the
// API response: applied state must match the plan for non-computed attributes
// even when the response omits an empty list or echoes a different default.
func (m *customPatternModel) keepConfigured(planned customPatternModel) {
	m.Name = planned.Name
	m.Pattern = planned.Pattern
	m.MustMatch = planned.MustMatch
	m.MustNotMatch = planned.MustNotMatch
	if !planned.StartDelimiter.IsUnknown() {
		m.StartDelimiter = planned.StartDelimiter
	}
	if !planned.EndDelimiter.IsUnknown() {
		m.EndDelimiter = planned.EndDelimiter
	}
}

func (m *customPatternModel) input() client.CustomPatternInput {
	in := client.CustomPatternInput{
		Name:         m.Name.ValueString(),
		Pattern:      m.Pattern.ValueString(),
		MustMatch:    listOf(m.MustMatch),
		MustNotMatch: listOf(m.MustNotMatch),
	}
	if !m.StartDelimiter.IsNull() && !m.StartDelimiter.IsUnknown() {
		v := m.StartDelimiter.ValueString()
		in.StartDelimiter = &v
	}
	if !m.EndDelimiter.IsNull() && !m.EndDelimiter.IsUnknown() {
		v := m.EndDelimiter.ValueString()
		in.EndDelimiter = &v
	}
	return in
}

// customPatternRule only rejects an empty expression: GitHub evaluates patterns
// with Hyperscan, whose accepted syntax Go's regexp would reject as well.
func customPatternRule() validator.String {
	return stringRule{
		"Must be a non-empty regular expression. GitHub evaluates patterns with Hyperscan (a subset of PCRE), so the provider does not validate the syntax; GitHub does.",
		func(s string) bool { return strings.TrimSpace(s) != "" },
	}
}

func (r *customPatternResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_secret_scanning_custom_pattern"
}

func (r *customPatternResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	observed := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.UseStateForUnknown()} }
	resp.Schema = schema.Schema{MarkdownDescription: "Manages a GitHub Enterprise Cloud secret scanning custom pattern. GitHub creates the pattern as `unpublished`; the API cannot publish it, so publish it from the enterprise secret scanning settings in the GitHub UI, where push protection for the pattern is also configured. Requires a token with `admin:enterprise`. Destroying the resource deletes the pattern and, by default, resolves its alerts; `post_delete_action = \"delete_alerts\"` deletes them irreversibly instead. Changing `name` replaces the resource, because GitHub does not allow renaming a pattern.", Attributes: map[string]schema.Attribute{
		"id":                      schema.StringAttribute{Computed: true, MarkdownDescription: "Stable identity: enterprise_slug:pattern_id.", PlanModifiers: observed()},
		"enterprise_slug":         schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Enterprise account slug. Defaults to the provider-level enterprise_slug. Changing it replaces the resource.", Validators: []validator.String{enterpriseRule()}, PlanModifiers: replace()},
		"pattern_id":              schema.Int64Attribute{Computed: true, MarkdownDescription: "Numeric pattern ID assigned by GitHub.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"name":                    schema.StringAttribute{Required: true, MarkdownDescription: "Pattern name. Changing it replaces the resource: GitHub does not allow renaming a custom pattern.", Validators: []validator.String{nameRule()}, PlanModifiers: replace()},
		"pattern":                 schema.StringAttribute{Required: true, MarkdownDescription: "Regular expression that identifies the secret. GitHub evaluates it with Hyperscan (a subset of PCRE), so the provider only requires a non-empty value and leaves the syntax to GitHub. Changing it updates the pattern in place.", Validators: []validator.String{customPatternRule()}},
		"start_delimiter":         schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Delimiter GitHub must place before a match. GitHub applies a default when it is omitted, so the applied value is read back into state. Removing it from configuration does not clear a delimiter GitHub already applied.", PlanModifiers: observed()},
		"end_delimiter":           schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Delimiter GitHub must place after a match. GitHub applies a default when it is omitted, so the applied value is read back into state. Removing it from configuration does not clear a delimiter GitHub already applied.", PlanModifiers: observed()},
		"must_match":              schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Regexes the secret must additionally match. The complete list is sent on every change, including when it is emptied."},
		"must_not_match":          schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Regexes the secret must not match. The complete list is sent on every change, including when it is emptied."},
		"slug":                    schema.StringAttribute{Computed: true, MarkdownDescription: "URL-friendly identifier GitHub derives from the pattern name.", PlanModifiers: observed()},
		"state":                   schema.StringAttribute{Computed: true, MarkdownDescription: "Publish state reported by GitHub. Created patterns arrive as `unpublished`, and the API has no operation to publish them; publish the pattern from the enterprise secret scanning settings in the GitHub UI.", PlanModifiers: observed()},
		"push_protection_enabled": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether push protection is enabled for this pattern. It is configured in the enterprise's secret scanning pattern configurations, not by this resource; GitHub only reports it here.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
		"custom_pattern_version":  schema.StringAttribute{Computed: true, MarkdownDescription: "Version of the pattern used for optimistic concurrency. GitHub requires it on every update and delete and rejects a stale value with HTTP 412. It changes on every write, so it stays unknown in an update plan."},
		"post_delete_action":      schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(defaultPostDeleteAction), MarkdownDescription: "What GitHub does with the pattern's alerts when the resource is destroyed: `resolve_alerts` resolves them (the default), `delete_alerts` deletes them irreversibly. Changing it only updates what Terraform records; the value is sent to GitHub on destroy.", Validators: []validator.String{oneOf("resolve_alerts", "delete_alerts")}},
	}}
}

func (r *customPatternResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	applyEnterpriseSlugDefault(ctx, r.enterpriseSlug, req, resp)
}

func (r *customPatternResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data customPatternModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.CreateCustomPattern(ctx, data.Enterprise.ValueString(), data.input())
	if err != nil {
		resp.Diagnostics.AddError("Create secret scanning custom pattern", err.Error())
		return
	}
	planned := data
	resp.Diagnostics.Append(data.refresh(ctx, p)...)
	data.keepConfigured(planned)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *customPatternResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data customPatternModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.PatternID.IsNull() {
		// Import sets only the composite id; the parts come from parsing it.
		enterprise, patternID, err := parseCustomPatternImportID(data.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid import ID", err.Error())
			return
		}
		data.Enterprise = types.StringValue(enterprise)
		data.PatternID = types.Int64Value(patternID)
	}
	p, err := r.client.GetCustomPattern(ctx, data.Enterprise.ValueString(), data.PatternID.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Read secret scanning custom pattern", err.Error())
		return
	}
	if p == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(data.refresh(ctx, p)...)
	// post_delete_action is not an API field; an imported state records the default.
	if data.PostDeleteAction.IsNull() {
		data.PostDeleteAction = types.StringValue(defaultPostDeleteAction)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// rawJSON renders a Go value as a raw patch field. Marshaling strings and
// string slices cannot fail.
func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// patchOptionalString sends a changed delimiter; a removed value travels as an
// explicit null so GitHub clears it instead of keeping the previous one.
func patchOptionalString(patch map[string]json.RawMessage, key string, planned, prior types.String) {
	if planned.IsUnknown() || planned.Equal(prior) {
		return
	}
	if planned.IsNull() {
		patch[key] = json.RawMessage("null")
		return
	}
	patch[key] = rawJSON(planned.ValueString())
}

func (r *customPatternResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, old customPatternModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Only attributes that changed are sent. GitHub decides whether to accept
	// the emptied lists and cleared delimiters; a rejection is an apply error,
	// never silent drift.
	patch := map[string]json.RawMessage{}
	if !data.Pattern.Equal(old.Pattern) {
		patch["pattern"] = rawJSON(data.Pattern.ValueString())
	}
	patchOptionalString(patch, "start_delimiter", data.StartDelimiter, old.StartDelimiter)
	patchOptionalString(patch, "end_delimiter", data.EndDelimiter, old.EndDelimiter)
	if !data.MustMatch.Equal(old.MustMatch) {
		patch["must_match"] = rawJSON(listOf(data.MustMatch))
	}
	if !data.MustNotMatch.Equal(old.MustNotMatch) {
		patch["must_not_match"] = rawJSON(listOf(data.MustNotMatch))
	}
	if len(patch) == 0 {
		// Nothing GitHub accepts changed (for example only post_delete_action):
		// record the plan without calling the API. The version is unknown in
		// the plan because it changes on every write, but no write happened.
		data.CustomPatternVersion = old.CustomPatternVersion
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}
	p, err := r.client.UpdateCustomPattern(ctx, old.Enterprise.ValueString(), old.PatternID.ValueInt64(), old.CustomPatternVersion.ValueString(), patch)
	if err != nil {
		resp.Diagnostics.AddError("Update secret scanning custom pattern", err.Error())
		return
	}
	planned := data
	resp.Diagnostics.Append(data.refresh(ctx, p)...)
	data.keepConfigured(planned)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *customPatternResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data customPatternModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteCustomPattern(ctx, data.Enterprise.ValueString(), data.PatternID.ValueInt64(), data.CustomPatternVersion.ValueString(), data.PostDeleteAction.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Delete secret scanning custom pattern", err.Error())
	}
}

func (r *customPatternResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func parseCustomPatternImportID(id string) (enterprise string, patternID int64, err error) {
	parts := strings.Split(id, idSeparator)
	if len(parts) != 2 || !enterpriseSlugPattern.MatchString(parts[0]) {
		return "", 0, fmt.Errorf("expected enterprise_slug:pattern_id")
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != parts[1] {
		return "", 0, fmt.Errorf("pattern_id must be a positive integer in canonical decimal format")
	}
	return parts[0], n, nil
}
