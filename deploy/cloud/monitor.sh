#!/bin/sh
# Local collection/evaluation is the default. Only host-owned configuration
# enables external notifications; never source a user-controlled environment.
set -eu
umask 077
base=${GATEWAY_BASE:-/opt/mcp-gateway}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
set -- python3 "$script_dir/monitor.py" --base "$base" \
  --workspace "${GATEWAY_MONITOR_WORKSPACE:-team}" \
  --state "$base/monitor-state/metrics.json"
if [ -n "${GATEWAY_MONITOR_RULES:-}" ]; then
  set -- "$@" --rules "$GATEWAY_MONITOR_RULES"
fi
if [ -n "${GATEWAY_MONITOR_DELIVERY_CONFIG:-}" ]; then
  set -- "$@" --delivery-config "$GATEWAY_MONITOR_DELIVERY_CONFIG"
fi
exec "$@"
