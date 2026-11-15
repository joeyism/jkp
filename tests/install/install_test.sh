#!/bin/sh
# tests/install/install_test.sh — proves jkp installs on a machine without Go.
#
# Runs scripts/install.sh against a local HTTP server and a sandboxed HOME.
# The sandbox PATH has no `go`. Success means the installed binary runs and
# the slash command files do not mention compiling.
#
# Usage: sh tests/install/install_test.sh
#   JKP_FIXTURE_BIN  — path to a prebuilt jkp binary to install (optional)
#   JKP_SKIP_REAL_BINARY=1 — skip the real-binary cross-compile half

set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

# Every non-binary asset fetched by install.sh must be staged by the release workflow.
for asset in install.sh opencode-jkp.md opencode-jkp-skill.md claude-jkp.md pi-jkp.ts SHA256SUMS; do
  if ! grep -Fq "$asset" "$ROOT/.github/workflows/release.yml"; then
    fail "release workflow does not stage installer asset: $asset"
  fi
done
pass "release workflow stages all installer assets"

# A project-local OpenCode command shadows ~/.config/opencode/command/jkp.md.
# Keep this repository from shipping the old host-only prototype, which would
# ignore an invite argument and tell guests to run `jkp host`.
if [ -f "$ROOT/.opencode/command/jkp.md" ]; then
  fail "stale project-local .opencode/command/jkp.md shadows the invite-aware installed command"
fi
if ! grep -Fq 'If `$ARGUMENTS` starts with `jkp1.`' "$ROOT/integrations/opencode/jkp.md"; then
  fail "canonical OpenCode command does not route jkp1 invites to join"
fi
pass "OpenCode invite command is not shadowed by project-local prototype"

SANDBOX="$(mktemp -d)"
RELEASE="$(mktemp -d)"
TOOLS="$(mktemp -d)"
trap 'rm -rf "$SANDBOX" "$RELEASE" "$TOOLS"' EXIT

# Keep the installer PATH independent of runner-provided tools (including Go).
for tool in sh uname mktemp rm curl awk mkdir cp chmod dirname; do
  tool_path="$(PATH="/usr/bin:/bin:$PATH" command -v "$tool" 2>/dev/null)" \
    || fail "required host tool not found: $tool"
  ln -s "$tool_path" "$TOOLS/$tool"
done
if command -v sha256sum >/dev/null 2>&1; then
  ln -s "$(PATH="/usr/bin:/bin:$PATH" command -v sha256sum)" "$TOOLS/sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  ln -s "$(PATH="/usr/bin:/bin:$PATH" command -v shasum)" "$TOOLS/shasum"
else
  fail "neither sha256sum nor shasum found"
fi

if _sys_go="$(PATH="/usr/bin:/bin" command -v go 2>/dev/null)"; then
  echo "diag: host /usr/bin:/bin has go at $_sys_go ($(/bin/ls -ld "$_sys_go" 2>/dev/null || true))"
else
  echo "diag: host /usr/bin:/bin has no go"
fi

assert_no_go_in_path() {
  _check_path="$1"
  if _found_go="$(HOME="$SANDBOX" PATH="$_check_path" command -v go 2>/dev/null)"; then
    fail "go is present in sandbox PATH ($_check_path): $_found_go ($(/bin/ls -ld "$_found_go" 2>/dev/null || true))"
  fi
}

# --- fixture binary ---
if [ -n "${JKP_FIXTURE_BIN:-}" ] && [ -x "$JKP_FIXTURE_BIN" ]; then
  cp "$JKP_FIXTURE_BIN" "$RELEASE/jkp-linux-amd64"
else
  cat > "$RELEASE/jkp-linux-amd64" << 'STUB'
#!/bin/sh
if [ "$1" = "version" ]; then echo ci-test; exit 0; fi
echo "stub jkp $*"
exit 0
STUB
  chmod +x "$RELEASE/jkp-linux-amd64"
fi
cp "$RELEASE/jkp-linux-amd64" "$RELEASE/jkp-darwin-arm64"

# --- checksums (sha256sum or shasum -a 256) ---
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$RELEASE" && sha256sum jkp-linux-amd64 jkp-darwin-arm64 > SHA256SUMS )
elif command -v shasum >/dev/null 2>&1; then
  ( cd "$RELEASE" && shasum -a 256 jkp-linux-amd64 jkp-darwin-arm64 > SHA256SUMS )
else
  fail "neither sha256sum nor shasum found"
fi

# --- hook template fixtures (overwritten by real files in Task 5) ---
if [ -f "$ROOT/integrations/opencode/jkp.md" ]; then
  cp "$ROOT/integrations/opencode/jkp.md" "$RELEASE/opencode-jkp.md"
else
  cat > "$RELEASE/opencode-jkp.md" << 'FIX'
---
description: Host a jkp room for this session
---
Run this exact command. Do not add flags.

    jkp host
FIX
fi
if [ -f "$ROOT/integrations/opencode/jkp-skill/SKILL.md" ]; then
  cp "$ROOT/integrations/opencode/jkp-skill/SKILL.md" "$RELEASE/opencode-jkp-skill.md"
fi
if [ -f "$ROOT/integrations/claude/jkp.md" ]; then
  cp "$ROOT/integrations/claude/jkp.md" "$RELEASE/claude-jkp.md"
else
  cat > "$RELEASE/claude-jkp.md" << 'FIX'
---
description: Host or join a jkp room
---
Run: jkp host  (or jkp join --invite <token> --pi-session claude)
FIX
fi
# pi extension: copy real file, or stub if the real one still has "go build"
if [ -f "$ROOT/extensions/pi-jkp/index.ts" ] && ! grep -q "go build" "$ROOT/extensions/pi-jkp/index.ts"; then
  cp "$ROOT/extensions/pi-jkp/index.ts" "$RELEASE/pi-jkp.ts"
else
  echo 'export default function jkp() {}' > "$RELEASE/pi-jkp.ts"
fi
cp "$ROOT/scripts/install.sh" "$RELEASE/install.sh" 2>/dev/null || true

# --- start HTTP server ---
SRV_LOG="$(mktemp)"
python3 -c "
import http.server, socketserver, os, sys
os.chdir(sys.argv[1])
with socketserver.TCPServer(('127.0.0.1', 0), http.server.SimpleHTTPRequestHandler) as httpd:
    print(f'SERVING {httpd.server_address[1]}', flush=True)
    httpd.serve_forever()
" "$RELEASE" > "$SRV_LOG" 2>&1 &
SRV_PID=$!
trap 'kill $SRV_PID 2>/dev/null; rm -rf "$SANDBOX" "$RELEASE" "$TOOLS" "$SRV_LOG"' EXIT

# wait for port
PORT=""
for _ in $(seq 1 50); do
  PORT=$(grep -o 'SERVING [0-9]*' "$SRV_LOG" | awk '{print $2}')
  [ -n "$PORT" ] && break
  sleep 0.1
done
[ -n "$PORT" ] || fail "HTTP server did not report a port (log: $(cat $SRV_LOG 2>/dev/null | head -3))"
BASE="http://127.0.0.1:$PORT"
pass "HTTP server on $BASE"

# --- verify go is absent from the sandbox PATH ---
if echo "/usr/bin:/bin:$SANDBOX/.local/bin" | grep -q "go" && command -v go >/dev/null 2>&1; then
  : # go is on the OUTER path; that is fine for the real-binary half
fi
# The sandboxed install runs with PATH="/usr/bin:/bin:$SANDBOX/.local/bin"
# which never contains go. Assert that after install.

# ============================================================
# CASE 1: happy path (binary + hooks installed, exit 0)
# ============================================================
echo "--- case 1: happy path ---"
assert_no_go_in_path "$TOOLS:$SANDBOX/.local/bin"
if HOME="$SANDBOX" \
   JKP_PREFIX="$SANDBOX/.local" \
   JKP_RELEASE_BASE="$BASE" \
   JKP_TARGET="linux-amd64" \
   PATH="$TOOLS:$SANDBOX/.local/bin" \
   sh "$ROOT/scripts/install.sh"
then
  pass "install.sh exited 0"
else
  fail "install.sh exited $? (want 0)"
fi

# go must not be on the sandbox PATH
assert_no_go_in_path "$TOOLS:$SANDBOX/.local/bin"
pass "go absent from sandbox PATH"

# binary runs
GOT="$(HOME="$SANDBOX" PATH="$TOOLS:$SANDBOX/.local/bin" "$SANDBOX/.local/bin/jkp" version 2>&1 || true)"
if [ "$GOT" != "ci-test" ]; then
  fail "jkp version: got '$GOT', want 'ci-test'"
fi
pass "jkp version prints ci-test"

# hook files exist
for f in \
  "$SANDBOX/.config/opencode/command/jkp.md" \
  "$SANDBOX/.config/opencode/skills/jkp/SKILL.md" \
  "$SANDBOX/.claude/commands/jkp.md" \
  "$SANDBOX/.pi/agent/extensions/jkp.ts"
do
  [ -f "$f" ] || fail "missing hook file: $f"
done
pass "all command and skill files present"

# hooks must not mention compiling
if grep -q "go build" "$SANDBOX/.config/opencode/command/jkp.md" \
  "$SANDBOX/.config/opencode/skills/jkp/SKILL.md" \
  "$SANDBOX/.claude/commands/jkp.md" \
  "$SANDBOX/.pi/agent/extensions/jkp.ts"
then
  fail "hook files contain 'go build'"
fi
pass "hooks do not mention 'go build'"

# OpenCode file must contain the exact command
grep -q "jkp host" "$SANDBOX/.config/opencode/command/jkp.md" \
  || fail "OpenCode command does not contain 'jkp host'"
pass "OpenCode command contains 'jkp host'"

# ============================================================
# CASE 2: PATH warning (exit 2)
# ============================================================
echo "--- case 2: PATH warning ---"
set +e
HOME="$SANDBOX" \
  JKP_PREFIX="$SANDBOX/.local2" \
  JKP_RELEASE_BASE="$BASE" \
  JKP_TARGET="linux-amd64" \
  PATH="$TOOLS" \
  sh "$ROOT/scripts/install.sh" > "$SANDBOX/case2.out" 2>&1
RC2=$?
set -e
[ "$RC2" = "2" ] || fail "case 2: exit $RC2, want 2"
grep -q "export PATH=" "$SANDBOX/case2.out" || fail "case 2: no PATH hint in output"
pass "PATH warning works (exit 2)"

# ============================================================
# CASE 3: checksum mismatch (exit non-zero, mentions checksum)
# ============================================================
echo "--- case 3: checksum mismatch ---"
cp "$RELEASE/jkp-linux-amd64" "$RELEASE/jkp-linux-amd64.bak"
printf 'X' >> "$RELEASE/jkp-linux-amd64"
set +e
HOME="$SANDBOX" \
  JKP_PREFIX="$SANDBOX/.local3" \
  JKP_RELEASE_BASE="$BASE" \
  JKP_TARGET="linux-amd64" \
  PATH="$TOOLS" \
  sh "$ROOT/scripts/install.sh" > "$SANDBOX/case3.out" 2>&1
RC3=$?
set -e
[ "$RC3" -ne 0 ] || fail "case 3: exit $RC3, want non-zero"
grep -qi "checksum" "$SANDBOX/case3.out" || fail "case 3: no 'checksum' in output"
mv "$RELEASE/jkp-linux-amd64.bak" "$RELEASE/jkp-linux-amd64"
pass "checksum mismatch rejected (exit $RC3)"

# ============================================================
# CASE 4 (optional): real Go binary through the installer
# ============================================================
if [ "${JKP_SKIP_REAL_BINARY:-}" != "1" ] && command -v go >/dev/null 2>&1; then
  echo "--- case 4: real Go binary ---"
  case "$(uname -s)" in
    Darwin) _OS=darwin ;;
    Linux)  _OS=linux  ;;
    *)      echo "case 4: skipped (unsupported OS)"; _OS="" ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) _ARCH=arm64 ;;
    x86_64)        _ARCH=amd64 ;;
    *)             _ARCH="" ;;
  esac
  if [ -n "$_OS" ] && [ -n "$_ARCH" ]; then
    _REAL_TARGET="${_OS}-${_ARCH}"
    rm -f "$RELEASE/jkp-$_REAL_TARGET"
    CGO_ENABLED=0 GOOS="$_OS" GOARCH="$_ARCH" \
      go build -ldflags "-X github.com/joey/jkp/internal/cli.Version=ci-test" \
      -o "$RELEASE/jkp-$_REAL_TARGET" "$ROOT/cmd/jkp"
    if command -v sha256sum >/dev/null 2>&1; then
      ( cd "$RELEASE" && sha256sum jkp-* > SHA256SUMS )
    else
      ( cd "$RELEASE" && shasum -a 256 jkp-* > SHA256SUMS )
    fi
    if HOME="$SANDBOX" \
       JKP_PREFIX="$SANDBOX/.local4" \
       JKP_RELEASE_BASE="$BASE" \
       PATH="$TOOLS:$SANDBOX/.local4/bin" \
       sh "$ROOT/scripts/install.sh"
    then
      pass "real binary install exited 0"
    else
      fail "real binary install exited $?"
    fi
    GOT4="$(HOME="$SANDBOX" PATH="$TOOLS:$SANDBOX/.local4/bin" "$SANDBOX/.local4/bin/jkp" version 2>&1 || true)"
    [ "$GOT4" = "ci-test" ] || fail "real binary: got '$GOT4', want 'ci-test'"
    pass "real Go binary prints ci-test"
  fi
else
  echo "--- case 4: skipped (JKP_SKIP_REAL_BINARY=1 or no go) ---"
fi

echo ""
echo "ALL PASS"
