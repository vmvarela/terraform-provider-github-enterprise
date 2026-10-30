# Complete example

This example uses all three resources and four data sources alongside the official
`integrations/github` enterprise lookup. Each block explicitly selects its provider,
and the enterprise is set once on the `github-enterprise` provider block. It pins the
published `vmvarela/github-enterprise` 0.4 line and `integrations/github` 6.13.0.

Set `GITHUB_TOKEN` for the official provider and `GITHUB_ENTERPRISE_TOKEN` for this
provider, then supply `enterprise_slug`, `username`, and `organization_slugs`.
Review the plan before applying: this example creates a team, adds a direct member,
and authoritatively manages its assignments. Use a team without an IdP group for
direct membership management.

For an existing team, import rather than creating a second binding:

```sh
terraform import github_enterprise_team.platform my-enterprise/123456
terraform import github_enterprise_team_membership.member my-enterprise/123456/octocat
terraform import github_enterprise_team_organizations.platform my-enterprise/123456
```

Run the imports with configuration matching the existing objects. Review the next
plan, especially the complete organization assignment set, before applying.
