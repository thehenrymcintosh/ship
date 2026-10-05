#!/bin/sh
# Lists the sha256 of every version of the skills `ship init` has installed
# (from git history plus the working tree) in internal/initfiles/shipped.txt.
# ship refreshes an installed skill only when it matches one of these, so a
# skill someone has edited is never overwritten. Run after changing a skill.
set -eu
cd "$(dirname "$0")/.."
out=internal/initfiles/shipped.txt
tmp=$(mktemp)
for f in internal/initfiles/files/*/SKILL.md; do
	for c in $(git log --format=%H -- "$f"); do
		git show "$c:$f" 2>/dev/null | shasum -a 256 | cut -d' ' -f1 >>"$tmp" || true
	done
	shasum -a 256 <"$f" | cut -d' ' -f1 >>"$tmp"
done
{
	echo "# sha256 of every shipped version of the init skills; see scripts/skill-hashes.sh"
	sort -u "$tmp"
} >"$out"
rm -f "$tmp"
