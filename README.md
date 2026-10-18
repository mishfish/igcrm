# igcrm — customer cards from Instagram Direct

You keep chatting with customers as usual, from your phone. The service:

1. catches every Direct message (incoming and your replies) via Meta webhooks → Postgres;
2. creates a contact and fetches their @username;
3. after 3 minutes of silence in the thread, sends the recent messages to an LLM and fills in the card:
   **full name, phone, city, Nova Poshta branch, sizes, what they wanted to buy**;
4. flags the card `needs_review = true` so you can check what changed (history is in `extractions`);
5. if the customer explicitly confirmed they want to order, creates an `orders` row for you
   (item, amount if stated, recipient info from the card) — see **Auto-created orders** below;
6. refreshes the Instagram token itself and sends a Telegram alert if it fails.

You manage orders in Directus: pick the customer, enter the item and amount — name/phone/branch
are filled in automatically from the card.

```
Instagram ─webhook─▶ igcrm (Go) ─▶ Postgres ◀─ Directus (UI)
                        └─▶ LLM (OpenAI-compatible API)
           Caddy (HTTPS, Let's Encrypt) in front of everything
```

## Tables

| table | what's in it |
|---|---|
| `contacts` | customer card. `auto_update=false` — the service stops touching the fields; `notes` is yours, the service never writes there |
| `messages` | the full conversation (`in` / `out`) |
| `orders` | orders: status, items, amount, tracking number, recipient |
| `extractions` | log of what the LLM extracted and which fields it changed (old → new) |
| `ig_token` | Instagram token. **Hide it in Directus** |

## Deployment

You need: a VPS (≥2 GB RAM, Directus on Node eats ~400 MB), a domain with an A record pointing
at the server's IP, ports 80/443 open.

```sh
git clone … /opt/igcrm && cd /opt/igcrm   # or scp this folder
cp .env.example .env && nano .env          # fill in everything except IG_* (for now)
docker compose up -d --build
docker compose logs -f igcrm
```

Open `https://DOMAIN` → Directus login (email/password from `.env`).

## Meta / Instagram (one-time, ~30–60 min of clicking)

1. Instagram account → **professional** (business or creator).
2. developers.facebook.com → Create App → use case **"Manage messaging & content on Instagram"**.
3. App roles → add your Instagram as an **Instagram tester** and accept the invite on Instagram
   (on instagram.com: Settings → Apps and websites permissions → Tester invites;
   Meta renames these menu items often, search for "tester invites").
4. Instagram → **API setup with Instagram login**:
   - **Generate token** for your account → `IG_ACCESS_TOKEN` in `.env`;
   - Business login settings → **Instagram app secret** → `IG_APP_SECRET`
     (this one, not the App secret from Settings → Basic — otherwise webhooks fail with 401);
   - `docker compose up -d` (restart with the new env vars);
   - **Configure webhooks**: Callback URL `https://DOMAIN/ig/webhook`, Verify token = `IG_VERIFY_TOKEN`
     → Verify and save → subscribe to the **messages** field (+ `message_echoes` if listed separately).
5. On Instagram: Settings → Messages → **Connected tools → Allow access to messages**.
   Without this, webhooks never arrive — the most common "I set everything up and it's silent" cause.
6. Message yourself from another account → `docker compose logs igcrm` should show `msg=message dir=in`.

App Review / Business Verification are **not needed**: Standard Access works for accounts that
hold a role on the app. If Meta asks for Live mode, it only needs a privacy policy URL (any page) —
and the app must actually be **published** for webhooks to fire at all, not just "configured".

### Old conversations (optional)

```sh
docker compose run --rm igcrm backfill
```

Uses the Conversations API to pull in threads that already exist in Direct, and queues them for
extraction. Meta only returns the most recent messages of each thread (not the full history).
This is best-effort: I couldn't test it against a live account, so report back if it breaks.

## LLM

Two options, `LLM_PROVIDER`:

- `openai` (default) — any OpenAI-compatible `/chat/completions`: OpenAI, OpenRouter, Ollama, etc.
  Needs `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`.
- `claude` — [Claude API](https://console.anthropic.com) directly, no `LLM_BASE_URL`.
  `LLM_API_KEY` = key from console.anthropic.com, `LLM_MODEL=claude-haiku-5-5` (a cheap model, plenty for this kind of extraction).

A cheap model is enough; for a few dozen conversations a day this costs cents a month.
Without `LLM_MODEL` (and a key/endpoint for the chosen provider) the service just stores the conversation.

Worth knowing:
- Customer names, phone numbers and addresses go to the LLM provider. If that's not acceptable,
  a local model (Ollama) is an option, but it won't fit on a €5 VPS.
- The model makes mistakes. Hence `needs_review` + the `extractions` log + `auto_update=false` for manual mode.
- Empty model answers never erase existing data; phone numbers are normalized to `+380…`,
  branches to `Branch No. 45` / `Parcel locker No. 1234`.

### Auto-created orders

When the model sees an explicit "yes, I'll take it" in the conversation (not just interest or a price
question), it creates an `orders` row for you: item/size from `last_interest`, amount if a sum was
stated, `payment_type` if discussed (`full_prepay` / `partial_prepay`, e.g. 50/50 / `cod`), recipient
fields filled in from the card as usual. The row is marked `auto_created = true` and starts in status
`new`, same as if you'd entered it by hand — **payment is never set automatically**, you still mark
`paid` yourself once you've actually confirmed it.

To avoid duplicate orders while a conversation keeps going, a new one is only auto-created if the
customer has no order currently sitting in `new` / `awaiting_payment`. Once you move an order past
that (paid, cancelled, etc.), the next confirmed order creates a fresh row.

## Directus: initial setup (~15 min)

The service's tables show up in Settings → Data Model as untracked — click each one so Directus picks it up.

1. **contacts** → add a *One to Many* field "Orders" → `orders.contact_id`
   and another "Messages" → `messages.contact_id` (sorted by `sent_at`).
   Collection display template: `{{username}} {{full_name}}`.
2. **orders** → `contact_id` field, *Many to One* interface (pick a customer);
   `status` → Dropdown interface with options: new, awaiting_payment, paid, shipped, delivered, returned, cancelled.
3. **ig_token**, **schema_migrations** → Hidden.
4. On contacts, save a bookmark with the filter `needs_review = true` → "To review".
5. **Conversation as chat**: `directus-extensions/chat-history` — a custom interface that renders
   the related messages as chat bubbles right inside the customer card. Build and deploy:
   ```sh
   cd directus-extensions/chat-history && npm install && npm run build
   scp -r package.json dist root@<server>:/opt/igcrm/chat-history-ext/
   ssh root@<server> 'docker cp /opt/igcrm/chat-history-ext igcrm-directus-1:/directus/extensions/chat-history \
     && docker exec -u root igcrm-directus-1 chown -R node:node /directus/extensions/chat-history \
     && docker compose restart directus'
   ```
   After deploying, activate it on the `contacts.messages` field: `PATCH /fields/contacts/messages {"meta":{"interface":"chat-history"}}`.
6. **Conversation on the order form**: `directus-extensions/order-chat` — same idea, but reads
   `contact_id` from the order form itself (there's no direct relation from orders to messages) via
   `inject('values')`, so it works from an order without navigating to the contact. Build and deploy
   the same way as chat-history (own extension folder, own container path). Then add it as a new
   field on `orders`:
   ```sh
   curl -X POST https://DOMAIN/fields/orders -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"field":"client_chat","type":"alias","meta":{"interface":"order-chat","special":["alias","no-data"],"width":"full"}}'
   ```

After this, a customer card = fields + list of orders + conversation, all on one page.

## Maintenance

- **Backups**: `scripts/backup.sh` in cron (the line is inside the file) + an offsite copy
  (rclone → Google Drive, remote `gdrive:`). Skip this and the whole system is one disk failure away from gone.
- **Alerts**: `TG_BOT_TOKEN`/`TG_CHAT_ID` in `.env` — a Telegram bot (create via @BotFather) that
  sends alerts when the IG token refresh fails and when a backup fails.
- **Uptime**: an external monitor (UptimeRobot) on `/healthz`, since the service can't notify
  anyone about its own downtime.
- **Token**: refreshes automatically 15 days before expiry (lasts 60 days). Telegram alert if it fails.
  Manual refresh: paste the new token into `.env`, set `IG_ACCESS_TOKEN_FORCE=1`, restart, then set it back to `0`.
- **API version**: `IG_API_VERSION`. Meta retires old versions roughly every ~2 years — bump it then.
- **Media**: attachment URLs in `messages.attachments` are temporary (Meta's CDN); photos aren't stored.

## Development

```sh
go test ./...                                          # unit tests
TEST_DATABASE_URL=postgres://… go test ./internal/store/   # integration test (wipes the public schema!)
```
For live webhooks locally: `cloudflared tunnel --url http://localhost:8080`.
