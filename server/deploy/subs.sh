#!/bin/sh
# Print every client's URLs, and check that they answer. Retyping a token into
# a wrapped SSH line is how tokens get mangled — the panel already knows them,
# so nothing here has to be typed by hand.
#
#   ./subs.sh            list clients with their URLs
#   ./subs.sh --check    ... and fetch each subscription, reporting the status
set -eu

SELF_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ENV_FILE="$SELF_DIR/.env"
CHECK=""

while [ $# -gt 0 ]; do
    case "$1" in
        --check) CHECK="yes"; shift ;;
        -h|--help) sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) printf 'unknown option: %s\n' "$1" >&2; exit 2 ;;
    esac
done

die() { printf 'subs: %s\n' "$*" >&2; exit 1; }

command -v python3 >/dev/null 2>&1 || die "python3 is required to read the store"
[ -f "$ENV_FILE" ] || die "no .env in $SELF_DIR"

. "$SELF_DIR/env-kv.sh"

BASE=$(get_kv PODKOP_SERVER_PUBLIC_URL)
if [ -z "$BASE" ]; then
    die "PODKOP_SERVER_PUBLIC_URL is not set in .env — the URLs would point at
     whatever host you browse the panel from, which behind Access is a host no
     client can fetch from"
fi
BASE="${BASE%/}"

compose() {
    docker compose --project-directory "$SELF_DIR" -f "$SELF_DIR/docker-compose.yml" "$@"
}

STORE=$(compose exec -T podkop-server cat /var/lib/podkop-server/store.json 2>/dev/null) ||
    die "could not read the store — is the panel running?"

printf '%s' "$STORE" | BASE="$BASE" python3 -c '
import json, os, sys

base = os.environ["BASE"]
try:
    data = json.load(sys.stdin)
except ValueError as exc:
    sys.exit("subs: store is not valid JSON: %s" % exc)

clients = data.get("clients") or {}
if not clients:
    print("No clients yet — issue one on the CLIENTS screen.")
    sys.exit(0)

for token, c in sorted(clients.items(), key=lambda kv: kv[1].get("name", "")):
    state = "on" if c.get("enabled") else "OFF"
    link = c.get("proxy_string") or ""
    print("%s  [%s]" % (c.get("name", "?"), state))
    print("  router: %s/api/v1/profile?token=%s" % (base, token))
    print("  phone:  %s/api/v1/sub?token=%s" % (base, token))
    if not link:
        print("  !! no proxy link stored — the subscription would be empty")
    print()
'

[ -n "$CHECK" ] || exit 0

printf '=== fetching each subscription ===\n'
printf '%s' "$STORE" | python3 -c '
import json, sys
data = json.load(sys.stdin)
for token, c in (data.get("clients") or {}).items():
    print("%s\t%s" % (token, c.get("name", "?")))
' | while IFS='	' read -r token name; do
    [ -n "$token" ] || continue
    code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 \
        "$BASE/api/v1/sub?token=$token" 2>/dev/null || echo "---")
    case "$code" in
        200) note="ok" ;;
        401) note="token missing from the URL" ;;
        403) note="client disabled" ;;
        404) note="unknown token, or no proxy link stored" ;;
        30*) note="redirected — this host is behind Access, clients cannot fetch it" ;;
        *) note="unexpected" ;;
    esac
    printf '%-24s %s  %s\n' "$name" "$code" "$note"
done
