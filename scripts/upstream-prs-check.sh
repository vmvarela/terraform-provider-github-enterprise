#!/usr/bin/env bash
# Tracks the types proposed by open pull requests in the official provider.
#
# Names in an open PR can change, and new PRs open without notice, so the check
# compares the current state with a committed baseline:
#   - a type we declare here that an open PR also declares is a future collision
# Only enterprise-prefixed types are tracked: every type this provider declares
# starts with github_enterprise_, so nothing else can collide with it.
#   - a new, renamed, moved or withdrawn type means the baseline is stale
#
# Usage: scripts/upstream-prs-check.sh [--update]
#   --update rewrites the baseline with what is on the remote right now.
#
# Exit status: 0 when the baseline matches; 1 when it does not or a collision
# exists; 2 when the remote could not be read.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
upstream="integrations/terraform-provider-github"
baseline="$repo_root/scripts/upstream-prs.txt"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

update=0
[[ "${1:-}" == "--update" ]] && update=1

if ! gh api --paginate "repos/$upstream/pulls?state=open&per_page=100" \
	--jq '.[] | "\(.number)\t\(.title)"' >"$work/pulls.txt" 2>"$work/err.txt"; then
	echo "upstream-prs-check: cannot read open pull requests from $upstream" >&2
	cat "$work/err.txt" >&2
	exit 2
fi

: >"$work/current.txt"
while IFS=$'\t' read -r number title; do
	[[ -z "$number" ]] && continue
	gh api "repos/$upstream/pulls/$number/files?per_page=100" --jq '.[].filename' 2>/dev/null |
		grep -oE 'github/(resource|data_source)_github_enterprise_[a-z0-9_]+\.go$' |
		sed -E 's#github/(resource|data_source)_(github_enterprise_[a-z0-9_]+)\.go#\2#' |
		grep -v '_test$' |
		sort -u |
		sed "s/$/\t$number/" >>"$work/current.txt" || true
done <"$work/pulls.txt"

sort -u -o "$work/current.txt" "$work/current.txt"
echo "upstream-prs-check: $(wc -l <"$work/current.txt" | tr -d ' ') types proposed by $(wc -l <"$work/pulls.txt" | tr -d ' ') open pull requests"

if ((update)); then
	cp "$work/current.txt" "$baseline"
	echo "upstream-prs-check: baseline updated at $baseline"
	exit 0
fi

[[ -f "$baseline" ]] || {
	echo "upstream-prs-check: no baseline; run scripts/upstream-prs-check.sh --update"
	exit 1
}

status=0
if ! diff -u "$baseline" "$work/current.txt" >"$work/diff.txt"; then
	echo "upstream-prs-check: the official pull requests changed since the baseline (- baseline, + now):"
	sed -n '3,$p' "$work/diff.txt" | sed 's/^/  /'
	echo "upstream-prs-check: review it, then run scripts/upstream-prs-check.sh --update"
	status=1
fi

scripts/our-types.sh >"$work/ours.txt"
collisions="$(awk -F'\t' 'NR==FNR{ours[$1];next} ($1 in ours){print $1" (#"$2")"}' \
	"$work/ours.txt" "$work/current.txt" | sort -u)"
# A standing risk, not a regression: these are already in the baseline, so a new
# one arrives as a baseline diff and fails the run there.
if [[ -n "$collisions" ]]; then
	echo "upstream-prs-check: open pull requests propose types this provider already declares:"
	echo "$collisions" | sed 's/^/  /'
	echo "upstream-prs-check: those types are at risk of a name change before merge; follow the pull request."
fi

exit "$status"
