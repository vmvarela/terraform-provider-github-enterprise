package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

type enterpriseProvider struct{ version string }

// Provider local names cannot contain underscores. Keep the readable resource
// prefix independent; configurations select this provider explicitly.
const (
	providerName       = "github-enterprise"
	resourceTypePrefix = "github_enterprise"
)

type providerModel struct {
	Token      types.String `tfsdk:"token"`
	BaseURL    types.String `tfsdk:"base_url"`
	APIVersion types.String `tfsdk:"api_version"`
	Enterprise types.String `tfsdk:"enterprise_slug"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &enterpriseProvider{version: version} }
}

func (p *enterpriseProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName, resp.Version = providerName, p.version
}

func (p *enterpriseProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages GitHub Enterprise Cloud teams and cost centers alongside integrations/github. Set `provider = github-enterprise` on every resource and data source to select this provider explicitly.", Attributes: map[string]schema.Attribute{
		"token":       schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Classic PAT or enterprise GitHub App access token. Cost centers require manage_billing:enterprise for a classic PAT; App Enterprise billing permission is not generally available, so verify availability and required operations before using an App. Defaults to GITHUB_ENTERPRISE_TOKEN, then GITHUB_TOKEN. Prefer environment variables to keep credentials out of configuration."},
		"base_url":    schema.StringAttribute{Optional: true, MarkdownDescription: "API base URL. Defaults to GITHUB_ENTERPRISE_BASE_URL, then https://api.github.com/. Supports dedicated api.SUBDOMAIN.ghe.com endpoints."},
		"api_version": schema.StringAttribute{Optional: true, MarkdownDescription: "GitHub API version. Defaults to 2026-03-10."},
		"enterprise_slug": schema.StringAttribute{Optional: true, MarkdownDescription: "Enterprise account slug. Defaults to GITHUB_ENTERPRISE_SLUG and is inherited by every resource and data source that omits enterprise_slug. A value set on the block wins over this one.",
			Validators: []validator.String{enterpriseRule()}},
	}}
}

func (p *enterpriseProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.Token.IsUnknown() || data.BaseURL.IsUnknown() || data.APIVersion.IsUnknown() || data.Enterprise.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration", "token, base_url, api_version and enterprise_slug must be known before configuring the provider.")
		return
	}
	token := data.Token.ValueString()
	if data.Token.IsNull() {
		token = os.Getenv("GITHUB_ENTERPRISE_TOKEN")
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
	}
	base := data.BaseURL.ValueString()
	if data.BaseURL.IsNull() {
		base = os.Getenv("GITHUB_ENTERPRISE_BASE_URL")
		if base == "" {
			base = "https://api.github.com/"
		}
	}
	version := data.APIVersion.ValueString()
	if data.APIVersion.IsNull() {
		version = client.DefaultAPIVersion
	}
	enterprise := data.Enterprise.ValueString()
	if data.Enterprise.IsNull() {
		enterprise = os.Getenv("GITHUB_ENTERPRISE_SLUG")
	}
	if enterprise != "" && !enterpriseSlugPattern.MatchString(enterprise) {
		resp.Diagnostics.AddError("Invalid provider configuration", "enterprise_slug must be a lowercase enterprise slug containing letters, digits and hyphens.")
		return
	}
	c, err := client.New(base, token, version)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	// A single value carries both the client and the slug inherited by
	// resources and data sources, so neither reads the environment again.
	configured := &providerData{client: c, enterpriseSlug: enterprise}
	resp.ResourceData, resp.DataSourceData = configured, configured
}

func (p *enterpriseProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &teamResource{} }, func() resource.Resource { return &membershipResource{} }, func() resource.Resource { return &organizationsResource{} }, func() resource.Resource { return &costCenterResource{} }, func() resource.Resource { return &costCenterResourcesResource{} }, func() resource.Resource { return &budgetResource{} }}
}
func (p *enterpriseProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &teamDataSource{} }, func() datasource.DataSource { return &teamsDataSource{} }, func() datasource.DataSource { return &membershipDataSource{} }, func() datasource.DataSource { return &organizationsDataSource{} }, func() datasource.DataSource { return &costCenterDataSource{} }, func() datasource.DataSource { return &costCentersDataSource{} }, func() datasource.DataSource { return &scimGroupDataSource{} }, func() datasource.DataSource { return &scimGroupsDataSource{} }, func() datasource.DataSource { return &scimUserDataSource{} }, func() datasource.DataSource { return &scimUsersDataSource{} }, func() datasource.DataSource { return &budgetDataSource{} }, func() datasource.DataSource { return &budgetsDataSource{} }}
}

type providerData struct {
	client         *client.Client
	enterpriseSlug string
}

type configuredResource struct {
	client *client.Client
	// enterpriseSlug is the provider-level default for resources that omit it.
	enterpriseSlug string
}

func (r *configuredResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", fmt.Sprintf("Expected *providerData, got %T", req.ProviderData))
		return
	}
	r.client, r.enterpriseSlug = c.client, c.enterpriseSlug
}

type configuredDataSource struct {
	client         *client.Client
	enterpriseSlug string
}

func (r *configuredDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", fmt.Sprintf("Expected *providerData, got %T", req.ProviderData))
		return
	}
	r.client, r.enterpriseSlug = c.client, c.enterpriseSlug
}
