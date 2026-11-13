#!/bin/sh
# jkp manual test setup — starts Ergo and creates IRC accounts for endpoints.
#
# Usage:
#   ./scripts/jkp-setup.sh              Start Ergo + create default accounts
#   ./scripts/jkp-setup.sh add <name>   Create an additional account
#
# After setup, start agents with:
#   jkp agent --name oc-author --harness opencode --oc-session auto ...
#   jkp agent --name pi-reviewer --harness pi ...
set -eu

JKP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ERGO_DIR="$JKP_DIR/deploy/ergo"
OPER_PW="jkp-test-oper-pw"
ERGO_HOST="${JKP_IRC_HOST:-127.0.0.1}"
ERGO_PORT="${JKP_IRC_PORT:-6667}"

log() { printf 'jkp-setup: %s\n' "$*"; }

# Start Ergo if not already running.
if docker ps --filter name=jkp-ergo --format '{{.Status}}' 2>/dev/null | grep -q "Up"; then
  log "Ergo already running"
else
  log "Starting Ergo..."
  docker compose -f "$ERGO_DIR/docker-compose.yml" up -d 2>&1 | tail -1
  sleep 3
  log "Ergo started"
fi

# Wait for Ergo to accept connections.
for i in 1 2 3 4 5; do
  if nc -z -w2 "$ERGO_HOST" "$ERGO_PORT" 2>/dev/null; then
    break
  fi
  sleep 1
done

# Create an IRC account via the server operator.
create_account() {
  name="$1"
  pass="${2:-${name}-pw}"
  log "Creating IRC account: $name"

  python3 - "$ERGO_HOST" "$ERGO_PORT" "$OPER_PW" "$name" "$pass" <<'PYEOF'
import socket, sys, time

host, port, oper_pw, nick, pw = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], sys.argv[5]

s = socket.create_connection((host, port), timeout=10)
s.settimeout(5)

def send(line):
    s.sendall((line + "\r\n").encode())

def read_until(marker, timeout=8):
    deadline = time.time() + timeout
    buf = b""
    while time.time() < deadline:
        try:
            data = s.recv(4096)
        except socket.timeout:
            continue
        if not data:
            break
        buf += data
        if marker.encode() in buf:
            return buf.decode(errors="replace")
    return buf.decode(errors="replace")

send("NICK jkpsetup")
send("USER jkpsetup 0 * :jkp setup")
read_until(" 001 ")

send("OPER admin " + oper_pw)
read_until(" 381 ")

send("NICKSERV SAREGISTER " + nick + " " + pw)
time.sleep(0.5)

send("QUIT :done")
s.close()
print(f"  account {nick} created")
PYEOF
}

# Create default accounts with the same password for simplicity.
# In production each endpoint should have its own strong password.
create_account "oc-author" "oc-author-pw"
create_account "pi-reviewer" "pi-reviewer-pw"
create_account "human" "human-pw"

# Create additional accounts if requested.
if [ "${1:-}" = "add" ] && [ -n "${2:-}" ]; then
  create_account "$2" "${3:-$2-pw}"
fi

log "Setup complete."
log ""
log "Next steps:"
log "  1. Start the OpenCode agent:"
log "     jkp agent --name oc-author --harness opencode \\"
log "       --oc-url http://127.0.0.1:4200 \\"
log "       --oc-password \$OPENCODE_SERVER_PASSWORD \\"
log "       --oc-session auto \\"
log "       --irc-password oc-author-pw &"
log ""
log "  2. Start pi with the jkp extension:"
log "     pi --extension $JKP_DIR/extensions/pi-jkp/index.ts"
log ""
log "  3. In pi, type:  /jkp join oc-author"
log ""
log "  4. Send a message from either side and watch them communicate."
