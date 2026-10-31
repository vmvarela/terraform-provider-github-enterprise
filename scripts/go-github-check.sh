#!/usr/bin/env bash
# Checks whether the go-github release we depend on has moved, whether the
# provider still builds and tests against the newest patch of that major, and
# which enterprise API methods appeared since the pinned version.
#
# Usage: scripts/go-github-check.sh
#
# Exit status: 0 when nothing needs attention; 1 when the provider no longer
# builds, a new major exists, or new enterprise methods appeared.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

module_line="$(grep -oE 'github.com/google/go-github/v[0-9]+ v[0-9]+\.[0-9]+\.[0-9]+' "$repo_root/go.mod" | head -1)"
pinned_major="$(echo "$module_line" | grep -oE 'v[0-9]+ ' | tr -d ' v')"
pinned_version="$(echo "$module_line" | awk '{print $2}')"
echo "go-github-check: pinned v${pinned_major} ${pinned_version}"

# The module proxy needs no token and is the authoritative source for what we
# can actually depend on; probing the next majors detects a major bump without
# depending on an authenticated API call.
proxy_latest() { # major
	curl -fsS "https://proxy.golang.org/github.com/google/go-github/v$1/@latest" 2>/dev/null |
		sed -n 's/.*"Version": *"\([^"]*\)".*/\1/p' || true
}

latest_same_major="$(proxy_latest "$pinned_major")"
newer_major=""
for candidate in $((pinned_major + 1)) $((pinned_major + 2)) $((pinned_major + 3)); do
	version="$(proxy_latest "$candidate")"
	if [[ -n "$version" ]]; then
		newer_major="$candidate"
		break
	fi
done
echo "go-github-check: newest v${pinned_major} ${latest_same_major:-unknown}${newer_major:+, newer major v${newer_major}}"

status=0
if [[ -n "$newer_major" ]]; then
	echo "go-github-check: MAJOR BUMP: go-github v${newer_major} is out; import paths change, so this needs a migration"
	status=1
fi

# 1. Does the provider still build and pass its tests against the newest patch?
if [[ -n "$latest_same_major" && "$latest_same_major" != "$pinned_version" ]]; then
	mkdir "$work/src"
	(cd "$repo_root" && tar -cf - --exclude .git --exclude bin .) | (cd "$work/src" && tar -xf -)
	if (cd "$work/src" && go get "github.com/google/go-github/v${pinned_major}@${latest_same_major}" >/dev/null 2>&1 &&
		go build ./... && go test ./... >"$work/test.log" 2>&1); then
		echo "go-github-check: builds and passes its tests with ${latest_same_major}"
	else
		echo "go-github-check: FAILS with ${latest_same_major}; see the log below"
		tail -30 "$work/test.log" 2>/dev/null || true
		status=1
	fi
else
	echo "go-github-check: already on the newest patch, no build check needed"
fi

# 2. Which enterprise API methods appeared since the pinned version?
fetch_zip() { # major version
	curl -fsSL "https://proxy.golang.org/github.com/google/go-github/v${1}/@v/${2}.zip" -o "$work/${2}.zip"
	mkdir "$work/${2}"
	unzip -q "$work/${2}.zip" -d "$work/${2}"
	echo "$work/${2}"
}

enterprise_api() { # extracted module dir
	python3 - "$1" <<'PY'
import pathlib, re, sys

root = pathlib.Path(sys.argv[1])
methods = set()
for path in root.rglob("github/*.go"):
    if path.name.endswith("_test.go"):
        continue
    text = path.read_text(errors="replace")
    if "enterprises/" not in text:
        continue
    for receiver, name in re.findall(r"^func \(\w+ \*(\w+)\) (\w+)\(", text, re.M):
        methods.add(f"{receiver}.{name}")
print("\n".join(sorted(methods)))
PY
}

if [[ -n "$latest_same_major" && "$latest_same_major" != "$pinned_version" ]]; then
	fetch_zip "$pinned_major" "$pinned_version" >/dev/null
	fetch_zip "$pinned_major" "$latest_same_major" >/dev/null
	enterprise_api "$work/${pinned_version}" >"$work/before.txt"
	enterprise_api "$work/${latest_same_major}" >"$work/after.txt"
	added="$(comm -13 "$work/before.txt" "$work/after.txt")"
	if [[ -n "$added" ]]; then
		echo "go-github-check: enterprise API methods added since ${pinned_version}:"
		echo "$added" | sed 's/^/  /'
		status=1
	fi
fi

# 3. Which enterprise SDK methods does this provider never call? Informational:
# it is the backlog of enterprise surface we have not implemented yet.
if [[ -n "$latest_same_major" ]]; then
	if [[ ! -d "$work/${latest_same_major}" ]]; then
		fetch_zip "$pinned_major" "$latest_same_major" >/dev/null
	fi
	enterprise_api "$work/${latest_same_major}" >"$work/current.txt"
	: >"$work/unused.txt"
	while read -r method; do
		name="${method#*.}"
		if ! grep -rqF ".${name}(" "$repo_root/internal"; then
			echo "$method" >>"$work/unused.txt"
		fi
	done <"$work/current.txt"
	echo "go-github-check: $(wc -l <"$work/unused.txt" | tr -d ' ') enterprise SDK methods are not called by this provider (informational)"
	sed 's/^/  /' "$work/unused.txt"
fi

exit "$status"
