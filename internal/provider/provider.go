package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
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
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &enterpriseProvider{version: version} }
}

func (p *enterpriseProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName, resp.Version = providerName, p.version
}

func (p *enterpriseProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages GitHub Enterprise Cloud teams alongside integrations/github. Set `provider = github-enterprise` on every resource and data source to select this provider explicitly.", Attributes: map[string]schema.Attribute{
		"token":       schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Classic PAT or enterprise GitHub App access token. Defaults to GITHUB_ENTERPRISE_TOKEN, then GITHUB_TOKEN. Prefer environment variables to keep credentials out of configuration."},
		"base_url":    schema.StringAttribute{Optional: true, MarkdownDescription: "API base URL. Defaults to GITHUB_ENTERPRISE_BASE_URL, then https://api.github.com/. Supports dedicated api.SUBDOMAIN.ghe.com endpoints."},
		"api_version": schema.StringAttribute{Optional: true, MarkdownDescription: "GitHub API version. Defaults to 2026-03-10."},
	}}
}

func (p *enterpriseProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.Token.IsUnknown() || data.BaseURL.IsUnknown() || data.APIVersion.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration", "token, base_url and api_version must be known before configuring the provider.")
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
	c, err := client.New(base, token, version)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	resp.ResourceData, resp.DataSourceData = c, c
}

func (p *enterpriseProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &teamResource{} }, func() resource.Resource { return &membershipResource{} }, func() resource.Resource { return &organizationsResource{} }}
}
func (p *enterpriseProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{func() datasource.DataSource { return &teamDataSource{} }, func() datasource.DataSource { return &teamsDataSource{} }, func() datasource.DataSource { return &membershipDataSource{} }, func() datasource.DataSource { return &organizationsDataSource{} }}
}

type configuredResource struct{ client *client.Client }

func (r *configuredResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

type configuredDataSource struct{ client *client.Client }

func (r *configuredDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}
