#!/bin/sh
# Fresh-install test: installs Credence the way a new user does, with the
# one-line installer and guided setup, inside a throwaway home folder, and
# checks each step a new user sees. Run it before every release.
#
#   scripts/fresh-install-test.sh
#
# The machine's own ~/.claude, ~/.credence and background services stay
# as they are: everything runs with HOME set to a temporary folder, the
# service step is skipped, and a stand-in `claude` command records calls.
# KEEP=1 keeps the temporary folder for a look afterwards.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
tmp=$(cd "$tmp" && pwd -P)
fake="$tmp/home"
bin="$fake/.local/bin/credence"
mkdir -p "$fake" "$tmp/stub" "$tmp/dist"

pass=0
ok() { pass=$((pass + 1)); printf '  ✓ %s\n' "$1"; }
bad() { printf '  ✗ %s\n' "$1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/      /'; finish 1; }
expect() { # expect FILE TEXT DESCRIPTION
  if grep -qF -- "$2" "$1"; then ok "$3"; else bad "$3 (looked for: $2)" "$(tail -n 40 "$1")"; fi
}
absent() { # absent FILE TEXT DESCRIPTION
  if grep -qF -- "$2" "$1" 2>/dev/null; then bad "$3 (found: $2)"; else ok "$3"; fi
}
finish() {
  [ -x "$bin" ] && run "$bin" stop >/dev/null 2>&1 || true
  if [ "${KEEP:-0}" = 1 ]; then echo "kept $tmp"; else rm -rf "$tmp"; fi
  [ "$1" = 0 ] && echo "fresh-install test passed: $pass checks"
  exit "$1"
}
trap 'finish 1' INT TERM

# A clean environment, like a new Mac account.
run() {
  env -i HOME="$fake" USER="${USER:-tester}" SHELL=/bin/zsh TERM=dumb LANG=en_US.UTF-8 TMPDIR="$tmp/" \
    PATH="$tmp/stub:/usr/bin:/bin:/usr/sbin:/sbin" CREDENCE_NO_OPEN=1 "$@"
}

# A stand-in for the claude command.
cat > "$tmp/stub/claude" <<'EOF'
#!/bin/sh
echo "claude $*" >> "$HOME/claude-calls.log"
[ "$1" = --version ] && echo "2.1.0 (Claude Code)"
exit 0
EOF
chmod 755 "$tmp/stub/claude"

case "$(uname -s)" in Darwin) os=darwin ;; *) os=linux ;; esac
case "$(uname -m)" in arm64 | aarch64) arch=arm64 ;; *) arch=amd64 ;; esac

echo "Building this checkout for $os/$arch"
sh "$root/scripts/dist.sh" "$tmp/dist" "$os/$arch" >/dev/null
tar -xzf "$tmp/dist/credence_${os}_${arch}.tar.gz" -C "$tmp" credence
want=$("$tmp/credence" version)

echo "A Claude Code session from earlier today: the bakery example"
run "$tmp/credence" demo --no-serve --dir "$tmp/fixture" --ended 40m > "$tmp/fixture.log"
mkdir -p "$fake/.claude"
cp -R "$tmp/fixture/claude/projects" "$fake/.claude/projects"
transcript=$(find "$fake/.claude/projects" -name '*.jsonl' | head -n 1)
session=$(basename "$transcript" .jsonl)
cwd="$tmp/fixture/sunrise-bakery"

echo "Dry run"
run "$tmp/credence" init --dry-run --no-service > "$tmp/dry.log"
expect "$tmp/dry.log" "Dry run: everything above is a preview" "dry run says it is a preview"
[ -e "$fake/.claude/settings.json" ] && bad "dry run wrote settings.json"
[ -e "$fake/.claude/skills" ] && bad "dry run wrote a skill"
[ -e "$fake/.credence/config.yaml" ] && bad "dry run wrote config.yaml"
ok "dry run leaves settings, skills and config as they were"

echo "Install with the one-line installer, answering yes to both questions"
run CREDENCE_DOWNLOAD_BASE="$tmp/dist" CREDENCE_SETUP=1 CREDENCE_SETUP_FLAGS="--no-service --no-open" \
  sh "$root/scripts/install.sh" > "$tmp/install.log" 2>&1 || bad "installer failed" "$(cat "$tmp/install.log")"
sed 's/^/    | /' "$tmp/install.log"
expect "$tmp/install.log" "Checksum verified." "installer checks the download"
expect "$tmp/install.log" "Installed $want" "installer installs $want"
expect "$fake/.zshrc" '$HOME/.local/bin' "installer adds credence to PATH in .zshrc"
expect "$tmp/install.log" "Connect Credence to Claude Code?" "setup asks to connect Claude Code"
expect "$fake/.claude/settings.json" "$bin hook claude-code" "Claude Code hooks point at the installed binary"
expect "$fake/.claude/skills/credence/SKILL.md" "$bin check --harness claude-code" "the /credence skill runs credence check"
expect "$fake/claude-calls.log" "mcp add --scope user credence -- $bin mcp" "setup registers the MCP tool"
expect "$tmp/install.log" "self-check passed" "setup checks itself"
expect "$tmp/install.log" "Check your sessions from the last 7 days now?" "setup offers the first check"
expect "$tmp/install.log" "1 session checked: 7 claims" "setup checks the earlier session"
expect "$tmp/install.log" "contradicted" "setup sums up the verdicts"
expect "$tmp/install.log" "Type /credence in Claude Code" "setup explains /credence"
expect "$tmp/install.log" "Open a new terminal, then type credence anytime" "installer ends with the next step"

echo "After a Claude Code reply"
printf '{"hook_event_name":"Stop","session_id":"%s","transcript_path":"%s","cwd":"%s","stop_hook_active":false,"last_assistant_message":"All 6 tests pass. The site is live at https://sunrise-bakery.pages.dev."}' \
  "$session" "$transcript" "$cwd" | (cd "$cwd" && run "$bin" hook claude-code) > "$tmp/note.json"
cat "$tmp/note.json" | sed 's/^/    | /'
expect "$tmp/note.json" '"systemMessage":"Credence: 2 claims in this reply' "the reply note counts the claims"
expect "$tmp/note.json" "✗ “All 6 tests pass.”" "the reply note leads with the contradicted claim"
printf '{"hook_event_name":"Stop","session_id":"%s","transcript_path":"%s","cwd":"%s","last_assistant_message":"Want me to add a gallery page next?"}' \
  "$session" "$transcript" "$cwd" | (cd "$cwd" && run "$bin" hook claude-code) > "$tmp/quiet.json"
[ -s "$tmp/quiet.json" ] && bad "a reply without claims gets a note" "$(cat "$tmp/quiet.json")"
ok "a reply without claims stays quiet"

echo "/credence in Claude Code"
(cd "$cwd" && run "$bin" check --harness claude-code "--session=$session") > "$tmp/check.log"
sed 's/^/    | /' "$tmp/check.log"
expect "$tmp/check.log" "Credence checked 7 claims in this session" "credence check lists the session's claims"
expect "$tmp/check.log" "✗ Contradicted: “All 6 tests pass.”" "credence check shows the contradicted claim first"
expect "$tmp/check.log" "Full report: run \`credence\` in a terminal." "credence check ends with the next step"

echo "Everyday commands"
run "$bin" doctor > "$tmp/doctor.log" 2>&1 || bad "doctor failed" "$(cat "$tmp/doctor.log")"
expect "$tmp/doctor.log" "Everything works." "doctor passes"
run "$bin" > "$tmp/bare.log" 2>&1 || bad "credence on its own failed" "$(cat "$tmp/bare.log")"
expect "$tmp/bare.log" "http://127.0.0.1:" "credence on its own opens the reports"
run "$bin" sessions > "$tmp/sessions.log"
expect "$tmp/sessions.log" "cake order form for the bakery website" "credence sessions lists the session"

echo "Upgrade: the installer runs again"
run CREDENCE_DOWNLOAD_BASE="$tmp/dist" sh "$root/scripts/install.sh" > "$tmp/upgrade.log" 2>&1 || bad "upgrade failed" "$(cat "$tmp/upgrade.log")"
absent "$tmp/upgrade.log" "Connect Credence" "an upgrade keeps the existing setup"
expect "$tmp/upgrade.log" "Credence is set up." "an upgrade says Credence is set up"

echo "Uninstall"
run "$bin" uninstall --yes > "$tmp/uninstall.log" 2>&1 || bad "uninstall failed" "$(cat "$tmp/uninstall.log")"
absent "$fake/.claude/settings.json" "hook claude-code" "uninstall removes the hooks"
[ -e "$fake/.claude/skills/credence" ] && bad "uninstall leaves the skill"
ok "uninstall removes the /credence skill"
expect "$fake/claude-calls.log" "mcp remove --scope user credence" "uninstall removes the MCP tool"

finish 0
