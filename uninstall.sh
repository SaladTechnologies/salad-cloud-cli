#!/bin/sh
# Uninstalls the SaladCloud CLI (saladcloud) that install.sh put on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/uninstall.sh | sh
#
# Removes the binary from the folder install.sh used. The config file (your
# saved API keys, organization and project) stays, so a reinstall picks up
# where you left off, unless SALAD_CLI_PURGE=1. Environment, all optional:
#
#   SALAD_CLI_INSTALL_DIR   the folder install.sh used (default: $HOME/.local/bin)
#   SALAD_CLI_PURGE         set to 1 to also delete the config file and the
#                            CLI's known_hosts (and their folder, once empty)
#   SALAD_CLI_CONFIG_DIR    the config folder, if you set it for the CLI
#
# A saladcloud installed some other way (Homebrew, go install, a manual copy)
# is not touched; the script says where it is.
set -eu

INSTALL_DIR="${SALAD_CLI_INSTALL_DIR:-$HOME/.local/bin}"
bin="$INSTALL_DIR/saladcloud"

say() { printf 'saladcloud uninstall: %s\n' "$*" >&2; }

# The binary knows where its config is (the OS's folder, or
# SALAD_CLI_CONFIG_DIR); ask it before removing it. Without it, use the folder
# it would have used.
config=""
if [ -x "$bin" ]; then
  config=$("$bin" config path 2>/dev/null || true)
fi
if [ -z "$config" ]; then
  if [ -n "${SALAD_CLI_CONFIG_DIR:-}" ]; then
    config="$SALAD_CLI_CONFIG_DIR/config.json"
  elif [ "$(uname -s)" = Darwin ]; then
    config="$HOME/Library/Application Support/saladcloud/config.json"
  else
    config="${XDG_CONFIG_HOME:-$HOME/.config}/saladcloud/config.json"
  fi
fi

if [ -e "$bin" ]; then
  rm -f "$bin"
  say "removed $bin"
else
  say "no saladcloud in $INSTALL_DIR; nothing to remove there"
fi

dir=$(dirname "$config")
if [ "${SALAD_CLI_PURGE:-}" = 1 ]; then
  # Only the files the CLI writes, never the folder's other contents:
  # SALAD_CLI_CONFIG_DIR can point at a folder that holds other things.
  if [ -e "$config" ] || [ -e "$dir/known_hosts" ]; then
    rm -f "$config" "$dir/known_hosts"
    rmdir "$dir" 2>/dev/null || true
    say "removed the config file $config and the CLI's known_hosts"
  fi
elif [ -e "$config" ]; then
  say "kept the config file $config (it holds your saved API keys); delete it, or rerun with SALAD_CLI_PURGE=1"
fi
if [ -e "$HOME/.ssh/saladcloud_ed25519" ]; then
  say "kept the SSH key ~/.ssh/saladcloud_ed25519 (saladcloud ssh-key setup made it, and it may be registered in the portal)"
fi

# Another copy still on PATH came from somewhere else.
if other=$(command -v saladcloud 2>/dev/null); then
  if command -v brew >/dev/null 2>&1 && brew list --cask saladcloud >/dev/null 2>&1; then
    how="brew uninstall --cask saladcloud"
  else
    case "$other" in
      */go/bin/*) how="rm $other (go install put it there)" ;;
      *) how="remove it the way it was installed" ;;
    esac
  fi
  say "another saladcloud is still on your PATH: $other; to remove it, $how"
fi
