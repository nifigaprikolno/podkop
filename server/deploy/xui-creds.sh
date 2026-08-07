#!/bin/sh
# Recover the 3x-UI login. The container prints its generated credentials once,
# on the very first start, so by the time anyone looks the line is usually gone
# from the log — this asks the binary instead, and can set a fresh login when
# even that comes up empty.
#
#   ./xui-creds.sh                       show current settings
#   ./xui-creds.sh --reset               set a new random username/password
#   ./xui-creds.sh --reset --user NAME   ... with a username of your choosing
set -eu

SELF_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
RESET=""
USERNAME="admin"

while [ $# -gt 0 ]; do
    case "$1" in
        --reset) RESET="yes"; shift ;;
        --user) USERNAME="${2:?--user needs a value}"; shift 2 ;;
        --user=*) USERNAME="${1#*=}"; shift ;;
        -h|--help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) printf 'unknown option: %s\n' "$1" >&2; exit 2 ;;
    esac
done

die() { printf 'xui-creds: %s\n' "$*" >&2; exit 1; }

compose() {
    docker compose --project-directory "$SELF_DIR" -f "$SELF_DIR/docker-compose.yml" "$@"
}

compose ps --status running --services 2>/dev/null | grep -qx '3x-ui' ||
    die "the 3x-ui container is not running — start it with ./setup.sh"

printf 'Credential lines still in the log:\n'
logged=$(compose logs 3x-ui 2>/dev/null |
    grep -iE 'username|password|webbasepath|panel url|access url' | head -20 || true)
if [ -n "$logged" ]; then
    printf '%s\n' "$logged"
else
    printf '  none — the first-run banner has scrolled out or was never written\n'
fi

# The binary lives at /app/x-ui in the official image; the others are where
# distro packages and the install script put it.
XUI=""
for candidate in /app/x-ui /usr/local/x-ui/x-ui /usr/bin/x-ui; do
    if compose exec -T 3x-ui test -x "$candidate" 2>/dev/null; then
        XUI="$candidate"
        break
    fi
done
[ -n "$XUI" ] || die "no x-ui binary found in the container; run: docker compose exec 3x-ui sh"

if [ -n "$RESET" ]; then
    NEW_PASSWORD=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | dd bs=1 count=24 2>/dev/null)
    NEW_PATH="/$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | dd bs=1 count=12 2>/dev/null)/"

    printf '\nSetting a new login:\n  username: %s\n  password: %s\n' "$USERNAME" "$NEW_PASSWORD"
    compose exec -T 3x-ui "$XUI" setting -username "$USERNAME" -password "$NEW_PASSWORD"

    # A panel sitting at "/" is one hostname route away from being the first
    # thing a scanner finds, so the path gets moved too. Older builds have no
    # such flag — then it is a field in Panel Settings.
    if compose exec -T 3x-ui "$XUI" setting -webBasePath "$NEW_PATH" >/dev/null 2>&1; then
        printf '  path:     %s\n' "$NEW_PATH"
    else
        printf '  path:     unchanged — this build takes no -webBasePath;\n'
        printf '            set it in Panel Settings once you are logged in\n'
    fi

    compose restart 3x-ui >/dev/null
    printf 'Done — the container was restarted so the change takes effect.\n'
fi

printf '\nCurrent settings:\n'
settings=$(compose exec -T 3x-ui "$XUI" setting -show 2>&1 || true)
printf '%s\n' "$settings"

case "$settings" in
    *hasDefaultCredential:\ true*)
        printf '\n!! The panel still has its default login. Anyone who reaches port\n'
        printf '!! 2053 is in — right now only loopback does, but do not publish it\n'
        printf '!! on a hostname before running: ./xui-creds.sh --reset\n'
        ;;
esac

case "$settings" in
    *"webBasePath: /"[!a-zA-Z0-9]* | *"webBasePath: /")
        printf '\nNote: the panel answers at the root path. Behind Cloudflare Access\n'
        printf 'that is survivable, but a secret path is the cheaper second layer.\n'
        ;;
esac

printf '\nThe interface stays on loopback. Reach it with:\n'
printf '  ssh -L 2053:127.0.0.1:2053 %s@THIS-VPS\n' "$(id -un)"
printf '  then http://127.0.0.1:2053/<webBasePath>/\n'
