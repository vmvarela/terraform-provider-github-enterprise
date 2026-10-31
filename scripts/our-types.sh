#!/usr/bin/env bash
# Prints the resource and data source types this provider declares, one per line.
# Shared by the upstream checks so both read the same source of truth.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

grep -rhoE 'resourceTypePrefix \+ "_[a-z_]+"' "$repo_root/internal/provider" |
	sed -E 's/resourceTypePrefix \+ "_(.*)"/github_enterprise_\1/' |
	sort -u
