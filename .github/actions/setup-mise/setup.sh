#!/usr/bin/env bash
# Installs mise at a pinned version, checksum-verified, then the mise.toml tools named in MISE_TOOLS (every tool when it
# is empty), and puts their bin directories on PATH for the job's later steps. The setup-mise action runs it; the runner
# provides RUNNER_OS, RUNNER_ARCH, RUNNER_TEMP and GITHUB_PATH.
#
# To move to a newer mise, change version and take each archive's sum from the release's SHASUMS256.txt.
set -euo pipefail

runner_os="${RUNNER_OS:?set by the Actions runner}"
runner_arch="${RUNNER_ARCH:?set by the Actions runner}"
runner_temp="${RUNNER_TEMP:?set by the Actions runner}"
path_file="${GITHUB_PATH:?set by the Actions runner}"

version=2026.10.11
case "${runner_os}/${runner_arch}" in
  Linux/X64)
    asset="mise-v${version}-linux-x64.tar.xz"
    sum=f5cf1e23fc08e44c72f5a77b656e03af8131b82a75407bb3a343e8eb4de28f6f
    ;;
  Linux/ARM64)
    asset="mise-v${version}-linux-arm64.tar.xz"
    sum=48756eedb15143ea143a9b24dff6e34e57df5078f6f01f25a228650364e58531
    ;;
  macOS/ARM64)
    asset="mise-v${version}-macos-arm64.tar.xz"
    sum=8cf78384382d5df52e7b897a05053e01cfa96108e642d6b7f300c4a80ed43422
    ;;
  macOS/X64)
    asset="mise-v${version}-macos-x64.tar.xz"
    sum=59e463b8aef9425a48478c1536c998be8aa0e54f197b184688c2420aa79e4da3
    ;;
  Windows/X64)
    asset="mise-v${version}-windows-x64.zip"
    sum=2337f9fdb13ba51d3bf5831cacec9c59e70fec9630e9ad30f65975eb6b83b162
    ;;
  *)
    echo "setup-mise: no pinned mise build for ${runner_os}/${runner_arch}" >&2
    exit 1
    ;;
esac

# On Windows, bash works in Unix-style paths and GITHUB_PATH takes Windows ones.
temp="${runner_temp}"
to_path_file=(printf '%s\n')
if command -v cygpath >/dev/null; then
  temp="$(cygpath -u "${runner_temp}")"
  to_path_file=(cygpath -w)
fi

dir="${temp}/mise-${version}"
mkdir -p "${dir}"
curl -fsSL --retry 3 -o "${dir}/${asset}" "https://github.com/jdx/mise/releases/download/v${version}/${asset}"
if command -v sha256sum >/dev/null; then
  printf '%s  %s\n' "${sum}" "${dir}/${asset}" | sha256sum -c -
else
  printf '%s  %s\n' "${sum}" "${dir}/${asset}" | shasum -a 256 -c -
fi
case "${asset}" in
  *.zip) unzip -q -o "${dir}/${asset}" -d "${dir}" ;;
  *) tar -xJf "${dir}/${asset}" -C "${dir}" ;;
esac

bin="${dir}/mise/bin"
export PATH="${bin}:${PATH}"
"${to_path_file[@]}" "${bin}" >>"${path_file}"
mise --version

# The ${tools[@]+...} form expands an empty list to nothing under set -u, even on bash 3.2.
read -r -a tools <<<"${MISE_TOOLS:-}"
mise install ${tools[@]+"${tools[@]}"}
mise bin-paths ${tools[@]+"${tools[@]}"} >>"${path_file}"
