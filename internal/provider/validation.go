package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

type stringRule struct {
	description string
	valid       func(string) bool
}

func (r stringRule) Description(context.Context) string             { return r.description }
func (r stringRule) MarkdownDescription(ctx context.Context) string { return r.Description(ctx) }
func (r stringRule) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !r.valid(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", r.description)
	}
}
func patternRule(pattern, description string) validator.String {
	re := regexp.MustCompile(pattern)
	return stringRule{description, re.MatchString}
}
func oneOf(values ...string) validator.String {
	return stringRule{"Must be one of: " + strings.Join(values, ", "), func(s string) bool {
		for _, v := range values {
			if s == v {
				return true
			}
		}
		return false
	}}
}
func nameRule() validator.String {
	return stringRule{"Must contain 1 to 255 characters and cannot be blank.", func(s string) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= 255 }}
}
func enterpriseRule() validator.String {
	return patternRule(`^[a-z0-9][a-z0-9-]*$`, "Use a lowercase enterprise slug containing letters, digits and hyphens.")
}
func usernameRule() validator.String {
	return patternRule(`^[A-Za-z0-9][A-Za-z0-9_-]*$`, "Use a GitHub username, including an enterprise managed username when applicable.")
}
func slugRule() validator.String {
	return patternRule(`^(ent:)?[a-z0-9][a-z0-9-]*$`, "Use the team slug returned by GitHub, including its ent: prefix when present.")
}

type positiveID struct{}

func (positiveID) Description(context.Context) string               { return "Must be a positive numeric team ID." }
func (v positiveID) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v positiveID) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && req.ConfigValue.ValueInt64() <= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid team ID", v.Description(ctx))
	}
}

func parseImportID(id string, count int) ([]string, error) {
	parts := strings.Split(id, "/")
	if len(parts) != count {
		return nil, fmt.Errorf("expected enterprise_slug/team_id%s", map[bool]string{true: "/username", false: ""}[count == 3])
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(parts[0]) {
		return nil, fmt.Errorf("invalid enterprise slug")
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != parts[1] {
		return nil, fmt.Errorf("team_id must be a positive integer in canonical decimal format")
	}
	if count == 3 && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`).MatchString(parts[2]) {
		return nil, fmt.Errorf("invalid username")
	}
	return parts, nil
}
