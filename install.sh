#!/bin/sh
# Installs the SaladCloud CLI (saladcloud) on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/install.sh | sh
#
# Downloads the release archive for this machine, verifies it against the
# release's checksums.txt, and installs the binary. Environment, all optional:
#
#   SALAD_CLI_VERSION       release tag, e.g. v0.1.0-beta.1 (default: the latest
#                            release, or the newest pre-release while there is none)
#   SALAD_CLI_INSTALL_DIR   where to put the binary (default: $HOME/.local/bin)
#   SALAD_CLI_REPO          GitHub repository (default: SaladTechnologies/salad-cloud-cli)
#   SALAD_CLI_DOWNLOAD_URL  base URL holding the release assets, instead of the
#                            repository's release downloads (for testing)
#   GITHUB_TOKEN            sent to the GitHub API when finding the latest
#                            release, to avoid its rate limit (CI)
set -eu

REPO="${SALAD_CLI_REPO:-SaladTechnologies/salad-cloud-cli}"
INSTALL_DIR="${SALAD_CLI_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${SALAD_CLI_VERSION:-}"

say() { printf 'saladcloud install: %s\n' "$*" >&2; }
die() {
  say "error: $*"
  exit 1
}

# fetch URL DEST: download to a file, failing on HTTP errors.
fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "curl or wget is required"
  fi
}

os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS '$os' (on Windows, use install.ps1)" ;;
esac
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU architecture '$arch'" ;;
esac
target="${os}_${arch}"
case "$target" in
  linux_amd64 | linux_arm64 | darwin_amd64 | darwin_arm64) ;;
  *) die "no prebuilt binary for $os/$arch; with Go 1.25 or later, run: go install github.com/saladtechnologies/salad-cloud-cli/cmd/saladcloud@latest" ;;
esac

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t saladcloud)
trap 'rm -rf "$tmp"' EXIT INT TERM

# latest_from_redirect prints the tag github.com/<repo>/releases/latest
# redirects to, or nothing when there is no full release (only
# pre-releases, which /releases/latest skips). No API call, no rate limit.
latest_from_redirect() {
  if command -v curl >/dev/null 2>&1; then
    url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null) || return 0
  elif command -v wget >/dev/null 2>&1; then
    # The last Location header wget follows is the release's tag page.
    # cspell:ignore ocation
    url=$(wget -q -S --spider "https://github.com/$REPO/releases/latest" 2>&1 | tr -d '\r' | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1)
  else
    return 0
  fi
  case "$url" in
    */releases/tag/*) printf '%s\n' "${url##*/}" ;;
  esac
}

# newest_from_api prints the newest release of any kind, pre-releases
# included, through the GitHub API (with GITHUB_TOKEN when set).
newest_from_api() {
  api="https://api.github.com/repos/$REPO/releases?per_page=1"
  if command -v curl >/dev/null 2>&1; then
    if [ -n "${GITHUB_TOKEN:-}" ]; then
      curl -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" -o "$tmp/release.json" "$api" || return 0
    else
      curl -fsSL --retry 3 -o "$tmp/release.json" "$api" || return 0
    fi
  elif [ -n "${GITHUB_TOKEN:-}" ]; then
    wget -q --header "Authorization: Bearer $GITHUB_TOKEN" -O "$tmp/release.json" "$api" || return 0
  else
    wget -q -O "$tmp/release.json" "$api" || return 0
  fi
  sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp/release.json" | head -n 1
}

if [ -z "$VERSION" ]; then
  # The latest full release first; during the beta every release is a
  # pre-release, so fall back to the newest release of any kind.
  VERSION=$(latest_from_redirect)
  [ -n "$VERSION" ] || VERSION=$(newest_from_api)
  [ -n "$VERSION" ] || die "could not find a release of $REPO; set SALAD_CLI_VERSION to choose one"
fi

name="saladcloud_${VERSION}_${target}"
archive="$name.tar.gz"
base="${SALAD_CLI_DOWNLOAD_URL:-https://github.com/$REPO/releases/download/$VERSION}"
say "installing saladcloud $VERSION for $os/$arch"
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || die "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
  die "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || die "checksum mismatch for $archive (expected $expected, got $actual)"

tar -xzf "$tmp/$archive" -C "$tmp"
mkdir -p "$INSTALL_DIR"
# Copy then rename, so replacing a binary that is in use is atomic.
cp "$tmp/$name/saladcloud" "$INSTALL_DIR/.saladcloud.new"
chmod 755 "$INSTALL_DIR/.saladcloud.new"
mv -f "$INSTALL_DIR/.saladcloud.new" "$INSTALL_DIR/saladcloud"
say "installed $INSTALL_DIR/saladcloud"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "$INSTALL_DIR is not on your PATH; add it with: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
say "for shell completion, see saladcloud completion --help (scripts are also in the release archive)"
if [ -z "${SALAD_CLI_DOWNLOAD_URL:-}" ]; then
  say "to verify this build's provenance: gh attestation verify $archive --repo $REPO (on the archive from $base)"
fi
"$INSTALL_DIR/saladcloud" version
printf '\nNext, log in:\n  saladcloud login\n' >&2
