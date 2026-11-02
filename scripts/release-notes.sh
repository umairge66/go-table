#!/usr/bin/env bash
# Prints the release notes of a tag as Markdown: every commit since the
# previous tag with its full message (subject as a heading, the body below),
# then a link to the full comparison. Usage: scripts/release-notes.sh v1.2.0
# Needs the full history (actions/checkout with fetch-depth: 0).
set -euo pipefail
cd "$(dirname "$0")/.."

tag=${1:?usage: scripts/release-notes.sh <tag>}
prev=$(git describe --tags --abbrev=0 "$tag^" 2>/dev/null || true)
range=${prev:+$prev..}$tag
repo=${GITHUB_REPOSITORY:-$(git remote get-url origin | sed -E 's#(git@|https://)github.com[:/]##; s#\.git$##')}

details=""
small=""
for sha in $(git rev-list --reverse --no-merges "$range"); do
	subject=$(git log -1 --format=%s "$sha")
	# The body without trailers such as Co-Authored-By and Signed-off-by.
	body=$(git log -1 --format=%b "$sha" | grep -viE '^(co-authored-by|signed-off-by):' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')
	short=$(git rev-parse --short "$sha")
	if [[ -n "${body//[[:space:]]/}" ]]; then
		details+="### ${subject}"$'\n\n'"${body}"$'\n\n'"<sub>${short}</sub>"$'\n\n'
	else
		small+="- ${subject} (${short})"$'\n'
	fi
done

printf '%s' "$details"
if [[ -n "$small" ]]; then
	[[ -n "$details" ]] && printf '### Other changes\n\n'
	printf '%s\n' "$small"
fi
if [[ -n "$prev" ]]; then
	printf '**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$repo" "$prev" "$tag"
fi
