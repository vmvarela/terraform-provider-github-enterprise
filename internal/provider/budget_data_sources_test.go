package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

const budgetFixture = `{"id":"b1","budget_scope":"user","user":"octocat","budget_type":"BundlePricing",
"budget_product_sku":"ai_credits","budget_amount":30,"prevent_further_usage":true,
"budget_alerting":{"will_alert":true,"alert_recipients":["octocat"]},
"expires_at":"2027-01-01","consumed_amount":12.5}`

func readBudgetDataSource(t *testing.T, ds datasource.DataSource, model any) (tfsdk.State, datasource.ReadResponse) {
	t.Helper()
	ctx := context.Background()
	var sr datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &sr)
	input := tfsdk.State{Schema: sr.Schema}
	if d := input.Set(ctx, model); d.HasError() {
		t.Fatal(d)
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: input.Raw}}, &resp)
	return resp.State, resp
}

func budgetTestServer(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "mock-token", client.DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBudgetDataSourceReadsConsumption(t *testing.T) {
	var path string
	c := budgetTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, budgetFixture)
	})
	d := &budgetDataSource{configuredDataSource{client: c}}
	state, resp := readBudgetDataSource(t, d, budgetDataModel{
		Enterprise: types.StringValue("acme"), BudgetID: types.StringValue("b1"),
		ID: types.StringNull(), Scope: types.StringNull(), EntityName: types.StringNull(), User: types.StringNull(),
		Amount: types.Int64Null(), PreventFurtherUsage: types.BoolNull(), Type: types.StringNull(), SKU: types.StringNull(),
		AlertsEnabled: types.BoolNull(), AlertRecipients: types.ListNull(types.StringType), ExpiresAt: types.StringNull(),
		ConsumedAmount: types.Float64Null(),
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got budgetDataModel
	state.Get(context.Background(), &got)
	if path != "/enterprises/acme/settings/billing/budgets/b1" {
		t.Errorf("path = %s", path)
	}
	if got.ID.ValueString() != "acme:b1" || got.ConsumedAmount.ValueFloat64() != 12.5 || got.Amount.ValueInt64() != 30 || got.User.ValueString() != "octocat" {
		t.Errorf("decoded wrong: %+v", got)
	}
	if got.AlertRecipients.IsNull() || got.AlertRecipients.String() != `["octocat"]` {
		t.Errorf("recipients = %v", got.AlertRecipients)
	}
}

func TestBudgetsDataSourceSendsFiltersAndLists(t *testing.T) {
	var query string
	c := budgetTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprintf(w, `{"budgets":[%s,%s],"has_next_page":false,"total_count":2}`, budgetFixture, budgetFixture)
	})
	d := &budgetsDataSource{configuredDataSource{client: c}}
	state, resp := readBudgetDataSource(t, d, budgetsDataModel{
		Enterprise: types.StringValue("acme"), Scope: types.StringValue("user"), User: types.StringValue("octocat"),
		ID: types.StringNull(), Budgets: types.ListNull(types.ObjectType{AttrTypes: budgetTypes_}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got budgetsDataModel
	state.Get(context.Background(), &got)
	for _, want := range []string{"scope=user", "user=octocat", "per_page=100"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q missing %q", query, want)
		}
	}
	if got.ID.ValueString() != "acme:budgets?scope=user?user=octocat" || !strings.Contains(got.Budgets.String(), "12.5") {
		t.Errorf("id=%s budgets=%s", got.ID, got.Budgets)
	}
}

func TestBudgetsDataSourceEmptyListIsNotNull(t *testing.T) {
	c := budgetTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"budgets":[],"has_next_page":false,"total_count":0}`)
	})
	d := &budgetsDataSource{configuredDataSource{client: c}}
	state, resp := readBudgetDataSource(t, d, budgetsDataModel{
		Enterprise: types.StringValue("acme"), Scope: types.StringNull(), User: types.StringNull(),
		ID: types.StringNull(), Budgets: types.ListNull(types.ObjectType{AttrTypes: budgetTypes_}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got budgetsDataModel
	state.Get(context.Background(), &got)
	if got.Budgets.IsNull() || got.Budgets.String() != "[]" {
		t.Errorf("budgets = %v", got.Budgets)
	}
}
