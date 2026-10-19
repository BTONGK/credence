#!/bin/sh
# Credence installer: https://tokenmanagementgroup.com/credence
#
#   curl -fsSL https://tokenmanagementgroup.com/credence/install.sh | sh
#
# Picks the build for this machine from the latest GitHub release, checks its
# SHA-256 and installs `credence` into ~/.local/bin.
#
# Then it runs `credence setup`, which asks two questions on the terminal:
# connect Credence to your agents, and check your recent sessions.
#
# Settings (optional):
#   CREDENCE_VERSION=v0.9.0       install a specific release
#   CREDENCE_INSTALL_DIR=/path    install somewhere else
#   CREDENCE_NO_MODIFY_PATH=1     leave shell startup files as they are
#   CREDENCE_NO_SETUP=1           install only; run `credence` later to set up
#   CREDENCE_SETUP=1              set up without questions (answers yes to both)
#   CREDENCE_SETUP_FLAGS="..."    extra flags for `credence setup`
#   CREDENCE_DOWNLOAD_BASE=...    fetch builds from this URL or folder

set -eu

REPO="BTONGK/credence"

say() { printf '%s\n' "$*"; }
fail() { printf 'credence install: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

fetch() { # fetch URL FILE
  case "$1" in
    /*) cp "$1" "$2"; return ;;
  esac
  if has curl; then
    curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
  elif has wget; then
    wget -q -O "$2" "$1"
  else
    fail "this installer needs curl or wget"
  fi
}

sha256() {
  if has sha256sum; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif has shasum; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  fi
}

# An upgrade restarts the background service from `credence setup`, so it
# runs the new version.
restart_service() {
  case "$(uname -s)" in
    Darwin)
      svc="gui/$(id -u)/dev.credence.daemon"
      [ -f "$HOME/Library/LaunchAgents/dev.credence.daemon.plist" ] || return 0
      if launchctl print "$svc" >/dev/null 2>&1 && launchctl kickstart -k "$svc" >/dev/null 2>&1; then
        say "Restarted the Credence background service."
      fi
      ;;
    Linux)
      [ -f "$HOME/.config/systemd/user/credenced.service" ] || return 0
      if has systemctl && systemctl --user is-active --quiet credenced.service 2>/dev/null &&
        systemctl --user restart credenced.service 2>/dev/null; then
        say "Restarted the Credence background service."
      fi
      ;;
  esac
}

main() {
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "Credence runs on macOS and Linux (this machine reports $(uname -s))" ;;
  esac

  case "$(uname -m)" in
    arm64 | aarch64) arch=arm64 ;;
    x86_64 | amd64) arch=amd64 ;;
    *) fail "Credence builds cover arm64 and x86-64 (this machine reports $(uname -m))" ;;
  esac
  # A Terminal running under Rosetta reports x86_64 on Apple silicon.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
    [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
    arch=arm64
  fi

  version="${CREDENCE_VERSION:-latest}"
  if [ -n "${CREDENCE_DOWNLOAD_BASE:-}" ]; then
    base="${CREDENCE_DOWNLOAD_BASE%/}"
  elif [ "$version" = latest ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$version"
  fi
  archive="credence_${os}_${arch}.tar.gz"
  dir="${CREDENCE_INSTALL_DIR:-$HOME/.local/bin}"

  tmp="$(mktemp -d 2>/dev/null || mktemp -d -t credence)"
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 1' INT TERM

  say "Downloading Credence ($os/$arch)..."
  fetch "$base/$archive" "$tmp/$archive" || fail "download failed: $base/$archive"
  fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

  want="$(grep " $archive\$" "$tmp/checksums.txt" | cut -d ' ' -f 1)"
  got="$(sha256 "$tmp/$archive")"
  if [ -n "$got" ]; then
    [ "$want" = "$got" ] || fail "checksum mismatch for $archive"
    say "Checksum verified."
  fi

  tar -xzf "$tmp/$archive" -C "$tmp" credence
  mkdir -p "$dir"
  # Replace through a rename so a running copy keeps working.
  cp "$tmp/credence" "$dir/.credence.new"
  chmod 755 "$dir/.credence.new"
  mv -f "$dir/.credence.new" "$dir/credence"
  say "Installed $("$dir/credence" version) to $dir/credence"
  restart_service

  case ":$PATH:" in
    *":$dir:"*) on_path=1 ;;
    *) on_path=0 ;;
  esac

  case "$dir" in
    "$HOME"/*) shown="\$HOME${dir#"$HOME"}" ;;
    *) shown="$dir" ;;
  esac
  line="export PATH=\"$shown:\$PATH\""
  if [ "$on_path" = 0 ] && [ "${CREDENCE_NO_MODIFY_PATH:-0}" != 1 ]; then
    case "${SHELL:-}" in
      */zsh) rcs="$HOME/.zshrc" ;;
      */bash) if [ "$os" = darwin ]; then rcs="$HOME/.bash_profile $HOME/.bashrc"; else rcs="$HOME/.bashrc"; fi ;;
      */fish) rcs="" ;;
      *) rcs="$HOME/.profile" ;;
    esac
    case "${SHELL:-}" in
      */fish)
        mkdir -p "$HOME/.config/fish/conf.d"
        printf 'fish_add_path %s\n' "$dir" > "$HOME/.config/fish/conf.d/credence.fish"
        say "Added $dir to your PATH in ~/.config/fish/conf.d/credence.fish"
        ;;
    esac
    for rc in $rcs; do
      if [ -f "$rc" ] && grep -qF "$line" "$rc"; then
        continue
      fi
      printf '\n# Added by the Credence installer\n%s\n' "$line" >> "$rc"
      say "Added $dir to your PATH in $rc"
    done
  fi

  # Setup asks its two questions on the terminal, even under `curl | sh`.
  # An existing setup gets only what this version adds, with no questions.
  set_up=0
  if [ "${CREDENCE_NO_SETUP:-0}" != 1 ]; then
    if [ -f "${CREDENCE_HOME:-$HOME/.credence}/config.yaml" ]; then
      say ""
      "$dir/credence" setup --if-needed && set_up=1
    elif [ "${CREDENCE_SETUP:-0}" = 1 ]; then
      say ""
      # shellcheck disable=SC2086
      "$dir/credence" setup --if-needed --yes ${CREDENCE_SETUP_FLAGS:-} && set_up=1
    elif [ -t 1 ] && (exec </dev/tty) 2>/dev/null; then
      say ""
      # shellcheck disable=SC2086
      "$dir/credence" setup --if-needed ${CREDENCE_SETUP_FLAGS:-} </dev/tty && set_up=1
    fi
  fi

  say ""
  if [ "$set_up" = 1 ]; then
    if [ "$on_path" = 1 ]; then
      say "Type credence anytime to open your reports."
    else
      say "Open a new terminal, then type credence anytime to open your reports."
      say "In this terminal, run this line first:"
      say ""
      say "  $line"
    fi
    return
  fi
  if [ "$on_path" = 1 ]; then
    say "Ready. Type credence to set up and see your first reports."
  else
    say "Ready. Open a new terminal and type credence to set up and see your first reports."
    say "Or start right now in this terminal:"
    say ""
    say "  $line && credence"
  fi
}

main "$@"
