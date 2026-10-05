#!/bin/sh
set -eu
umask 077
base=/opt/mcp-gateway
mkdir -p "$base/backups"
exec 9>"$base/backups/.lock"
flock -n 9 || exit 0
stamp=$(date -u +%Y%m%dT%H%M%SZ)
target="$base/backups/gateway-$stamp.dump"
trap 'rm -f "$target.tmp"' EXIT INT TERM
docker compose --env-file "$base/cloud.env" -f "$base/current/deploy/compose/cloud.yaml" exec -T postgres pg_dump -U gateway -d gateway -Fc > "$target.tmp"
test -s "$target.tmp"
docker compose --env-file "$base/cloud.env" -f "$base/current/deploy/compose/cloud.yaml" exec -T postgres pg_restore --list < "$target.tmp" > /dev/null
mv "$target.tmp" "$target"
find "$base/backups" -maxdepth 1 -name 'gateway-*.dump' -type f -mtime +7 -delete
echo "Database backup completed: $stamp"
