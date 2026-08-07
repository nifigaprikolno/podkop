# Shared .env editing helpers, sourced rather than executed. The caller sets
# ENV_FILE first; set_kv then keeps one backup per run and reports every write,
# so a caller only has to decide what to write.
#
# Writes are idempotent: a key that already holds the wanted value is left
# alone, a commented-out key is uncommented in place, an absent key is
# appended. Nothing else in the file is touched.

ENV_KV_BACKED_UP=""
ENV_KV_CHANGED=""

get_kv() {
    KEY="$1" awk '
        BEGIN { key = ENVIRON["KEY"]; value = "" }
        {
            line = $0
            sub(/^[ \t]+/, "", line)
            if (index(line, key "=") == 1) { value = substr(line, length(key) + 2) }
        }
        END { print value }
    ' "$ENV_FILE"
}

set_kv() {
    key="$1"
    value="$2"

    if [ "$(get_kv "$key")" = "$value" ]; then
        printf '  unchanged  %s\n' "$key"
        return 0
    fi

    if [ -z "$ENV_KV_BACKED_UP" ]; then
        cp -p "$ENV_FILE" "$ENV_FILE.bak"
        ENV_KV_BACKED_UP="yes"
    fi

    tmp=$(mktemp "$ENV_FILE.XXXXXX")
    chmod 600 "$tmp"
    KEY="$key" VALUE="$value" awk '
        BEGIN { key = ENVIRON["KEY"]; value = ENVIRON["VALUE"]; done = 0 }
        {
            probe = $0
            sub(/^[ \t]*#*[ \t]*/, "", probe)
            if (index(probe, key "=") == 1) {
                if (!done) { print key "=" value; done = 1 }
                next
            }
            print
        }
        END { if (!done) print key "=" value }
    ' "$ENV_FILE" >"$tmp"
    mv "$tmp" "$ENV_FILE"

    printf '  set        %s=%s\n' "$key" "$value"
    ENV_KV_CHANGED="yes"
}
