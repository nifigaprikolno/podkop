#!/bin/sh
# Bring the deployment to the intended state without hand-editing .env over
# SSH. Every setting it touches is written idempotently: run it twice and the
# second run reports "unchanged" instead of duplicating lines.
#
#   ./setup.sh --admin-host panel.example.com
#
# Secrets already in .env (the admin password, the tunnel token) are never
# read out or rewritten.
set -eu

SELF_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ENV_FILE="$SELF_DIR/.env"
EXAMPLE_FILE="$SELF_DIR/.env.example"

ADMIN_HOST=""
XRAY_PORT_VALUE="443"
XUI_UI_BIND_VALUE="127.0.0.1"
TRUSTED_PROXY_VALUE="true"
PROFILES="tunnel,xui"
BUILD="yes"
NEW_ADMIN_PASSWORD=""

usage() {
    cat <<'EOF'
Usage: ./setup.sh [options]

  --admin-host HOST   hostname whose root goes to the operator panel
                      (leave unset to keep whatever .env already has)
  --xray-port N       port the Xray inbound listens on          (default 443)
  --profiles LIST     comma separated compose profiles          (default tunnel,xui)
  --new-admin-password
                      generate a new operator password and print it once
  --no-trusted-proxy  do not set PODKOP_SERVER_TRUSTED_PROXY=true
  --no-build          skip rebuilding the podkop-server image
  -h, --help          this text
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --admin-host) ADMIN_HOST="${2:?--admin-host needs a value}"; shift 2 ;;
        --admin-host=*) ADMIN_HOST="${1#*=}"; shift ;;
        --xray-port) XRAY_PORT_VALUE="${2:?--xray-port needs a value}"; shift 2 ;;
        --xray-port=*) XRAY_PORT_VALUE="${1#*=}"; shift ;;
        --profiles) PROFILES="${2:?--profiles needs a value}"; shift 2 ;;
        --profiles=*) PROFILES="${1#*=}"; shift ;;
        --new-admin-password) NEW_ADMIN_PASSWORD="yes"; shift ;;
        --no-trusted-proxy) TRUSTED_PROXY_VALUE=""; shift ;;
        --no-build) BUILD=""; shift ;;
        -h|--help) usage; exit 0 ;;
        *) printf 'unknown option: %s\n\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
done

die() { printf 'setup: %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker is not installed"
docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"

# A random token that survives being copied around: no shell metacharacters,
# no characters that look alike in a terminal font.
random_token() {
    LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | dd bs=1 count="$1" 2>/dev/null
}

. "$SELF_DIR/env-kv.sh"

FRESH_ENV=""
if [ ! -f "$ENV_FILE" ]; then
    [ -f "$EXAMPLE_FILE" ] || die "neither .env nor .env.example found in $SELF_DIR"
    cp "$EXAMPLE_FILE" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
    FRESH_ENV="yes"
    printf 'No .env here — created one from .env.example.\n\n'
fi

printf 'Settings in %s:\n' "$ENV_FILE"
if [ -n "$FRESH_ENV" ]; then
    # The example ships placeholders that must not survive into a running
    # deployment: a guessable admin path and a literal CHANGE-ME password.
    set_kv PODKOP_SERVER_ADMIN_PATH "/manage-$(random_token 10)/"
    set_kv PODKOP_SERVER_ADMIN_PASSWORD "$(random_token 32)"
elif [ -n "$NEW_ADMIN_PASSWORD" ]; then
    set_kv PODKOP_SERVER_ADMIN_PASSWORD "$(random_token 32)"
fi
[ -n "$ADMIN_HOST" ] && set_kv PODKOP_SERVER_ADMIN_HOST "$ADMIN_HOST"
[ -n "$TRUSTED_PROXY_VALUE" ] && set_kv PODKOP_SERVER_TRUSTED_PROXY "$TRUSTED_PROXY_VALUE"
set_kv XRAY_PORT "$XRAY_PORT_VALUE"
set_kv XUI_UI_BIND "$XUI_UI_BIND_VALUE"
[ -n "$ENV_KV_CHANGED" ] && printf '\nPrevious .env kept as %s.bak\n' "$ENV_FILE"

# cloudflared exits immediately without a token, which is a confusing way to
# find out that .env is incomplete.
case ",$PROFILES," in
    *,tunnel,*)
        [ -n "$(get_kv CLOUDFLARE_TUNNEL_TOKEN)" ] || die \
            "profile 'tunnel' needs CLOUDFLARE_TUNNEL_TOKEN in .env (Zero Trust → Networks → Tunnels)"
        ;;
esac

set -- --project-directory "$SELF_DIR" -f "$SELF_DIR/docker-compose.yml"
for profile in $(printf '%s' "$PROFILES" | tr ',' ' '); do
    set -- "$@" --profile "$profile"
done
set -- "$@" up -d
[ -n "$BUILD" ] && set -- "$@" --build

printf '\nStarting: docker compose %s\n\n' "$*"
docker compose "$@"

printf '\nContainers:\n'
docker compose --project-directory "$SELF_DIR" -f "$SELF_DIR/docker-compose.yml" ps

case ",$PROFILES," in
    *,xui,*)
        creds=$(docker compose --project-directory "$SELF_DIR" -f "$SELF_DIR/docker-compose.yml" \
            logs 3x-ui 2>/dev/null | grep -iE 'username|password|webbasepath|panel url' | head -20 || true)
        printf '\n3x-UI credentials from the log:\n'
        if [ -n "$creds" ]; then
            printf '%s\n' "$creds"
        else
            printf '  nothing matched — the first-run banner is gone from the log.\n'
            printf '  Ask the container itself: ./xui-creds.sh (add --reset to set a\n'
            printf '  new login if the old one is lost).\n'
        fi
        ;;
esac

if command -v ss >/dev/null 2>&1; then
    printf '\nListening on non-loopback addresses (expect SSH and the Xray port only):\n'
    ss -lnt 2>/dev/null | awk 'NR == 1 || ($4 !~ /^127\./ && $4 !~ /^\[::1\]/)'
fi

if [ -n "$FRESH_ENV" ]; then
    printf '\nFirst run: the admin path and password printed above were generated\n'
    printf 'and written to .env. Write them down — nothing else displays them.\n'
fi
