# shellcheck disable=SC2034

PODKOP_VERSION="__COMPILED_VERSION_VARIABLE__"
## Common
PODKOP_CONFIG="/etc/config/podkop"
RESOLV_CONF="/etc/resolv.conf"
DNS_RESOLVERS="1.1.1.1 1.0.0.1 8.8.8.8 8.8.4.4 9.9.9.9 9.9.9.11 94.140.14.14 94.140.15.15 208.67.220.220 208.67.222.222 77.88.8.1 77.88.8.8"
CHECK_PROXY_IP_DOMAIN="ip.podkop.fyi"
FAKEIP_TEST_DOMAIN="fakeip.podkop.fyi"
TMP_SING_BOX_FOLDER="/tmp/sing-box"
TMP_RULESET_FOLDER="$TMP_SING_BOX_FOLDER/rulesets"
CLOUDFLARE_OCTETS="8.47 162.159 188.114" # Endpoints https://github.com/ampetelin/warp-endpoint-checker
JQ_REQUIRED_VERSION="1.7.1"
COREUTILS_BASE64_REQUIRED_VERSION="9.7"
RT_TABLE_NAME="podkop"

## nft
NFT_TABLE_NAME="PodkopTable"
NFT_LOCALV4_SET_NAME="localv4"
NFT_COMMON_SET_NAME="podkop_subnets"
NFT_DISCORD_SET_NAME="podkop_discord_subnets"
NFT_INTERFACE_SET_NAME="interfaces"
NFT_FAKEIP_MARK="0x00100000"
NFT_OUTBOUND_MARK="0x00200000"

## sing-box
SB_REQUIRED_VERSION="1.12.0"
# DNS
SB_DNS_SERVER_TAG="dns-server"
SB_FAKEIP_DNS_SERVER_TAG="fakeip-server"
SB_FAKEIP_INET4_RANGE="198.18.0.0/15"
SB_BOOTSTRAP_SERVER_TAG="bootstrap-dns-server"
SB_FAKEIP_DNS_RULE_TAG="fakeip-dns-rule-tag"
SB_INVERT_FAKEIP_DNS_RULE_TAG="invert-fakeip-dns-rule-tag"
# Inbounds
SB_TPROXY_INBOUND_TAG="tproxy-in"
SB_TPROXY_INBOUND_ADDRESS="127.0.0.1"
SB_TPROXY_INBOUND_PORT=1602
SB_DNS_INBOUND_TAG="dns-in"
SB_DNS_INBOUND_ADDRESS="127.0.0.42"
SB_DNS_INBOUND_PORT=53
SB_SERVICE_MIXED_INBOUND_TAG="service-mixed-in"
SB_SERVICE_MIXED_INBOUND_ADDRESS="127.0.0.1"
SB_SERVICE_MIXED_INBOUND_PORT=4534
# Outbounds
SB_DIRECT_OUTBOUND_TAG="direct-out"
# Route
SB_REJECT_RULE_TAG="reject-rule-tag"
SB_EXCLUSION_RULE_TAG="exclusion-rule-tag"
# Experimental
SB_CLASH_API_CONTROLLER_PORT=9090

## AmneziaWG (kernel module path: kmod-amneziawg + proto 'amneziawg')
AWG_DEFAULT_INTERFACE="awg0"
AWG_STATE_DIR="/etc/podkop"
# Upstream package installer. Run with '-e -n' to install packages only, without
# the interactive interface setup (podkop imports the .conf itself instead).
AWG_INSTALLER_URL="https://raw.githubusercontent.com/Slava-Shchipunov/awg-openwrt/master/amneziawg-install.sh"
# Obfuscation parameters of AmneziaWG 2.0, in .conf/UCI order. S3/S4 and I1-I5 are
# 2.0 additions; H1-H4 became ranges ("123-456") in 2.0.
AWG_OBFUSCATION_PARAMS="jc jmin jmax s1 s2 s3 s4 h1 h2 h3 h4 i1 i2 i3 i4 i5"
# Removed in AmneziaWG 2.0 and unsupported by the kernel module — never emit these,
# the interface would fail to come up.
AWG_REMOVED_PARAMS="j1 j2 j3 itime"

## Direct Russian domain zones (.ru / .su / .рф). '.xn--p1ai' is the punycode form
## of '.рф' (sing-box domain_suffix and the domain validator accept ASCII only).
## Used when settings.direct_ru_zones is enabled to force these zones straight out.
DIRECT_RU_ZONE_SUFFIXES='[".ru",".su",".xn--p1ai"]'

## Lists
GITHUB_RAW_URL="https://raw.githubusercontent.com/itdoginfo/allow-domains/main"
SRS_MAIN_URL="https://github.com/itdoginfo/allow-domains/releases/latest/download"
SUBNETS_TWITTER="${GITHUB_RAW_URL}/Subnets/IPv4/twitter.lst"
SUBNETS_META="${GITHUB_RAW_URL}/Subnets/IPv4/meta.lst"
SUBNETS_DISCORD="${GITHUB_RAW_URL}/Subnets/IPv4/discord.lst"
SUBNETS_ROBLOX="${GITHUB_RAW_URL}/Subnets/IPv4/roblox.lst"
SUBNETS_TELERAM="${GITHUB_RAW_URL}/Subnets/IPv4/telegram.lst"
SUBNETS_CLOUDFLARE="${GITHUB_RAW_URL}/Subnets/IPv4/cloudflare.lst"
SUBNETS_HETZNER="${GITHUB_RAW_URL}/Subnets/IPv4/hetzner.lst"
SUBNETS_OVH="${GITHUB_RAW_URL}/Subnets/IPv4/ovh.lst"
SUBNETS_DIGITALOCEAN="${GITHUB_RAW_URL}/Subnets/IPv4/digitalocean.lst"
SUBNETS_CLOUDFRONT="${GITHUB_RAW_URL}/Subnets/IPv4/cloudfront.lst"
COMMUNITY_SERVICES="russia_inside russia_outside ukraine_inside geoblock block porn news anime youtube hdrezka tiktok google_ai google_play hodca discord meta twitter cloudflare cloudfront digitalocean hetzner ovh telegram roblox"