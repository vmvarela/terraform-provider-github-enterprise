#!/usr/bin/env bash
# Fails when this provider declares a resource or data source type that the
# official github provider also declares.
#
# Why not just run `terraform init` with both providers: init succeeds even when
# the types collide, because Terraform has no cross-provider type registry. The
# type is bound to a provider by its local name (github), so a duplicate type
# silently lands on the official provider and fails later with a confusing
# schema error. Comparing the declared schemas catches it before release.
#
# Usage: scripts/upstream-check.sh [official-provider-version]
# Without an argument it uses the newest release the registry serves.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
official_version="${1:-}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if [[ -n "$official_version" ]]; then
	version_line="      version = \"= ${official_version}\""
else
	version_line=""
fi

cat >"$work/main.tf" <<HCL
terraform {
  required_providers {
    github = {
      source  = "integrations/github"
${version_line}
    }
  }
}
HCL

if ! (cd "$work" && terraform init -input=false -no-color >init.log 2>&1); then
	echo "upstream-check: terraform init failed for the official provider" >&2
	sed -n '1,20p' "$work/init.log" >&2
	exit 2
fi

official="$(sed -n 's/^ *version *= *"\(.*\)"/\1/p' "$work/.terraform.lock.hcl" | head -1)"
echo "upstream-check: official integrations/github ${official:-unknown}"

(cd "$work" && terraform providers schema -json >schema.json)

# Our types are read from the source, so the check also covers a working tree
# that has not been released yet.
"$repo_root/scripts/our-types.sh" >"$work/ours.txt"

python3 - "$work/schema.json" "$work/ours.txt" <<'PY'
import json, sys

schema_path, ours_path = sys.argv[1], sys.argv[2]
with open(schema_path) as f:
    data = json.load(f)

official = set()
for provider, schemas in data.get("provider_schemas", {}).items():
    if not provider.endswith("/integrations/github"):
        continue
    official |= set(schemas.get("resource_schemas") or {})
    official |= set(schemas.get("data_source_schemas") or {})

with open(ours_path) as f:
    ours = {line.strip() for line in f if line.strip()}

collisions = sorted(ours & official)
if collisions:
    print("upstream-check: the official provider already declares these types:")
    for name in collisions:
        print("  " + name)
    sys.exit(1)

print(f"upstream-check: ok, {len(ours)} types checked against {len(official)} official types, no collision")
PY
