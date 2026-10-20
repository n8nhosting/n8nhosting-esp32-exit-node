FROM gogost/gost:3
COPY gost.yaml /etc/gost/gost.yaml
COPY gen-config.sh /etc/gost/gen-config.sh
# Generate per-slot services (gen-config.sh), then render ${API_USER}/${API_PASS} from env at start (awk -v is delimiter-safe),
# then run gost with the rendered config. Use alphanumeric secrets.
ENTRYPOINT ["/bin/sh","-c"]
CMD ["PLUGIN_SECRET=\"$GOST_PLUGIN_SECRET\" SLOT_SECRET=\"$SLOT_SECRET\" SLOT_COUNT=\"${SLOT_COUNT:-30}\" sh /etc/gost/gen-config.sh /etc/gost/gost.yaml > /tmp/gost.full.yaml && awk -v u=\"$API_USER\" -v p=\"$API_PASS\" -v s=\"$GOST_PLUGIN_SECRET\" '{gsub(/[$][{]API_USER[}]/,u); gsub(/[$][{]API_PASS[}]/,p); gsub(/[$][{]PLUGIN_SECRET[}]/,s); print}' /tmp/gost.full.yaml > /tmp/gost.yaml && exec /bin/gost -C /tmp/gost.yaml"]
