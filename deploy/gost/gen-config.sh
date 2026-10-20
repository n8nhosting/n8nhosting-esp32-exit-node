#!/bin/sh
# Print the base gost config with per-slot items inserted into the existing
# top-level lists (gost does not merge duplicate keys):
#   slot-N service  -> end of services:  (just before "chains:")
#   chain-slot-N    -> end of chains:    (just before "authers:")
#   auther-slot-N   -> end of authers:   (just before "limiters:")
# Usage: SLOT_COUNT=<n> PLUGIN_SECRET=<s> SLOT_SECRET=<p> sh gen-config.sh base.yaml
# PLUGIN_SECRET unset -> the literal ${PLUGIN_SECRET} placeholder is kept so the
# later awk render fills it in. SLOT_SECRET is substituted directly (alphanumeric).
set -eu

base=${1:?usage: gen-config.sh base.yaml}
count=${SLOT_COUNT:-30}
slot_secret=${SLOT_SECRET:-}
plugin_secret=${PLUGIN_SECRET:-}
if [ -z "$plugin_secret" ]; then
  plugin_secret='${PLUGIN_SECRET}'
fi

emit_services() {
  i=1
  while [ "$i" -le "$count" ]; do
    printf '  - name: slot-%s\n' "$i"
    printf '    addr: ":%s"\n' "$((3130 + i))"
    printf '    handler:\n'
    printf '      type: auto\n'
    printf '      auther: auther-slot-%s\n' "$i"
    printf '      limiter: limiter-0\n'
    printf '      chain: chain-slot-%s\n' "$i"
    printf '    listener:\n'
    printf '      type: tcp\n'
    printf '    observer: observer-0\n'
    printf '    metadata:\n'
    printf '      enableStats: true\n'
    printf '      observePeriod: 10s\n'
    i=$((i + 1))
  done
  printf '\n'
}

emit_chains() {
  i=1
  while [ "$i" -le "$count" ]; do
    printf '  - name: chain-slot-%s\n' "$i"
    printf '    hops:\n'
    printf '      - name: hop-slot-%s\n' "$i"
    printf '        nodes:\n'
    printf '          - name: node-slot-%s\n' "$i"
    printf '            addr: broker:1080\n'
    printf '            connector:\n'
    printf '              type: socks5\n'
    printf '              auth:\n'
    printf '                username: slot-%s\n' "$i"
    printf '                password: %s\n' "$slot_secret"
    printf '            dialer:\n'
    printf '              type: tcp\n'
    i=$((i + 1))
  done
  printf '\n'
}

emit_authers() {
  i=1
  while [ "$i" -le "$count" ]; do
    printf '  - name: auther-slot-%s\n' "$i"
    printf '    plugin:\n'
    printf '      type: http\n'
    printf '      addr: http://admin-api:8088/gost/%s/auth?slot=%s\n' "$plugin_secret" "$i"
    printf '      timeout: 5s\n'
    i=$((i + 1))
  done
  printf '\n'
}

while IFS= read -r line || [ -n "$line" ]; do
  case $line in
    chains:*)  emit_services ;;
    authers:*) emit_chains ;;
    limiters:*) emit_authers ;;
  esac
  printf '%s\n' "$line"
done < "$base"
