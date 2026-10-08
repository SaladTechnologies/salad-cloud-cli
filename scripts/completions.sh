#!/bin/sh
# Writes the shell completion scripts into completions/, for the release
# archives and package managers. Run by goreleaser before it builds.
set -eu
cd "$(dirname "$0")/.."
rm -rf completions
mkdir -p completions
for shell in bash zsh fish powershell; do
  ext=$shell
  [ "$shell" = powershell ] && ext=ps1
  go run ./cmd/saladcloud completion "$shell" > "completions/saladcloud.$ext"
done
