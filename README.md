# igcrm — картки клієнтів з Instagram Direct

Ти переписуєшся з клієнтами як завжди, з телефону. Сервіс:

1. ловить усі повідомлення Direct (вхідні й твої відповіді) через вебхуки Meta → Postgres;
2. створює контакт і підтягує @username;
3. через 3 хв тиші в діалозі віддає останні повідомлення в LLM і заповнює картку:
   **ПІБ, телефон, місто, відділення НП, розміри, що хотів купити**;
4. позначає картку `needs_review = true`, щоб ти глянув, що змінилось (історія — в `extractions`);
5. сам оновлює токен Instagram і шле алерт у Telegram, якщо не вийшло.

Замовлення ведеш у Directus: обираєш клієнта, вписуєш товар і суму — ПІБ/телефон/відділення
підставляються з картки автоматично.

```
Instagram ─webhook─▶ igcrm (Go) ─▶ Postgres ◀─ Directus (UI)
                        └─▶ LLM (OpenAI-сумісний API)
           Caddy (HTTPS, Let's Encrypt) перед усім
```

## Таблиці

| таблиця | що там |
|---|---|
| `contacts` | картка клієнта. `auto_update=false` — сервіс більше не чіпає поля; `notes` — твоє, сервіс не пише |
| `messages` | уся переписка (`in` / `out`) |
| `orders` | замовлення: статус, товари, сума, ТТН, отримувач |
| `extractions` | журнал: що витягнув LLM і які поля змінив (старе → нове) |
| `ig_token` | токен Instagram. **Сховай у Directus** |

## Розгортання

Потрібно: VPS (≥2 GB RAM, Directus на Node їсть ~400 MB), домен з A-записом на IP сервера, відкриті 80/443.

```sh
git clone … /opt/igcrm && cd /opt/igcrm   # або scp цієї папки
cp .env.example .env && nano .env          # заповни все, крім IG_* (поки що)
docker compose up -d --build
docker compose logs -f igcrm
```

Відкрий `https://DOMAIN` → логін Directus (email/пароль з `.env`).

## Meta / Instagram (один раз, ~30–60 хв клацання)

1. Instagram акаунт → **професійний** (бізнес або автор).
2. developers.facebook.com → Create App → use case **"Manage messaging & content on Instagram"**.
3. App roles → додай свій Instagram як **Instagram tester** і прийми запрошення в Instagram
   (на instagram.com: Налаштування → Дозволи застосунків і сайтів → Запрошення тестувальника;
   назви пунктів Meta змінює, шукай «tester invites»).
4. Instagram → **API setup with Instagram login**:
   - **Generate token** для свого акаунта → `IG_ACCESS_TOKEN` у `.env`;
   - Business login settings → **Instagram app secret** → `IG_APP_SECRET`
     (саме цей, а не App secret з Settings → Basic — інакше вебхуки падатимуть з 401);
   - `docker compose up -d` (перезапуск з новими змінними);
   - **Configure webhooks**: Callback URL `https://DOMAIN/ig/webhook`, Verify token = `IG_VERIFY_TOKEN`
     → Verify and save → підпишись на поле **messages** (+ `message_echoes`, якщо є окремо).
5. В Instagram: Налаштування → Повідомлення → **Підключені інструменти → Дозволити доступ до повідомлень**.
   Без цього вебхуки не приходять — найчастіша причина «все налаштував, а тиша».
6. Напиши собі з іншого акаунта → у `docker compose logs igcrm` має з'явитись `msg=message dir=in`.

App Review / Business Verification **не потрібні**: Standard Access працює для акаунтів, які є
в ролях апки. Якщо Meta вимагатиме Live mode — потрібен лише URL privacy policy (будь-яка сторінка).

### Старі переписки (опційно)

```sh
docker compose run --rm igcrm backfill
```

Через Conversations API заливає діалоги, які вже є в Direct, і ставить їх у чергу на екстракцію.
Meta віддає лише останні повідомлення кожного діалогу (не всю історію). Це best-effort: я не міг
перевірити на живому акаунті, тож якщо впаде — скинь помилку.

## LLM

Будь-який OpenAI-сумісний `/chat/completions`: OpenAI, OpenRouter, Ollama тощо.
Потрібна дешева «mini»-модель; на кілька десятків діалогів на день це центи на місяць.
Без `LLM_BASE_URL`/`LLM_MODEL` сервіс просто зберігає переписку.

Що варто знати:
- ПІБ, телефони й адреси клієнтів ідуть до провайдера LLM. Якщо це неприйнятно — локальна модель
  (Ollama), але на VPS за €5 вона не влізе.
- Модель помиляється. Тому `needs_review` + журнал `extractions` + `auto_update=false` для ручного режиму.
- Порожні відповіді моделі ніколи не стирають наявні дані; телефон нормалізується до `+380…`,
  відділення — до `Відділення №45` / `Поштомат №1234`.

## Directus: первинне налаштування (~15 хв)

Таблиці сервісу з'являються в Settings → Data Model як некеровані — клікни кожну, щоб Directus її підхопив.

1. **contacts** → додай поле типу *One to Many* «Замовлення» → `orders.contact_id`
   і ще одне «Переписка» → `messages.contact_id` (сортування за `sent_at`).
   Display template колекції: `{{username}} {{full_name}}`.
2. **orders** → поле `contact_id` інтерфейс *Many to One* (вибір клієнта);
   `status` → інтерфейс Dropdown з варіантами: new, awaiting_payment, paid, shipped, delivered, returned, cancelled.
3. **ig_token**, **schema_migrations** → Hidden.
4. У contacts збережи закладку з фільтром `needs_review = true` → «Перевірити».

Після цього картка клієнта = поля + список замовлень + переписка на одній сторінці.

## Обслуговування

- **Бекапи**: `scripts/backup.sh` у cron (рядок усередині файлу) + копія поза сервером (rclone → B2).
  Без цього пункту вся система — до першого збою диска.
- **Токен**: оновлюється автоматично за 15 днів до кінця (діє 60 днів). Алерт у Telegram, якщо не вдалося.
  Новий токен вручну: вписати в `.env`, `IG_ACCESS_TOKEN_FORCE=1`, перезапустити, повернути `0`.
- **Версія API**: `IG_API_VERSION`. Meta раз на ~2 роки вимикає старі версії — тоді підняти.
- **Медіа**: URL вкладень у `messages.attachments` тимчасові (CDN Meta), фото не зберігаються.

## Розробка

```sh
go test ./...                                          # юніт-тести
TEST_DATABASE_URL=postgres://… go test ./internal/store/   # інтеграційний (затирає схему public!)
```
Для живих вебхуків локально: `cloudflared tunnel --url http://localhost:8080`.
