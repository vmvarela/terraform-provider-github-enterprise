---
page_title: "Quickstart"
description: |-
  Create an enterprise team, add a member and assign organizations in a few minutes.
---

# Quickstart

This provider manages GitHub Enterprise Cloud objects that `integrations/github`
does not: enterprise teams, their members and organization assignments, read-only
SCIM lookups, cost centers and billing budgets. Both providers can live in the same
configuration.

## 1. Authenticate

Use a classic personal access token from an enterprise owner, exported rather than
written into configuration:

```sh
export GITHUB_ENTERPRISE_TOKEN=ghp_...
export GITHUB_ENTERPRISE_SLUG=my-enterprise
```

Teams and memberships need `admin:enterprise` and `admin:org`. Cost centers and
budgets need `manage_billing:enterprise` and an enterprise owner or billing manager;
organization and repository budgets also accept an organization administrator.
Fine-grained tokens cannot call these endpoints.

## 2. Configure the provider

```hcl
terraform {
  required_providers {
    github-enterprise = {
      source  = "vmvarela/github-enterprise"
      version = "~> 0.4"
    }
  }
}

provider "github-enterprise" {
  # Optional: both values fall back to the environment variables above.
  enterprise_slug = "my-enterprise"
}
```

The official provider also declares `github_*` types, so **every resource and data
source states `provider = github-enterprise`**. Forgetting it is the most common
first error: Terraform reports that provider `github` does not support the type.

## 3. Create a team and add a member

```hcl
resource "github_enterprise_team" "platform" {
  provider = github-enterprise

  name                        = "Platform"
  description                 = "Platform Engineering"
  organization_selection_type = "selected"
}

resource "github_enterprise_team_membership" "ana" {
  provider = github-enterprise

  team_id  = github_enterprise_team.platform.team_id
  username = "ana"
}
```

`team_id` is the numeric ID and stays the same across renames, so reference it
instead of the slug. `organization_selection_type = "selected"` is what allows the
next step; leave it out and the team keeps no organization access.

## 4. Assign organizations

```hcl
resource "github_enterprise_team_organizations" "platform" {
  provider = github-enterprise

  team_id            = github_enterprise_team.platform.team_id
  organization_slugs = ["platform-org", "tools-org"]
}
```

This resource is authoritative: an organization missing from the list is removed.
Keep one instance per team, and import existing assignments before the first apply.

## 5. Link an IdP group

For a team managed by your identity provider, set `group_id` to the SCIM group ID.
Look it up instead of copying a UUID:

```hcl
data "github_enterprise_scim_groups" "platform" {
  provider = github-enterprise

  filter = "displayName eq \"Platform\""
}

resource "github_enterprise_team" "platform" {
  provider = github-enterprise

  name                        = "Platform"
  group_id                    = data.github_enterprise_scim_groups.platform.groups[0].scim_group_id
  organization_selection_type = "selected"
}
```

A team linked to an IdP group is provisioned by the IdP: manage its memberships
there, not with `github_enterprise_team_membership`. Setting `group_id = ""`
unlinks the group.

## 6. Read what already exists

```hcl
data "github_enterprise_team" "platform" {
  provider = github-enterprise

  slug = "platform"
}
```

Data sources never truncate a list; they page through every result. A single lookup
that returns 404 fails the plan instead of reporting an empty object.

## Import instead of recreating

```sh
terraform import github_enterprise_team.platform my-enterprise:123456
terraform import github_enterprise_team_membership.ana my-enterprise:123456:ana
terraform import github_enterprise_team_organizations.platform my-enterprise:123456
terraform import github_enterprise_cost_center.infra my-enterprise:<cost-center-id>
```

Every ID is `<enterprise_slug>:<object>`, the same separator the official `github` provider uses, so import strings transfer between them. Import with configuration that matches the
existing object, then review the plan before applying.

## When something fails

- **HTTP 404** means the object is missing *or* the token cannot see it. GitHub hides
  permission failures this way, so check the token scopes and the enterprise role
  before assuming the object is gone.
- **HTTP 403** usually means a missing enterprise scope or an account that is not an
  enterprise owner.
- **"provider github does not support resource type"** means the block is missing
  `provider = github-enterprise`.
- **A plan that wants to remove organizations** means another instance manages the
  same team's assignments, or the import did not run.

Diagnostics never include response bodies or headers, so a token cannot leak through
an error message.

See the [compatibility contract](initial-contract.md) for ownership rules, drift
handling and API limitations.
