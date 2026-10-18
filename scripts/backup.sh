#!/bin/bash
# Щоденний бекап БД. Cron: 30 3 * * * cd /opt/igcrm && ./scripts/backup.sh >> backups/backup.log 2>&1
set -eu
cd "$(dirname "$0")/.."

set -a
. ./.env
set +a

alert() {
	[ -n "${TG_BOT_TOKEN:-}" ] && [ -n "${TG_CHAT_ID:-}" ] && \
		curl -s -m 15 "https://api.telegram.org/bot${TG_BOT_TOKEN}/sendMessage" \
			--data-urlencode "chat_id=${TG_CHAT_ID}" \
			--data-urlencode "text=igcrm backup: $1" >/dev/null || true
}
trap 'alert "ПОМИЛКА бекапу ($(date -Is))"' ERR

mkdir -p backups
f="backups/crm-$(date +%Y%m%d-%H%M).dump"
docker compose exec -T postgres pg_dump -U crm -Fc crm > "$f"
find backups -name 'crm-*.dump' -mtime +14 -delete

# Копія поза сервером (обов'язково, інакше бекап помре разом з VPS)
rclone copy "$f" gdrive:igcrm-backups/

echo "$(date -Is) ok $f $(du -h "$f" | cut -f1)"
