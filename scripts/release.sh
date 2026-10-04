#!/usr/bin/env bash
# Cut a release: test, build and install locally, tag, push.
#
#   scripts/release.sh [patch|minor|major|X.Y.Z] [--dry-run] [--no-push]
#
# Bumps from the latest vX.Y.Z tag (default: patch; v0.0.0 if none yet).
# Pushing the tag triggers .github/workflows/release.yml, which publishes the
# archives that install.sh and `ship update` download.
set -euo pipefail

bump=patch dry=false push=true
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry=true ;;
    --no-push) push=false ;;
    -h | --help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) bump=$arg ;;
  esac
done

cd "$(git rev-parse --show-toplevel)"
die() { printf 'release: %s\n' "$*" >&2; exit 1; }
run() { if $dry; then printf '  (dry run) %s\n' "$*"; else "$@"; fi; }

# Next version.
last=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || echo v0.0.0)
IFS=. read -r major minor patch <<<"${last#v}"
case "$bump" in
  patch) next="$major.$minor.$((patch + 1))" ;;
  minor) next="$major.$((minor + 1)).0" ;;
  major) next="$((major + 1)).0.0" ;;
  [0-9]*.[0-9]*.[0-9]*) next=${bump#v} ;;
  v[0-9]*.[0-9]*.[0-9]*) next=${bump#v} ;;
  *) die "unknown bump '$bump' (patch, minor, major or X.Y.Z)" ;;
esac
tag="v$next"
git rev-parse -q --verify "refs/tags/$tag" >/dev/null && die "$tag already exists"

# Release from a clean, up-to-date main.
branch=$(git rev-parse --abbrev-ref HEAD)
[ "$branch" = main ] || die "on '$branch'; releases are cut from main"
[ -z "$(git status --porcelain --untracked-files=no)" ] || die "uncommitted changes; commit or stash them first"
git fetch -q origin main
[ "$(git rev-list --count HEAD..origin/main)" = 0 ] || die "main is behind origin/main; pull first"

echo "Releasing $last → $tag"
echo "Running tests…"
go test ./... >/dev/null || die "tests failed (run: go test ./...)"

echo "Building and installing $tag locally…"
run make install VERSION="$next"

run git tag -a "$tag" -m "ship $tag"
if $push; then
  run git push -q origin main
  run git push -q origin "$tag"
  echo "Pushed $tag. GitHub Actions is building the release:"
  echo "  https://github.com/thehenrymcintosh/ship/actions"
  echo "Once it's done, others can install or \`ship update\` to it."
else
  echo "Tagged $tag locally (not pushed). Push with: git push origin main $tag"
fi
