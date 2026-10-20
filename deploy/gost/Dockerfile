FROM gogost/gost:3
COPY gost.yaml /etc/gost/gost.yaml
# Render ${API_USER}/${API_PASS} from env at start (awk -v is delimiter-safe),
# then run gost with the rendered config. Use alphanumeric secrets.
ENTRYPOINT ["/bin/sh","-c"]
CMD ["awk -v u=\"$API_USER\" -v p=\"$API_PASS\" -v s=\"$GOST_PLUGIN_SECRET\" '{gsub(/[$][{]API_USER[}]/,u); gsub(/[$][{]API_PASS[}]/,p); gsub(/[$][{]PLUGIN_SECRET[}]/,s); print}' /etc/gost/gost.yaml > /tmp/gost.yaml && exec /bin/gost -C /tmp/gost.yaml"]
