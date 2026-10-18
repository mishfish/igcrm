#!/bin/sh
# Щоденний бекап БД. Cron: 30 3 * * * cd /opt/igcrm && ./scripts/backup.sh >> backups/backup.log 2>&1
set -eu
cd "$(dirname "$0")/.."
mkdir -p backups
f="backups/crm-$(date +%Y%m%d-%H%M).dump"
docker compose exec -T postgres pg_dump -U crm -Fc crm > "$f"
find backups -name 'crm-*.dump' -mtime +14 -delete
echo "$(date -Is) ok $f $(du -h "$f" | cut -f1)"

# Копія поза сервером (обов'язково, інакше бекап помре разом з VPS), напр. rclone → Backblaze B2:
# rclone copy "$f" b2:my-bucket/igcrm/
