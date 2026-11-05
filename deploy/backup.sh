#!/bin/sh
set -eu
umask 077
backup_path=${1:-"apphub-$(date +%Y%m%d-%H%M%S).dump"}
docker compose exec -T postgres sh -c 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup_path"
printf 'Backup saved: %s\n' "$backup_path"
