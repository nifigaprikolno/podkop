#!/bin/sh
# Why does a tunnel hostname return 502? The answer is almost always one of
# three things, and this prints all three at once: what is running, whether the
# containers can reach each other, and what cloudflared says it tried to dial.
set -eu

SELF_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

compose() {
    docker compose --project-directory "$SELF_DIR" -f "$SELF_DIR/docker-compose.yml" "$@"
}

printf '=== containers ===\n'
compose ps --format 'table {{.Service}}\t{{.State}}\t{{.Ports}}' 2>/dev/null ||
    compose ps

printf '\n=== networks ===\n'
for svc in podkop-server 3x-ui cloudflared; do
    cid=$(compose ps -q "$svc" 2>/dev/null || true)
    if [ -z "$cid" ]; then
        printf '%-14s not running\n' "$svc"
        continue
    fi
    nets=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$cid" 2>/dev/null || true)
    printf '%-14s %s\n' "$svc" "$nets"
done

# Reachability is tested from inside the compose network, which is exactly the
# vantage point cloudflared has. A service that answers here but 502s through
# the tunnel is a route pointing somewhere else — most often at 127.0.0.1,
# which for cloudflared means its own container.
printf '\n=== reachable from inside the network ===\n'
probe() {
    target="$1"
    if compose exec -T 3x-ui wget -S -O /dev/null "$target" 2>&1 |
        grep -m1 -oE 'HTTP/[0-9.]+ [0-9]+.*'; then
        return 0
    fi
    printf '%-24s unreachable\n' "$target"
}
printf '%-24s ' 'http://3x-ui:2053/'
probe http://3x-ui:2053/ || true
printf '%-24s ' 'http://podkop-server:8080/'
probe http://podkop-server:8080/ || true

printf '\n=== what cloudflared tried to dial ===\n'
compose logs --tail=200 cloudflared 2>/dev/null |
    grep -iE 'originservice|error|unable to reach|refused|no such host|dial|ingress' |
    tail -15 ||
    printf 'nothing matched in the last 200 lines\n'

printf '\nAny 502 with the services reachable above means the hostname route\n'
printf 'names a different origin (or a different tunnel) than this stack.\n'
printf 'The route must say http://3x-ui:2053 or http://podkop-server:8080.\n'
