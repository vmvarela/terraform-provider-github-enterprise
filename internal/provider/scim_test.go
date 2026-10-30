package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestSCIMIDValidation(t *testing.T) {
	for _, id := range []string{"", ".", "..", "AbC/1", "opaque:Mixed-Case", "123"} {
		var resp validator.StringResponse
		scimIDRule().ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringValue(id)}, &resp)
		invalid := id == "" || id == "." || id == ".."
		if resp.Diagnostics.HasError() != invalid {
			t.Errorf("ID %q: diagnostics %v", id, resp.Diagnostics)
		}
	}
}

func readSCIMDataSource(t *testing.T, ds datasource.DataSource, model any) (tfsdk.State, bool) {
	t.Helper()
	ctx := context.Background()
	var schemaResponse datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResponse)
	input := tfsdk.State{Schema: schemaResponse.Schema}
	if d := input.Set(ctx, model); d.HasError() {
		t.Fatal(d)
	}
	response := datasource.ReadResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResponse.Schema, Raw: input.Raw}}, &response)
	if response.Diagnostics.HasError() {
		t.Log(response.Diagnostics)
	}
	return response.State, !response.Diagnostics.HasError()
}

func scimJSONHandler(t *testing.T, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s", r.Method)
		}
		fmt.Fprint(w, body)
	}
}

func TestSCIMGroupPreservesNullOptionals(t *testing.T) {
	c := lookupClient(t, scimJSONHandler(t, `{"id":"grp-9f8b"}`))
	state, ok := readSCIMDataSource(t, &scimGroupDataSource{configuredDataSource{client: c, enterpriseSlug: "acme"}}, scimGroupDataModel{SCIMGroupID: types.StringValue("grp-9f8b")})
	if !ok {
		t.Fatal("read failed")
	}
	var model scimGroupDataModel
	if d := state.Get(context.Background(), &model); d.HasError() {
		t.Fatal(d)
	}
	if !model.ExternalID.IsNull() || !model.DisplayName.IsNull() {
		t.Fatalf("missing optional properties must stay null: %v %v", model.ExternalID, model.DisplayName)
	}
	if model.ID.ValueString() != "acme/grp-9f8b" {
		t.Fatalf("unexpected composite id %q", model.ID.ValueString())
	}
}

func TestSCIMUserPreservesExplicitInactive(t *testing.T) {
	c := lookupClient(t, scimJSONHandler(t, `{"id":"usr-4e5d","active":false}`))
	state, ok := readSCIMDataSource(t, &scimUserDataSource{configuredDataSource{client: c, enterpriseSlug: "acme"}}, scimUserDataModel{SCIMUserID: types.StringValue("usr-4e5d")})
	if !ok {
		t.Fatal("read failed")
	}
	var model scimUserDataModel
	if d := state.Get(context.Background(), &model); d.HasError() {
		t.Fatal(d)
	}
	if model.Active.IsNull() || model.Active.ValueBool() {
		t.Fatalf("explicit false must be preserved: %v", model.Active)
	}
	if !model.UserName.IsNull() {
		t.Fatalf("missing userName must stay null: %v", model.UserName)
	}
}

func TestSCIMReadErrorsAreNotReportedAsMissingData(t *testing.T) {
	for _, list := range []bool{false, true} {
		t.Run(fmt.Sprintf("list=%t", list), func(t *testing.T) {
			c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "denied", http.StatusForbidden)
			})
			var ds datasource.DataSource
			var model any
			if list {
				ds, model = &scimGroupsDataSource{configuredDataSource{client: c, enterpriseSlug: "acme"}}, scimGroupsDataModel{Groups: types.ListNull(types.ObjectType{AttrTypes: scimGroupTypes}), Filter: types.StringNull()}
			} else {
				ds, model = &scimGroupDataSource{configuredDataSource{client: c, enterpriseSlug: "acme"}}, scimGroupDataModel{SCIMGroupID: types.StringValue("grp-9f8b")}
			}
			_, ok := readSCIMDataSource(t, ds, model)
			if ok {
				t.Fatal("permission failures must not be reported as data")
			}
		})
	}
}

func TestSCIMListsPreserveEmptyAndEscapedFilterIdentity(t *testing.T) {
	filter := `displayName eq "Platform & Ops"`
	var received string
	c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
		received = r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:ListResponse"],"totalResults":0,"startIndex":1,"itemsPerPage":0,"Resources":[]}`)
	})
	state, ok := readSCIMDataSource(t, &scimGroupsDataSource{configuredDataSource{client: c, enterpriseSlug: "acme"}}, scimGroupsDataModel{Filter: types.StringValue(filter), Groups: types.ListNull(types.ObjectType{AttrTypes: scimGroupTypes})})
	if !ok {
		t.Fatal("read failed")
	}
	if received != filter {
		t.Fatalf("filter was not passed unchanged: %q", received)
	}
	var model scimGroupsDataModel
	if d := state.Get(context.Background(), &model); d.HasError() {
		t.Fatal(d)
	}
	if model.Groups.IsNull() || len(model.Groups.Elements()) != 0 {
		t.Fatalf("empty matches must be an empty list: %v", model.Groups)
	}
	want := "acme/groups?filter=" + url.QueryEscape(filter)
	if model.ID.ValueString() != want {
		t.Fatalf("query id %q, want %q", model.ID.ValueString(), want)
	}
}

func TestSCIMEnterpriseInheritsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured types.String
		want       string
	}{
		{"inherited", types.StringNull(), "acme"},
		{"override", types.StringValue("other"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields []string
			c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
				fields = strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				fmt.Fprint(w, `{"id":"grp-9f8b"}`)
			})
			state, ok := readSCIMDataSource(t, &scimGroupDataSource{configuredDataSource{client: c, enterpriseSlug: "acme"}}, scimGroupDataModel{Enterprise: tc.configured, SCIMGroupID: types.StringValue("grp-9f8b")})
			if !ok {
				t.Fatal("read failed")
			}
			if !strings.Contains(strings.Join(fields, "/"), "/enterprises/"+tc.want+"/") {
				t.Fatalf("request did not use %q: %v", tc.want, fields)
			}
			var model scimGroupDataModel
			if d := state.Get(context.Background(), &model); d.HasError() {
				t.Fatal(d)
			}
			if model.Enterprise.ValueString() != tc.want || !strings.HasPrefix(model.ID.ValueString(), tc.want+"/") {
				t.Fatalf("unexpected scope %q / %q", model.Enterprise, model.ID)
			}
		})
	}
}
