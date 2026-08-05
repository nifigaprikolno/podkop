# shellcheck shell=ash
# amneziawg.sh — import an AmneziaWG .conf and drive the interface through the
# kernel module (kmod-amneziawg + proto 'amneziawg').
#
# podkop does not implement the tunnel itself: it parses the .conf a user pastes
# (or the VPS panel pushes), writes it into /etc/config/network as an
# 'amneziawg' interface, and then routes through it with the existing
# connection_type=vpn path (sing-box binds its outbound to the interface).
#
# AmneziaWG 2.0 parameter set: jc jmin jmax s1 s2 s3 s4 h1-h4 i1-i5.
# S3/S4 and I1-I5 are 2.0 additions; H1-H4 became ranges ("123-456").
# J1-J3 and Itime were REMOVED in 2.0 and are rejected by the kernel module, so
# they are dropped with a warning if an old 1.5-era config still carries them.

# Parsed values live in these globals (reset per parse).
_awg_reset() {
    AWG_PRIVATE_KEY=""
    AWG_ADDRESSES=""
    AWG_LISTEN_PORT=""
    AWG_MTU=""
    AWG_DNS=""
    AWG_PEER_PUBLIC_KEY=""
    AWG_PEER_PRESHARED_KEY=""
    AWG_PEER_ENDPOINT_HOST=""
    AWG_PEER_ENDPOINT_PORT=""
    AWG_PEER_ALLOWED_IPS=""
    AWG_PEER_KEEPALIVE=""

    local param
    for param in $AWG_OBFUSCATION_PARAMS; do
        eval "AWG_OBF_${param}=''"
    done
}

# Normalize a comma/space separated list into a space separated one.
_awg_split_list() {
    printf '%s' "$1" | tr ',' ' ' | tr -s ' ' | sed 's/^ //; s/ $//'
}

# Split "host:port" (or "[v6]:port") into AWG_PEER_ENDPOINT_HOST/PORT.
_awg_split_endpoint() {
    local endpoint="$1"

    case "$endpoint" in
    \[*\]:*)
        AWG_PEER_ENDPOINT_HOST="${endpoint%%]:*}"
        AWG_PEER_ENDPOINT_HOST="${AWG_PEER_ENDPOINT_HOST#[}"
        AWG_PEER_ENDPOINT_PORT="${endpoint##*]:}"
        ;;
    *:*)
        AWG_PEER_ENDPOINT_HOST="${endpoint%:*}"
        AWG_PEER_ENDPOINT_PORT="${endpoint##*:}"
        ;;
    *)
        AWG_PEER_ENDPOINT_HOST="$endpoint"
        ;;
    esac
}

_awg_parse_interface_key() {
    local key="$1"
    local value="$2"

    case "$key" in
    privatekey) AWG_PRIVATE_KEY="$value" ;;
    address | addresses) AWG_ADDRESSES="$(_awg_split_list "$value")" ;;
    listenport) AWG_LISTEN_PORT="$value" ;;
    mtu) AWG_MTU="$value" ;;
    dns) AWG_DNS="$(_awg_split_list "$value")" ;;
    jc | jmin | jmax | s1 | s2 | s3 | s4 | h1 | h2 | h3 | h4 | i1 | i2 | i3 | i4 | i5)
        # Values are passed through verbatim: H1-H4 may be ranges ("123-456") and
        # I1-I5 are tag strings such as "<b 0xc7...><r 10>".
        eval "AWG_OBF_${key}=\$value"
        ;;
    j1 | j2 | j3 | itime)
        log "AmneziaWG parameter '$key' was removed in 2.0 and is rejected by the kernel module. Ignoring it" "warn"
        ;;
    fwmark | advancedsecurity | table | preup | postup | predown | postdown | saveconfig)
        log "AmneziaWG [Interface] key '$key' is not used by podkop. Ignoring it" "debug"
        ;;
    *)
        log "Unknown AmneziaWG [Interface] key '$key'. Ignoring it" "warn"
        ;;
    esac
}

_awg_parse_peer_key() {
    local key="$1"
    local value="$2"

    case "$key" in
    publickey) AWG_PEER_PUBLIC_KEY="$value" ;;
    presharedkey) AWG_PEER_PRESHARED_KEY="$value" ;;
    endpoint) _awg_split_endpoint "$value" ;;
    allowedips) AWG_PEER_ALLOWED_IPS="$(_awg_split_list "$value")" ;;
    persistentkeepalive) AWG_PEER_KEEPALIVE="$value" ;;
    *)
        log "Unknown AmneziaWG [Peer] key '$key'. Ignoring it" "warn"
        ;;
    esac
}

_awg_validate_parsed() {
    local missing=""

    [ -z "$AWG_PRIVATE_KEY" ] && missing="$missing [Interface] PrivateKey"
    [ -z "$AWG_ADDRESSES" ] && missing="$missing [Interface] Address"
    [ -z "$AWG_PEER_PUBLIC_KEY" ] && missing="$missing [Peer] PublicKey"
    [ -z "$AWG_PEER_ENDPOINT_HOST" ] && missing="$missing [Peer] Endpoint"

    if [ -n "$missing" ]; then
        log "AmneziaWG config is missing required fields:$missing" "error"
        return 1
    fi

    if [ -z "$AWG_PEER_ENDPOINT_PORT" ]; then
        AWG_PEER_ENDPOINT_PORT="51820"
        log "AmneziaWG endpoint has no port, defaulting to $AWG_PEER_ENDPOINT_PORT" "warn"
    fi

    if [ -z "$AWG_PEER_ALLOWED_IPS" ]; then
        AWG_PEER_ALLOWED_IPS="0.0.0.0/0"
        log "AmneziaWG peer has no AllowedIPs, defaulting to $AWG_PEER_ALLOWED_IPS" "warn"
    fi

    return 0
}

# Parse an AmneziaWG .conf passed as a single string argument.
amneziawg_parse_conf() {
    local conf="$1"

    _awg_reset

    local line section key value peer_count
    section=""
    peer_count=0

    # A heredoc keeps the loop in the current shell, so the globals survive it.
    while IFS= read -r line; do
        line="${line%%#*}"
        line="$(printf '%s' "$line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
        [ -z "$line" ] && continue

        case "$line" in
        \[*\])
            section="$(printf '%s' "$line" | tr -d '[]' | tr 'A-Z' 'a-z')"
            [ "$section" = "peer" ] && peer_count=$((peer_count + 1))
            continue
            ;;
        *=*) ;;
        *) continue ;;
        esac

        key="$(printf '%s' "${line%%=*}" | sed 's/[[:space:]]*$//' | tr 'A-Z' 'a-z')"
        value="$(printf '%s' "${line#*=}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
        [ -z "$value" ] && continue

        case "$section" in
        interface) _awg_parse_interface_key "$key" "$value" ;;
        peer)
            # podkop routes a single tunnel per interface; extra peers are ignored.
            [ "$peer_count" -le 1 ] && _awg_parse_peer_key "$key" "$value"
            ;;
        esac
    done <<EOF
$conf
EOF

    if [ "$peer_count" -gt 1 ]; then
        log "AmneziaWG config declares $peer_count peers, only the first one is used" "warn"
    fi

    _awg_validate_parsed
}

# Does this OpenWrt release have AmneziaWG 2.0 capable packages?
# Mirrors the version gate used by the upstream awg-openwrt installer.
amneziawg_supports_20() {
    local version major minor patch
    version="$(ubus call system board 2>/dev/null | jsonfilter -e '@.release.version' 2>/dev/null)"
    [ -z "$version" ] && return 1

    major="$(echo "$version" | cut -d '.' -f 1)"
    minor="$(echo "$version" | cut -d '.' -f 2)"
    patch="$(echo "$version" | cut -d '.' -f 3)"
    [ -z "$patch" ] && patch=0

    [ "$major" -gt 24 ] && return 0
    [ "$major" -eq 24 ] && [ "$minor" -gt 10 ] && return 0
    [ "$major" -eq 24 ] && [ "$minor" -eq 10 ] && [ "$patch" -ge 3 ] && return 0
    [ "$major" -eq 23 ] && [ "$minor" -eq 5 ] && [ "$patch" -ge 6 ] && return 0

    return 1
}

# Warn when the config uses 2.0-only features but the platform packages are 1.x.
_awg_warn_if_20_unsupported() {
    local uses_20="" param value

    for param in s3 s4 i1 i2 i3 i4 i5; do
        eval "value=\$AWG_OBF_${param}"
        [ -n "$value" ] && uses_20="yes"
    done
    for param in h1 h2 h3 h4; do
        eval "value=\$AWG_OBF_${param}"
        case "$value" in
        *-*) uses_20="yes" ;;
        esac
    done

    if [ -n "$uses_20" ] && ! amneziawg_supports_20; then
        log "Config uses AmneziaWG 2.0 features (S3/S4, I1-I5 or ranged H1-H4), but this OpenWrt release only has AmneziaWG 1.x packages. The tunnel may fail to start" "warn"
    fi
}

amneziawg_check_packages() {
    if command -v awg > /dev/null 2>&1; then
        return 0
    fi
    return 1
}

# Install kmod-amneziawg / amneziawg-tools / luci-proto-amneziawg via the upstream
# installer. '-e -n' keeps it non-interactive and skips its own interface setup,
# because podkop configures the interface from the .conf itself.
amneziawg_install_packages() {
    if amneziawg_check_packages; then
        echolog "✅ AmneziaWG packages are already installed"
        return 0
    fi

    local installer="/tmp/amneziawg-install.sh"

    echolog "📥 Installing AmneziaWG packages..."
    if ! wget -qO "$installer" "$AWG_INSTALLER_URL"; then
        echolog "❌ Failed to download the AmneziaWG installer from $AWG_INSTALLER_URL"
        return 1
    fi

    sh "$installer" -e -n
    rm -f "$installer"

    if ! amneziawg_check_packages; then
        echolog "❌ AmneziaWG packages were not installed"
        return 1
    fi

    echolog "✅ AmneziaWG packages installed"
    return 0
}

# Write the parsed config into /etc/config/network. Assumes amneziawg_parse_conf ran.
_awg_write_uci() {
    local iface="$1"

    local peer_section address allowed_ip param value

    uci -q delete "network.$iface"
    # Peer sections are anonymous; drop every existing one for this interface.
    while uci -q delete "network.@amneziawg_${iface}[0]"; do :; done

    uci set "network.$iface=interface"
    uci_set "network" "$iface" "proto" "amneziawg"
    uci_set "network" "$iface" "private_key" "$AWG_PRIVATE_KEY"
    [ -n "$AWG_LISTEN_PORT" ] && uci_set "network" "$iface" "listen_port" "$AWG_LISTEN_PORT"
    [ -n "$AWG_MTU" ] && uci_set "network" "$iface" "mtu" "$AWG_MTU"

    for address in $AWG_ADDRESSES; do
        uci_add_list "network" "$iface" "addresses" "$address"
    done

    for param in $AWG_OBFUSCATION_PARAMS; do
        eval "value=\$AWG_OBF_${param}"
        [ -n "$value" ] && uci_set "network" "$iface" "awg_$param" "$value"
    done

    peer_section="$(uci add network "amneziawg_${iface}")"
    uci_set "network" "$peer_section" "name" "${iface}_peer"
    uci_set "network" "$peer_section" "public_key" "$AWG_PEER_PUBLIC_KEY"
    [ -n "$AWG_PEER_PRESHARED_KEY" ] &&
        uci_set "network" "$peer_section" "preshared_key" "$AWG_PEER_PRESHARED_KEY"
    uci_set "network" "$peer_section" "endpoint_host" "$AWG_PEER_ENDPOINT_HOST"
    uci_set "network" "$peer_section" "endpoint_port" "$AWG_PEER_ENDPOINT_PORT"
    [ -n "$AWG_PEER_KEEPALIVE" ] &&
        uci_set "network" "$peer_section" "persistent_keepalive" "$AWG_PEER_KEEPALIVE"

    for allowed_ip in $AWG_PEER_ALLOWED_IPS; do
        uci_add_list "network" "$peer_section" "allowed_ips" "$allowed_ip"
    done

    # podkop decides what goes into the tunnel, so the peer must NOT install
    # routes of its own — AllowedIPs 0.0.0.0/0 would otherwise hijack everything.
    uci_set "network" "$peer_section" "route_allowed_ips" "0"

    uci commit "network"
}

# Import an AmneziaWG .conf into the given interface, idempotently.
# Arguments: interface name, .conf text.
amneziawg_sync_uci() {
    local iface="$1"
    local conf="$2"

    [ -z "$iface" ] && iface="$AWG_DEFAULT_INTERFACE"

    if [ -z "$conf" ]; then
        log "AmneziaWG config is empty for interface $iface" "error"
        return 1
    fi

    if ! amneziawg_check_packages; then
        log "AmneziaWG packages are missing. Run 'podkop awg_install' first" "error"
        return 1
    fi

    amneziawg_parse_conf "$conf" || return 1
    _awg_warn_if_20_unsupported

    local state_file current_hash new_hash
    state_file="$AWG_STATE_DIR/awg_${iface}.hash"
    new_hash="$(printf '%s' "$conf" | md5sum | cut -d ' ' -f 1)"

    if [ -r "$state_file" ]; then
        current_hash="$(cat "$state_file")"
        if [ "$current_hash" = "$new_hash" ] &&
            [ "$(uci -q get "network.$iface.proto")" = "amneziawg" ]; then
            log "AmneziaWG interface $iface is already up to date"
            return 0
        fi
    fi

    log "Applying AmneziaWG configuration to interface $iface"
    _awg_write_uci "$iface"

    mkdir -p "$AWG_STATE_DIR"
    printf '%s' "$new_hash" > "$state_file"

    ifup "$iface"
    log "AmneziaWG interface $iface has been brought up"

    return 0
}

# Apply the AmneziaWG config stored in a podkop section (vpn_type=amneziawg).
amneziawg_apply_section() {
    local section="$1"

    local connection_type vpn_type awg_config iface
    config_get connection_type "$section" "connection_type"
    config_get vpn_type "$section" "vpn_type" "interface"
    [ "$connection_type" = "vpn" ] || return 0
    [ "$vpn_type" = "amneziawg" ] || return 0

    config_get awg_config "$section" "awg_config"
    config_get iface "$section" "awg_interface" "$AWG_DEFAULT_INTERFACE"

    if [ -z "$awg_config" ]; then
        log "Section '$section' uses AmneziaWG but has no config. Skipping" "warn"
        return 0
    fi

    amneziawg_sync_uci "$iface" "$awg_config"
}

# Apply every AmneziaWG-backed section. Called on start and by the CLI verb.
amneziawg_apply() {
    config_foreach amneziawg_apply_section "section"
}

amneziawg_status() {
    if ! amneziawg_check_packages; then
        echolog "❌ AmneziaWG packages are not installed (run 'podkop awg_install')"
        return 1
    fi

    if amneziawg_supports_20; then
        echolog "✅ Platform packages support AmneziaWG 2.0"
    else
        echolog "⚠️ Platform packages are AmneziaWG 1.x only (OpenWrt 23.05.6+/24.10.3+ needed for 2.0)"
    fi

    local iface
    for iface in $(uci show network 2> /dev/null | sed -n "s/^network\.\([^.]*\)\.proto='amneziawg'$/\1/p"); do
        echolog "--- $iface ---"
        ifstatus "$iface" 2> /dev/null | jsonfilter -e '@.up' | grep -q true &&
            echolog "interface is up" || echolog "interface is down"
        awg show "$iface" 2> /dev/null
    done
}
