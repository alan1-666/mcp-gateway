#!/bin/sh
# Default is local-only encrypted backup. An explicit destination adds verified
# offhost transfer; only last-offhost.json proves that offhost transfer succeeded.
set -eu
umask 077
base=${GATEWAY_BASE:-/opt/mcp-gateway}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
set -- python3 "$script_dir/cloud-backup.py" create --base "$base" \
  --key "${GATEWAY_BACKUP_KEY_FILE:-$base/backup-key}" \
  --temp-dir "${GATEWAY_BACKUP_TEMP_DIR:-/dev/shm}"
if [ -n "${GATEWAY_BACKUP_OFFHOST_CONFIG:-}" ]; then
  set -- "$@" --offhost-config "$GATEWAY_BACKUP_OFFHOST_CONFIG" --scheduled
fi
exec "$@"
