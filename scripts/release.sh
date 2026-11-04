#!/usr/bin/env bash
# Tags a release the way Releasing in AGENTS.md describes, so a mistake is caught before it becomes a tag (tags can be
# neither moved nor deleted). It checks the version's form, that the tag is new, that main's CHANGELOG.md has a dated
# section for it and that test.yml passed on main's tip, then asks, creates an annotated tag on origin/main and pushes
# it, which starts .github/workflows/release.yml.
#
# This is the way in only until the release GitHub App is set up: then a release starts from the workflow (Actions →
# Release → Run workflow), the tag ruleset lets only the App create tags, and this script goes.
#
#   just release v0.1.0
set -euo pipefail

die() {
  printf 'release: %s\n' "$*" >&2
  exit 1
}

[[ $# -eq 1 ]] || die "usage: just release vX.Y.Z (or vX.Y.Z-rc.N for a release candidate)"
tag=$1
[[ ${tag} =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] ||
  die "'${tag}' is not vX.Y.Z or vX.Y.Z-rc.N (see Releasing in AGENTS.md)"
version=${tag#v}
today=$(date -u +%F)

git fetch --quiet origin main
commit=$(git rev-parse origin/main)

if git ls-remote --exit-code --tags origin "refs/tags/${tag}" >/dev/null; then
  die "${tag} already exists on GitHub; tags are permanent, so release the next version instead"
fi
if git rev-parse --quiet --verify "refs/tags/${tag}" >/dev/null; then
  die "${tag} exists only on this machine; remove it with: git tag -d ${tag}"
fi

changelog=$(git show "${commit}:CHANGELOG.md")
dated=$(printf '%s\n' "${changelog}" | sed -n "s/^## \[${version//./\\.}\] - \([0-9-]*\).*/\1/p" | head -n 1)
[[ -n ${dated} ]] ||
  die "CHANGELOG.md on main has no dated section for ${version}; merge a PR that adds \"## [${version}] - ${today}\""
if [[ ${dated} != "${today}" ]]; then
  printf 'warning: CHANGELOG.md dates %s %s, not today (%s)\n' "${version}" "${dated}" "${today}" >&2
fi
unreleased=$(printf '%s\n' "${changelog}" | awk '/^## \[Unreleased\]/ { s = 1; next } /^## \[/ { s = 0 } s && NF')
if [[ -n ${unreleased} ]]; then
  printf "warning: CHANGELOG.md's Unreleased section on main is not empty\n" >&2
fi

runs=$(gh run list --workflow test.yml --commit "${commit}" --json conclusion --jq 'map(.conclusion) | join(" ")')
case " ${runs} " in
  *" success "*) ;;
  *) die "test.yml has not passed on main's tip (${commit:0:7}); start it with: gh workflow run test.yml --ref main" ;;
esac

subject=$(git log -1 --format=%s "${commit}")
printf 'Tag %s on main at %s: %s\n' "${tag}" "${commit:0:7}" "${subject}"
read -r -p "Create and push ${tag}? [y/N] " answer || answer=
case "${answer}" in
  y | Y) ;;
  *) die "nothing tagged" ;;
esac
git tag -a "${tag}" -m "SaladCloud CLI ${version}" "${commit}"
git push origin "refs/tags/${tag}"
printf 'Pushed %s. Follow the release with: gh run watch (pick "Release") or in the Actions tab.\n' "${tag}"
