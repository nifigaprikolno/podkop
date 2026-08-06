# shellcheck shell=ash
# remote_config.sh — receive route settings from a central VPS panel (podkop-server).
#
# Only the panel URL and the per-client token/key are stored on the router; the
# panel decides which proxy key, community lists, zones and DNS a client gets and
# ships them as a small JSON "profile". The last profile is cached locally so the
# router keeps working while the panel is unreachable (offline resilience).
#
# Profile JSON shape (all fields optional; missing fields are left untouched):
#   {
#     "proxy_string":    "vless://...",            // key issued by the panel/3x-UI
#     "community_lists":  ["russia_inside", ...],   // replaces main.community_lists
#     "p2p_direct":       true,
#     "direct_ru_zones":  true,
#     "dns":             { "type": "doh", "server": "https://..." },
#     "update_interval":  "1d"
#   }
#
# The panel does not have to be published on the internet: remote_config_access
# selects how the router reaches it.
#   direct       — plain request to remote_config_url (default, old behaviour)
#   tunnel_route — the panel lives on a tunnel address (e.g. the AmneziaWG /24);
#                  podkop adds a single host route through the tunnel interface,
#                  because AWG sections are imported with route_allowed_ips=0 and
#                  therefore install no routes of their own
#   proxy        — the request goes through podkop's own service mixed inbound,
#                  so it leaves via the proxy outbound and the panel can sit on
#                  the VPS loopback (127.0.0.1:8080)

REMOTE_CONFIG_DEFAULT_CACHE="/etc/podkop/remote_profile.json"
REMOTE_CONFIG_CURL_TIMEOUT=15
REMOTE_CONFIG_DEFAULT_ACCESS="direct"

# Is remote management enabled and configured? Echoes url/token via globals.
_remote_config_load_settings() {
    config_get_bool _rc_enabled "settings" "remote_config_enabled" 0
    config_get _rc_url "settings" "remote_config_url"
    config_get _rc_token "settings" "remote_config_token"
    config_get _rc_cache "settings" "remote_config_cache" "$REMOTE_CONFIG_DEFAULT_CACHE"
    config_get _rc_access "settings" "remote_config_access" "$REMOTE_CONFIG_DEFAULT_ACCESS"
    config_get _rc_route_iface "settings" "remote_config_route_interface" "$AWG_DEFAULT_INTERFACE"
}

# IPv4 check without regexes: is_ipv4() in helpers.sh leans on escapes whose
# support differs between shells and libc regex engines, and a wrong answer here
# would silently refuse to install the route.
_remote_config_is_ipv4() {
    local ip="$1" octet count=0

    case "$ip" in
    "" | *[!0-9.]* | .* | *. | *..*) return 1 ;;
    esac

    while [ -n "$ip" ]; do
        octet="${ip%%.*}"
        case "$ip" in
        *.*) ip="${ip#*.}" ;;
        *) ip="" ;;
        esac

        [ "${#octet}" -le 3 ] || return 1
        [ "$octet" -le 255 ] || return 1
        count=$((count + 1))
    done

    [ "$count" -eq 4 ]
}

# Host route to the panel through the tunnel interface. Idempotent, in the style
# of route_table_rule_mark. The panel host must be an IP: a domain would have to
# be resolved first, and its address may well be outside the tunnel.
_remote_config_ensure_route() {
    local host="$1" iface="$2"

    if [ -z "$host" ] || [ -z "$iface" ]; then
        log "remote_config: tunnel_route needs both a panel host and an interface" "error"
        return 1
    fi

    if ! _remote_config_is_ipv4 "$host"; then
        log "remote_config: tunnel_route requires an IP in remote_config_url, got '$host'" "error"
        return 1
    fi

    if ! ip link show "$iface" > /dev/null 2>&1; then
        log "remote_config: interface '$iface' does not exist, cannot route to the panel" "error"
        return 1
    fi

    if [ -n "$(ip route show "$host/32" dev "$iface" 2> /dev/null)" ]; then
        log "remote_config: route to the panel exists" "debug"
        return 0
    fi

    if ip route add "$host/32" dev "$iface"; then
        log "remote_config: added route $host/32 dev $iface"
        return 0
    fi

    log "remote_config: failed to add route $host/32 dev $iface" "error"
    return 1
}

_remote_config_remove_route() {
    local host="$1" iface="$2"

    [ -n "$host" ] && [ -n "$iface" ] || return 0
    _remote_config_is_ipv4 "$host" || return 0

    if [ -n "$(ip route show "$host/32" dev "$iface" 2> /dev/null)" ]; then
        ip route del "$host/32" dev "$iface" 2> /dev/null
        log "remote_config: removed route $host/32 dev $iface" "debug"
    fi
}

# Add/remove the panel route around the service lifecycle. No-ops unless the
# tunnel_route access mode is selected.
remote_config_route_up() {
    local _rc_enabled _rc_url _rc_token _rc_cache _rc_access _rc_route_iface
    _remote_config_load_settings

    [ "$_rc_enabled" -eq 1 ] || return 0
    [ "$_rc_access" = "tunnel_route" ] || return 0
    [ -n "$_rc_url" ] || return 0

    _remote_config_ensure_route "$(url_get_host "$_rc_url")" "$_rc_route_iface"
}

remote_config_route_down() {
    local _rc_enabled _rc_url _rc_token _rc_cache _rc_access _rc_route_iface
    _remote_config_load_settings

    [ "$_rc_access" = "tunnel_route" ] || return 0
    [ -n "$_rc_url" ] || return 0

    _remote_config_remove_route "$(url_get_host "$_rc_url")" "$_rc_route_iface"
}

# Build the profile endpoint URL from the configured base URL.
_remote_config_profile_url() {
    local base="${1%/}"
    echo "${base}/api/v1/profile"
}

# Fetch the profile JSON from the panel. Echoes JSON on stdout, non-zero on failure.
# The transport depends on the access mode (see the header of this file).
_remote_config_fetch() {
    local url="$1" token="$2" access="$3" iface="$4"
    local service_proxy_address response host

    host="$(url_get_host "$url")"
    url="$(_remote_config_profile_url "$url")"

    case "$access" in
    "proxy")
        # Force the service mixed inbound; everything entering it is routed to
        # the proxy outbound, so the panel is dialed from the VPS side.
        service_proxy_address="$(get_service_mixed_inbound_address)"
        ;;
    "tunnel_route")
        _remote_config_ensure_route "$host" "$iface" || return 1
        service_proxy_address=""
        ;;
    *)
        # direct: still honour download_lists_via_proxy, as before
        service_proxy_address="$(get_service_proxy_address)"
        ;;
    esac

    if [ -n "$service_proxy_address" ]; then
        response="$(curl -fsSL -x "http://$service_proxy_address" -m "$REMOTE_CONFIG_CURL_TIMEOUT" \
            -H "Authorization: Bearer $token" -H "X-Podkop-Token: $token" \
            "${url}?token=${token}" 2>/dev/null)" || return 1
    else
        response="$(curl -fsSL -m "$REMOTE_CONFIG_CURL_TIMEOUT" \
            -H "Authorization: Bearer $token" -H "X-Podkop-Token: $token" \
            "${url}?token=${token}" 2>/dev/null)" || return 1
    fi

    [ -n "$response" ] || return 1
    # Must be valid JSON
    echo "$response" | jq empty >/dev/null 2>&1 || return 1
    echo "$response"
}

# Validate a proxy link before writing it into the config.
_remote_config_valid_proxy() {
    case "$1" in
    vless://* | ss://* | trojan://* | socks4://* | socks4a://* | socks5://* | hysteria2://* | hy2://*)
        return 0
        ;;
    esac
    return 1
}

# Apply a validated profile JSON (arg $1) to podkop's UCI config, then commit and
# reload the in-memory config. Does NOT restart the service.
_remote_config_apply() {
    local profile="$1"
    local proxy_string p2p_direct direct_ru_zones dns_type dns_server update_interval

    proxy_string="$(echo "$profile" | jq -r '.proxy_string // empty')"
    p2p_direct="$(echo "$profile" | jq -r 'if .p2p_direct == null then empty elif .p2p_direct then "1" else "0" end')"
    direct_ru_zones="$(echo "$profile" | jq -r 'if .direct_ru_zones == null then empty elif .direct_ru_zones then "1" else "0" end')"
    dns_type="$(echo "$profile" | jq -r '.dns.type // empty')"
    dns_server="$(echo "$profile" | jq -r '.dns.server // empty')"
    update_interval="$(echo "$profile" | jq -r '.update_interval // empty')"

    # Proxy key for the main section
    if [ -n "$proxy_string" ]; then
        if _remote_config_valid_proxy "$proxy_string"; then
            uci_set "podkop" "main" "proxy_config_type" "url"
            uci_set "podkop" "main" "proxy_string" "$proxy_string"
        else
            log "remote_config: ignoring proxy_string with unsupported scheme" "warn"
        fi
    fi

    # Community lists (array) → replace main.community_lists
    if echo "$profile" | jq -e '.community_lists | type == "array"' >/dev/null 2>&1; then
        uci -q delete "podkop.main.community_lists"
        echo "$profile" | jq -r '.community_lists[]' | while IFS= read -r cl; do
            [ -n "$cl" ] && uci_add_list "podkop" "main" "community_lists" "$cl"
        done
    fi

    [ -n "$p2p_direct" ] && uci_set "podkop" "settings" "p2p_direct" "$p2p_direct"
    [ -n "$direct_ru_zones" ] && uci_set "podkop" "settings" "direct_ru_zones" "$direct_ru_zones"

    case "$dns_type" in
    udp | dot | doh) uci_set "podkop" "settings" "dns_type" "$dns_type" ;;
    esac
    [ -n "$dns_server" ] && uci_set "podkop" "settings" "dns_server" "$dns_server"

    case "$update_interval" in
    1h | 3h | 12h | 1d | 3d) uci_set "podkop" "settings" "update_interval" "$update_interval" ;;
    esac

    uci commit "podkop" && config_load "$PODKOP_CONFIG"
}

# Apply the cached profile without any network access. Used at service start so the
# router boots with the last known-good settings even when the panel is offline.
remote_config_apply_cached() {
    local _rc_enabled _rc_url _rc_token _rc_cache _rc_access _rc_route_iface
    _remote_config_load_settings
    [ "$_rc_enabled" -eq 1 ] || return 0

    if [ -r "$_rc_cache" ] && [ -s "$_rc_cache" ]; then
        log "remote_config: applying cached profile from $_rc_cache"
        _remote_config_apply "$(cat "$_rc_cache")"
    else
        log "remote_config: no cached profile yet" "warn"
    fi
}

# Fetch the profile from the panel, apply it and reload podkop. Falls back to the
# cached profile when the panel is unreachable. Reloads only when something changed.
# Used by cron and by the "Apply now" button in the UI.
remote_config_update() {
    local _rc_enabled _rc_url _rc_token _rc_cache _rc_access _rc_route_iface
    _remote_config_load_settings

    if [ "$_rc_enabled" -ne 1 ]; then
        echolog "ℹ️ Remote config management is disabled"
        return 0
    fi
    if [ -z "$_rc_url" ] || [ -z "$_rc_token" ]; then
        echolog "❌ Remote config: URL or token is not set"
        return 1
    fi

    case "$_rc_access" in
    "direct" | "tunnel_route" | "proxy") ;;
    *)
        echolog "❌ Remote config: unknown access mode '$_rc_access'"
        return 1
        ;;
    esac

    echolog "🔄 Fetching route profile from the panel ($_rc_access)..."

    local profile changed=1
    if profile="$(_remote_config_fetch "$_rc_url" "$_rc_token" "$_rc_access" "$_rc_route_iface")"; then
        if [ -r "$_rc_cache" ] && [ "$profile" = "$(cat "$_rc_cache")" ]; then
            changed=0
            echolog "✅ Profile unchanged"
        else
            mkdir -p "$(dirname "$_rc_cache")"
            echo "$profile" > "$_rc_cache"
            echolog "✅ New profile fetched and cached"
        fi
    else
        if [ -r "$_rc_cache" ] && [ -s "$_rc_cache" ]; then
            profile="$(cat "$_rc_cache")"
            echolog "⚠️ Panel unreachable, using cached profile"
        else
            echolog "❌ Panel unreachable and no cached profile available"
            return 1
        fi
    fi

    _remote_config_apply "$profile" || {
        echolog "❌ Failed to apply route profile"
        return 1
    }

    if [ "$changed" -eq 1 ]; then
        echolog "🔁 Reloading podkop with the new profile"
        reload
    fi
    echolog "✅ Route profile applied"
}

# Install/refresh the cron job that pulls the profile on the configured interval.
add_remote_config_cron_job() {
    local _rc_enabled _rc_url _rc_token _rc_cache _rc_access _rc_route_iface
    local update_interval cron_job
    _remote_config_load_settings
    [ "$_rc_enabled" -eq 1 ] || return 0

    config_get update_interval "settings" "update_interval"
    case "$update_interval" in
    "1h") cron_job="27 * * * * /usr/bin/podkop remote_config_update" ;;
    "3h") cron_job="27 */3 * * * /usr/bin/podkop remote_config_update" ;;
    "12h") cron_job="27 */12 * * * /usr/bin/podkop remote_config_update" ;;
    "1d") cron_job="27 9 * * * /usr/bin/podkop remote_config_update" ;;
    "3d") cron_job="27 9 */3 * * /usr/bin/podkop remote_config_update" ;;
    *)
        log "remote_config: invalid update_interval value: $update_interval"
        return
        ;;
    esac

    remove_remote_config_cron_job
    crontab -l 2>/dev/null | {
        cat
        echo "$cron_job"
    } | crontab -
    log "remote_config: cron job created: $cron_job"
}

remove_remote_config_cron_job() {
    (crontab -l 2>/dev/null | grep -v "/usr/bin/podkop remote_config_update") | crontab -
}
