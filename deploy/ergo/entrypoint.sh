#!/bin/sh
# jkp Ergo test entrypoint.
#
# Derives a deterministic config from the image's default.yaml with a known
# operator password, generates TLS certs, and runs the server. Test-only: the
# operator password below is intentionally a well-known value and must never be
# used on a real deployment.
set -eu

CONF=/ircd/ircd.yaml
OPER_PW="${JKP_OPER_PASSWORD:-jkp-test-oper-pw}"

if [ ! -f "$CONF" ]; then
    ENCRYPTED=$(/ircd-bin/ergo genpasswd <<EOF
$OPER_PW
EOF
)
    # The default.yaml ships a placeholder bcrypt hash used for every example
    # oper password. Replace it literally (not as a regex) with the generated
    # hash so OPER admin <OPER_PW> authenticates deterministically.
    awk -v enc="$ENCRYPTED" '
      BEGIN {
        orig = "\"$2a$04$0123456789abcdef0123456789abcdef0123456789abcdef01234\""
        repl = "\"" enc "\""
      }
      {
        line = $0
        out = ""
        while ((i = index(line, orig)) > 0) {
          out = out substr(line, 1, i - 1) repl
          line = substr(line, i + length(orig))
        }
        print out line
      }
    ' /ircd-bin/default.yaml \
      | sed 's|path: languages|path: /ircd-bin/languages|' \
      | sed 's|max-concurrent-connections: 16|max-concurrent-connections: 1000|' \
      | sed 's|max-connections-per-window: 32|max-connections-per-window: 10000|' \
      | sed 's|max-attempts: 3$|max-attempts: 10000|' \
      > "$CONF"
fi

# Self-signed TLS certs for the test server name. mkcerts exits non-zero when
# the files already exist, which is fine on a restart with a persistent volume.
# cd to /ircd so certs land on the data volume and survive container recreate.
cd /ircd
/ircd-bin/ergo mkcerts || true

exec /ircd-bin/ergo run --conf "$CONF"
