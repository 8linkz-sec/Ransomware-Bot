# Ransomware News Bot

A high-performance bot written in **Go** that delivers ransomware alerts via **Discord**, **Slack**, and approved Slack-compatible webhooks. The bot fetches data from the ransomware.live API and multiple RSS feeds, providing regular cybersecurity updates to your communication channels.

The original bot from vx-underground no longer works, so I built a new one (<https://github.com/vxunderground/ThreatIntelligenceDiscordBot>).

⚠️ Disclaimer
This bot is 100% vibe-coded - I provide no guarantee for 100% security.

<img src="https://github.com/user-attachments/assets/699b63de-043e-40cc-9fb8-e396cf55ce79" alt="Screenshot of Ransomware News Bot notification output" width="524" height="377">

## Contents

- [Quick Start](#quick-start)
- [CLI Flags](#cli-flags)
- [Prerequisites](#prerequisites)
- [Configuration](#configuration)
- [Config Hot-Reload](#config-hot-reload)
- [Docker](#docker)
- [Troubleshooting](#troubleshooting)
- [Platform Support](#platform-support)
- [Acknowledgments](#acknowledgments)

Operator detail (endpoint-order internals, healthcheck internals, audit-log sizing, special
builds) is in the [Operations Guide](OPERATIONS.md).

## Quick Start

Requirements: Git, Go 1.27.x, and optionally Docker with Docker Compose.

### Run locally

```bash
git clone https://github.com/8linkz-sec/Ransomware-Bot.git
cd Ransomware-Bot
go mod download
go test ./...
cp configs/config_general.example.json configs/config_general.json
# add your API key and webhook URL to configs/config_general.json, then:
go run . --check-config --config-dir ./configs
go run . --dry-run --config-dir ./configs --data-dir ./data
```

Run a single package with `go test ./internal/<package> -count=1`. Do not commit local
credentials, status files, logs, or private feed URLs.

### Run with Docker

```bash
cp configs/config_general.example.json configs/config_general.json   # then add your credentials
mkdir -p logs data
chown -R "${BOT_UID:-1000}:${BOT_GID:-1000}" logs data   # or: pwsh ./scripts/prepare-compose-host.ps1
RANSOMWARE_BOT_IMAGE_TAG="latest" docker compose up -d --build
```

Updates and rollbacks are described in [Build, update and roll back](#build-update-and-roll-back).

## CLI Flags

| Flag | Description |
| --- | --- |
| `--config-dir` | Directory containing the JSON config files (default: `./configs`) |
| `--data-dir` | Directory for persistent data; overrides the config and the `DATA_DIR` env |
| `--check-config` | Validate the configuration and exit 0 (`Configuration valid`) or 1; also checks the [endpoint order](#webhook-endpoint-order) against `data_dir`, so pass the production `--data-dir` |
| `--accept-destination-remap` | Accept a changed [endpoint order](#webhook-endpoint-order) once on a normal start; ignored by `--check-config` and `--healthcheck` |
| `--healthcheck` | Check runtime health and exit 0 or 1; never creates `data_dir` ([what it checks](OPERATIONS.md#healthcheck)) |
| `--dry-run` | Run one cycle and log `[DRY-RUN]` previews without sending or changing status; exits 1 on a genuine API or RSS failure |
| `--list-dead-letter` | List terminally failed deliveries with recovery details and exit |
| `--locale` | CLI output language, `en` or `de` (also via `BOT_LOCALE` env) |
| `--version` | Show version information and exit, e.g. `Ransomware News Bot v1.2.1 (commit abc123, built 2026-09-05T12:00:00Z)` |
| `--help`, `-h` | Print usage to stderr and exit 0 (an unknown flag exits 2) |

## Prerequisites

### API Key

The ransomware.live API allows **3,000 calls per day** (API Pro). You need to request an API key at: <https://www.ransomware.live/api>

### Discord Webhooks

1. Go to your Discord server settings
2. Navigate to "Integrations" → "Webhooks"
3. Click "Create Webhook"
4. Choose the channel and copy the webhook URL
5. Add the URL to the configuration (see [Configuration](#configuration))

### Slack Webhooks

1. Go to <https://api.slack.com/apps>
2. Create a new app or select an existing one
3. Navigate to "Incoming Webhooks" and activate it
4. Click "Add New Webhook to Workspace"
5. Select the channel and copy the webhook URL
6. Add the URL to the configuration (see [Configuration](#configuration))

⚠️ **Security Warning**: Never share your webhook URLs with others - they provide direct access to post messages in your channels.

## Configuration

### 1. General Configuration (configs/config_general.json)

The repository tracks `configs/config_general.example.json` as a template. Copy it to
`configs/config_general.json` and put real API keys and webhook URLs only in that local file. Do
not commit it after adding real credentials.

Minimal first run for Discord ransomware alerts:

```json
{
  "api_key": "YOUR_RANSOMWARE_LIVE_API_KEY",
  "discord_webhooks": {
    "ransomware": {
      "enabled": true,
      "url": "https://discord.com/api/webhooks/YOUR_WEBHOOK_ID/YOUR_WEBHOOK_TOKEN"
    }
  }
}
```

For Slack, use the same `api_key` and enable `slack_webhooks.ransomware.url` with an incoming
webhook URL. The full example below shows optional logging, polling, retry, quiet-hours, and
multi-channel settings:

```json
{
  "log_level": "INFO",
  "log_file_path": "./logs/bot.log",
  "log_rotation": { "max_size_mb": 10, "max_backups": 30, "max_age_days": 90, "compress": true },
  "max_rss_workers": 5,
  "api_key": "",
  "api_base_url": "https://api-pro.ransomware.live",
  "api_request_timeout": "30s",
  "api_max_retries": 3,
  "api_retry_delay": "1s",
  "api_poll_interval": "1h",
  "api_check_timeout": "5m",
  "rss_poll_interval": "30m",
  "rss_check_timeout": "10m",
  "rss_retry_count": 3,
  "rss_retry_delay": "2s",
  "rss_worker_timeout": "30s",
  "rss_max_item_age": "",
  "max_api_entries_per_cycle": 100,
  "max_rss_entries_per_cycle": 100,
  "data_dir": "./data",
  "discord_delay": "2s",
  "slack_delay": "2s",
  "webhook_request_timeout": "10s",
  "webhook_max_retries": 3,
  "webhook_retry_delay": "1s",
  "retry_max_attempts": 5,
  "retry_window": "24h",
  "status_retention": {
    "max_api_sent_items": 100000,
    "max_rss_parsed_items": 10000,
    "rss_parsed_max_age": "8760h",
    "max_rss_sent_items": 100000,
    "max_retry_queue_items": 10000,
    "retry_queue_max_age": "720h",
    "max_dead_letter_items": 10000,
    "dead_letter_max_age": "720h",
    "audit_log_rotation": { "max_size_mb": 60, "max_backups": 20, "max_age_days": 365, "compress": true }
  },
  "discord_webhooks": {
    "ransomware": {
      "enabled": false,
      "url": "",
      "quiet_hours": { "enabled": false, "start": "10pm", "end": "7am", "timezone": "Europe/Berlin" }
    },
    "rss": { "enabled": false, "url": "", "urls": [], "targets": [] },
    "government": { "enabled": false, "url": "" }
  },
  "slack_webhooks": {
    "ransomware": { "enabled": false, "url": "" },
    "rss": { "enabled": false, "url": "" },
    "government": { "enabled": false, "url": "" }
  },
  "slack_compatible_webhook_hosts": [],
  "slack_compatible_webhooks": {
    "ransomware": { "enabled": false, "url": "" },
    "rss": { "enabled": false, "url": "" },
    "government": { "enabled": false, "url": "" }
  }
}
```

#### Configuration Options

| Field | Description | Example |
| --- | --- | --- |
| `log_level` | Logging verbosity | `"TRACE"`, `"DEBUG"`, `"INFO"`, `"WARNING"`, `"WARN"`, `"ERROR"` |
| `log_file_path` | Rotating application log file path | `"./logs/bot.log"` |
| `log_rotation` | `max_size_mb` 1-1000 (MiB), `max_backups` 0-50, `max_age_days` 0-365, `compress`; `max_backups` or `max_age_days` must be above 0 | see full example |
| `api_key` | ransomware.live API key; required when a ransomware webhook is enabled | `""` |
| `api_base_url` | API base URL; override only for a compatible mirror or proxy | `"https://api-pro.ransomware.live"` |
| `api_request_timeout` | Timeout for one API HTTP attempt; 1s-5m | `"30s"`, `"2m"` |
| `api_max_retries` | HTTP attempts before one API fetch fails; 1-10 | `3`, `10` |
| `api_retry_delay` | Base API retry backoff unless `Retry-After` is given; 100ms-1m | `"1s"`, `"5s"` |
| `api_poll_interval` | API checking frequency; minimum 1m | `"1h"`, `"15m"` |
| `api_check_timeout` | Deadline for one API cycle (fetch, send, retry recovery); minimum 1m | `"5m"`, `"10m"` |
| `rss_poll_interval` | RSS checking frequency; minimum 1m | `"30m"`, `"5m"` |
| `rss_check_timeout` | Deadline for one RSS cycle; minimum 1m; keep it above `rss_worker_timeout` ([details](OPERATIONS.md#rss-cycle-budgets)) | `"10m"`, `"15m"` |
| `max_rss_workers` | Concurrent RSS feed workers; 1-10 | `5`, `10` |
| `rss_worker_timeout` | Budget for one RSS feed batch (the whole worker pool); 5s-5m | `"30s"`, `"2m"` |
| `rss_retry_count` | Failed feed retry attempts; 0-10 | `3`, `10` |
| `rss_retry_delay` | Delay between feed retries; minimum 1s | `"2s"`, `"10s"` |
| `rss_max_item_age` | Max age of RSS items to post; empty = off, negative rejected; skipped items stay recoverable and undated items are exempt ([details](OPERATIONS.md#rss-item-age-limit)) | `""`, `"48h"` |
| `max_api_entries_per_cycle` | Max API alerts per webhook target per cycle; 1-1000 | `100`, `250` |
| `max_rss_entries_per_cycle` | Max RSS alerts per webhook target per cycle, including recovery backlog; 1-1000 | `100`, `250` |
| `data_dir` | Directory for persistent status data (resolved to an absolute path) | `"./data"`, `"/opt/bot/data"` |
| `discord_delay` | Delay between Discord messages; 400ms-30s | `"2s"`, `"500ms"` |
| `slack_delay` | Delay between Slack messages; 1s-30s | `"2s"`, `"3s"` |
| `webhook_request_timeout` | Timeout for one webhook HTTP request; 1s-5m | `"10s"`, `"20s"` |
| `webhook_max_retries` | Webhook HTTP attempts for provider rate limits; 1-10 ([details](OPERATIONS.md#retry-counting)) | `3`, `5` |
| `webhook_retry_delay` | Base webhook rate-limit retry delay when `Retry-After` is missing; 100ms-1m | `"1s"`, `"500ms"` |
| `retry_max_attempts` | Retries after the first send before dead-lettering (`5` = six sends); 0-100, `0` = unlimited within `retry_window` ([details](OPERATIONS.md#retry-counting)) | `5`, `0` |
| `retry_window` | Max time to retry failed sends; `0` = unlimited only when `retry_max_attempts` is set; negative rejected | `"24h"`, `"0"` |
| `status_retention` | Bounds for the status files (item caps 1-1000000, ages 1h-43800h) and `audit_log_rotation` for `delivery_audit.jsonl` (same ranges as `log_rotation`) | see full example |
| `url` / `urls` / `targets` | Primary webhook URL, extra URLs with shared settings, or targets with their own filters and quiet hours | see [Webhooks](#webhooks) |
| `slack_compatible_webhook_hosts` | Approved hostnames for self-hosted or sovereign Slack-compatible endpoints | `["hooks.eu.example"]` |
| `slack_compatible_webhooks` | Slack-compatible targets with the same `ransomware`, `rss`, `government` shape as Slack | `{"rss":{"enabled":true,"url":"https://hooks.eu.example/services/rss"}}` |

At least one persistent retry bound must stay active: do not set both `retry_max_attempts` and
`retry_window` to `0`. With the default `audit_log_rotation`, size the `data_dir` volume for
about 1.3 GB of audit log (1,260 MiB uncompressed worst case) plus the status files; see
[Delivery audit log sizing](OPERATIONS.md#delivery-audit-log-sizing) before the first start on a
large existing `delivery_audit.jsonl`.

Terminal delivery failures are kept in `retry_status.json` as dead letters; inspect them with
`--list-dead-letter` and pass `--data-dir` when the data directory is outside the configured path.

#### Webhooks

- Discord, Slack, and Slack-compatible delivery support three webhooks per platform:
  `ransomware` (ransomware.live API), `rss` (general RSS feeds) and `government` (government
  agency alerts such as CISA or NCSC). Each can be enabled or disabled independently.
- Use `url` for the primary endpoint, `urls` for additional endpoints with the same filters and
  quiet hours, or `targets` when each endpoint needs its own filters or quiet hours.
- `slack_compatible_webhooks` reuse Slack Block Kit JSON for approved endpoints listed in
  `slack_compatible_webhook_hosts`.
- Each webhook supports optional [`filters`](#per-webhook-filters) and
  [`quiet_hours`](#quiet-hours).
- Set a real webhook URL before changing `enabled` to `true`. Placeholder webhook URLs and
  placeholder API keys fail `--check-config` for enabled ransomware alerting.
- Two endpoints may share one URL, for example to give one channel two filter sets. Where the
  filters overlap, the alert is posted once per endpoint, and `--check-config` warns
  ([details](OPERATIONS.md#shared-webhook-urls)).

#### Webhook Endpoint Order

Each endpoint gets a positional destination ID (`discord.ransomware`, `discord.ransomware.2`, ...),
and stored dedup markers, retries and dead letters are keyed by that ID. **Add endpoints only at
the end of `url` → `urls` → `targets`, and delete only from the end.** If the order changed, the
bot refuses to start with `Destination remap detected ...` (`--check-config` exits 1,
`--healthcheck` reports `destination remap pending`). There are two ways out:

1. Restore the previous order and start normally. Nothing is lost.
2. Start once with `--accept-destination-remap` to keep the new order. The affected channels then
   inherit each other's history.

How the check works, and how to start clean instead: [Destination IDs and the endpoint order](OPERATIONS.md#destination-ids-and-the-endpoint-order).

### Per-Webhook Filters

Filters control which entries are sent to each webhook. All filter groups are AND-combined; values within a group are OR-combined. Omitting `filters` means all entries are accepted.

```json
{
  "enabled": true,
  "url": "https://discord.com/api/webhooks/...",
  "filters": {
    "include_countries": ["US", "DE", "GB"],
    "exclude_groups": ["lockbit"],
    "include_fields": {"victim": ["hospital"]},
    "include_keywords": ["critical", "healthcare"],
    "keyword_match": "literal"
  }
}
```

Endpoint-specific targets can route different subsets to different channels:

```json
{
  "enabled": true,
  "targets": [
    {
      "url": "https://hooks.slack.com/services/T/B/security",
      "filters": {"include_countries": ["DE"]}
    },
    {
      "url": "https://hooks.slack.com/services/T/B/healthcare",
      "filters": {"include_keywords": ["hospital"]},
      "quiet_hours": {"enabled": true, "start": "22:00", "end": "06:00", "timezone": "Europe/Berlin"}
    }
  ]
}
```

A target that omits `filters` or `quiet_hours` inherits the block's value for that key; an
explicit `{}` opts it out ([details](OPERATIONS.md#target-inheritance-of-filters-and-quiet-hours)).

| Filter Field | Scope | Description |
| --- | --- | --- |
| `include_groups` / `exclude_groups` | Ransomware API only | Filter by ransomware group name |
| `include_countries` / `exclude_countries` | Ransomware API only | Filter by two uppercase ASCII country letters such as `US`, `DE`, or `GB` |
| `include_activities` / `exclude_activities` | Ransomware API only | Filter by activity type |
| `include_keywords` / `exclude_keywords` | Ransomware API and RSS | Filter API alert title, victim, description, group, and activity; RSS title and description |
| `include_categories` / `exclude_categories` | RSS only | Filter by RSS category |
| `include_fields` / `exclude_fields` | Ransomware API and RSS | Generic field filters. API fields: `id`, `victim`, `group`, `country`, `activity`, `attack_date`, `discovered`, `published`, `claim_url`, `website`, `description`, `screenshot`; RSS fields: `title`, `description`, `feed_title`, `feed_url`, `link`, `author`, `category` (alias `categories`), `published`, `guid` |
| `keyword_match` | Ransomware API and RSS | `"literal"` (default, case-insensitive) or `"regex"` (Go regular expressions; case-sensitive unless the pattern includes `(?i)`) |

Filter lists accept up to 50 values each. Blank values are rejected. Group, activity, category, and country filter values are capped before runtime; keyword values are capped separately for literal and regex modes.

### Quiet Hours

Quiet hours pause message delivery during a configurable time window per webhook. Messages are not lost: ransomware API items are queued locally without consuming retry attempts, and RSS items remain recoverable from parsed/retry state. Delivery resumes automatically when the window ends.

```json
{
  "enabled": true,
  "url": "https://discord.com/api/webhooks/...",
  "quiet_hours": {
    "enabled": true,
    "start": "10pm",
    "end": "7am",
    "timezone": "Europe/Berlin"
  }
}
```

| Field | Description | Examples |
| --- | --- | --- |
| `enabled` | Toggle quiet hours on/off without removing config | `true`, `false` |
| `start` | Window start (inclusive) — 24h or 12h format | `"22:00"`, `"10pm"`, `"10:30 PM"` |
| `end` | Window end (exclusive) — 24h or 12h format | `"07:00"`, `"7am"`, `"6:30 AM"` |
| `timezone` | IANA timezone (default: `"UTC"`) | `"Europe/Berlin"`, `"America/New_York"` |

Formats are mixable (`"start": "22:00", "end": "7am"`), and midnight-crossing windows work.

### 2. RSS Feeds Configuration (configs/config_feeds.json)

The shipped [`configs/config_feeds.json`](configs/config_feeds.json) lists the default feeds in
three categories:

- `ransomware_feeds` - RSS feeds specific to ransomware threats (routed to the ransomware webhook)
- `government_feeds` - Government cybersecurity alerts (routed to the government webhook)
- `general_feeds` - General cybersecurity news and research (routed to the RSS webhook)

You can add, remove, or change feed URLs in any category. Feed URLs must be HTTPS, without
credentials, sensitive query parameters or fragments, and must resolve to a public address
([full rules](OPERATIONS.md#feed-url-rules)). Feed fetches ignore `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`;
webhook deliveries honour them.

### 3. Message Formatting (configs/config_format.json)

The shipped [`configs/config_format.json`](configs/config_format.json) is a working example of
the options below.

**Formatting Options:**

- `show_unicode_flags` - Display country flag emojis (🇺🇸, 🇩🇪, etc.)
- `display_locale` - BCP 47 locale for country/region display names such as `"en"` or `"de"` (default: `"en"`)
- `timestamp_format` - Go time layout for alert timestamps (default: `"2006-01-02 15:04:05 MST"`)
- `display_timezone` - IANA timezone for alert timestamps such as `"UTC"` or `"Europe/Berlin"` (default: `"UTC"`)
- `show_empty_fields` - Include missing ransomware alert fields in Discord and Slack messages (default: `false`)
- `empty_field_text` - Placeholder text for missing ransomware alert fields (default: `"N/A"`)
- `field_order` - Default ransomware alert field order for Discord messages
- `field_labels` - Shared label overrides for Discord and Slack fields and fixed labels
- `rss.title_text` - Fallback title for untitled general RSS items
- `rss.show_author` - Forward RSS author/byline names only when explicitly set to `true` (default: `false`)
- `rss.field_order` - Shared RSS metadata order for Discord and Slack RSS messages
- `rss.description_max_chars` - Optional RSS description limit; omitted keeps legacy platform defaults
- `rss.field_labels` - RSS-specific label overrides such as `categories`, `published`, `source`, and `feed_url`
- `discord.show_icons` - Include decorative icons in Discord labels and titles
- `discord.ransomware_color`, `discord.rss_color`, `discord.government_color` - Discord embed accent colors as `#RRGGBB`
- `discord.description_max_chars` - Max Discord ransomware description length
- `discord.field_labels` - Discord-specific label overrides; these take precedence over shared labels
- `slack.title_text` - Custom title for Slack ransomware alerts
- `slack.rss_text` - Legacy custom title for Slack general RSS updates
- `slack.description_max_chars` - Max Slack ransomware description length
- `slack.field_order` - Field order specific to Slack (overrides default `field_order`)
- `slack.field_labels` - Slack-specific label overrides for fields, fallback text, context labels, and action buttons

`field_labels` maps override labels such as `group`, `victim`, `post_url`, `source`,
`ransomware_alert`, `ransomware_source`, `rss_update`, `government_rss_update`,
`ransomware_rss_update`, `open_article`, `open_feed`, `open_website`, and `open_screenshot`.
`show_empty_fields` and `empty_field_text` do not add missing RSS metadata fields.

**Available ransomware fields (if present):** `id`, `victim` (target organization), `group`,
`country` (with flag emoji if enabled), `activity` (attack classification), `attack_date`,
`discovered`, `post_url` (ransom post/leak page), `website` (victim's website), `description`,
`screenshot` (ransom note screenshot), `published`.

**Available RSS fields:** `title`, `description`, `link` (article URL/button), `author` (only when
`rss.show_author` is `true`), `categories`, `published`, `feed_title` / `source` (source feed
title), `feed_url`.

## Config Hot-Reload

The bot checks for config file changes every 60 seconds. A change is reloaded and validated
without a restart; if the new config fails validation, the current config stays active and the
error is logged.

**Reloadable at runtime:**

- Poll intervals and cycle deadlines (`api_poll_interval`, `api_check_timeout`, `rss_poll_interval`, `rss_check_timeout`)
- Delays (`discord_delay`, `slack_delay`)
- Log level (`log_level`)
- API key rotation (`api_key`)
- RSS/API delivery limits and RSS settings (`max_api_entries_per_cycle`, `max_rss_entries_per_cycle`, `rss_retry_count`, `rss_retry_delay`, `rss_worker_timeout`, `rss_max_item_age`, `max_rss_workers`)
- Feed URLs and webhook enabled/disabled flags
- Webhook endpoint lists, but only by appending or removing from the end; a reload that reorders, inserts or rotates an endpoint URL is rejected with a WARN (see [Webhook Endpoint Order](#webhook-endpoint-order))
- Webhook filters and quiet hours
- Format configuration

**Require restart:**

- `data_dir`, `log_file_path`, `log_rotation`
- `webhook_request_timeout`, `webhook_max_retries`, `webhook_retry_delay`

## Docker

✅ **Tested on Unraid**: This container has been successfully tested on Unraid without any issues.
✅ **Lightweight**: Docker image size is approximately ~31MB

The image does not bake runtime configs into the build; keep `configs/config_general.json`
mounted at runtime. The image does not set `DATA_DIR`, so `data_dir` from the config stays
effective unless you set that override. `docker-compose.yml` runs the container as UID/GID
`1000:1000` with app path `/app`, requires `RANSOMWARE_BOT_IMAGE_TAG`, and defaults to
`linux/amd64`; override with `BOT_UID`, `BOT_GID`, `APP_DIR` and `RANSOMWARE_BOT_PLATFORM`.

### Build, update and roll back

Security fixes are listed in [CHANGELOG.md](CHANGELOG.md). If an advisory names leaked
credentials, rotate the ransomware.live API key and the affected webhooks before restarting.

```bash
# 1. Remember the running tag and back up persistent state
PREVIOUS_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}"
tar -czf "ransomware-bot-data-$(date -u +%Y%m%dT%H%M%SZ).tgz" ./data

# 2. Build the new commit, tagged with its short SHA
git pull --ff-only
export RANSOMWARE_BOT_IMAGE_TAG="$(git rev-parse --short HEAD)"
docker build --pull --platform "${RANSOMWARE_BOT_PLATFORM:-linux/amd64}" \
  --build-arg COMMIT="$(git rev-parse --short HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t "ransomware-news-bot:${RANSOMWARE_BOT_IMAGE_TAG}" .

# 3. Validate the mounted config, start, and verify
docker compose run --rm ransomware-news-bot --check-config --config-dir /app/configs --data-dir /app/data
docker compose up -d
docker compose run --rm ransomware-news-bot --version
docker compose run --rm ransomware-news-bot --healthcheck --config-dir /app/configs --data-dir /app/data
docker compose run --rm ransomware-news-bot --list-dead-letter --config-dir /app/configs --data-dir /app/data

# 4. Roll back if the new build fails these checks or produces terminal delivery failures
RANSOMWARE_BOT_IMAGE_TAG="${PREVIOUS_IMAGE_TAG}" docker compose up -d --no-build
```

`docker build` uses the Dockerfile defaults for UID/GID and app path. With non-default `BOT_UID`,
`BOT_GID` or `APP_DIR`, build with `docker compose build --pull` instead; that build does not pass
`COMMIT`/`BUILD_DATE`, so `--version` reports them as `unknown`. On a rollback keep the existing
`./data` and run `--healthcheck` again; restore the backup only when status files are damaged or
contain bad-release-only state, then run `--list-dead-letter` to confirm recovery. Mirrored builds and image export:
[Building images](OPERATIONS.md#building-images).

### Required Volume Mounts

- **Configuration**: `${APP_DIR:-/app}/configs` - your config directory (mounted read-only)
- **Logs**: `${APP_DIR:-/app}/logs` - application logs with rotation
- **Data**: `${APP_DIR:-/app}/data` - **critical for persistence**: status tracking, deduplication
  and `destinations.json` for the endpoint-order check

⚠️ **Important**: Without the `/app/data` volume, all processed-item tracking is lost on container restart, causing duplicate messages.

On Linux hosts, create writable bind mounts before the first start (see [Run with Docker](#run-with-docker)).
`pwsh ./scripts/prepare-compose-host.ps1` creates `logs/` and `data/` and, on non-Windows hosts,
applies the configured `BOT_UID`/`BOT_GID` ownership.

- Run only one bot instance per `/app/data` volume. The bot holds an exclusive lock on
  `.ransomware-bot.lock` in `data_dir`, and a second instance on the same directory exits. The
  marker file stays in place after shutdown on purpose.
- `logs/bot.log.lock` is held the same way for the log file and is reused on the next start; do
  not delete it while the bot is running.
- Compose gives the bot a 45 second stop grace period to flush status files. Use the same timeout
  for manual stops: `docker compose stop -t 45 ransomware-news-bot`.
- Compose keeps up to 30 Docker JSON log files of 50 MB each. If you need guaranteed 90-day
  retention, ship Docker logs and `./logs` to a central log store.
- Docker does not alert when the container turns `unhealthy`; see
  [Alerting on an unhealthy container](OPERATIONS.md#alerting-on-an-unhealthy-container).

## Troubleshooting

Run `go run . --check-config --config-dir ./configs` before starting the bot, and
`--dry-run` to preview a changed config without sending. Longer explanations are in
[Troubleshooting details](OPERATIONS.md#troubleshooting-details).

| Symptom | Fix |
| --- | --- |
| `config directory "..." does not exist` | Run from the repository root or pass the absolute `--config-dir`. |
| `api_key is required` | Set a real ransomware.live API key when ransomware webhooks are enabled. |
| `webhook URL is not valid` | Use full Discord or Slack incoming webhook URLs, not channel URLs or placeholders. |
| `data_dir` or log path errors | Create the directory and make sure the process or container UID can write to it. |
| `quiet_hours` time or timezone errors | Use `HH:MM`, `10pm`, or `10:30 PM`, and an IANA timezone such as `Europe/Berlin`. |
| `Could not initialize file logger; falling back to stdout-only logging` | Another bot process owns this log file; its pid and host are in `logs/bot.log.lock`. Stop the second container or process. A lock left by a crash is recovered automatically. |
| `Data directory's filesystem does not support file locking (ENOLCK/EINVAL)` | The `data_dir` filesystem does not support file locking; put `data_dir` on a filesystem that does (on Unraid `/mnt/cache` or a disk share, not `/mnt/user`). |
| Feeds fail with connection errors only in a proxied network | Feed fetches ignore `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` by design; the feed host must be reachable directly. |
| `Destination remap detected ...` or `Healthcheck failed: destination remap pending` | The webhook endpoint order changed. Restore the previous order, or start once with `--accept-destination-remap` (see [Webhook Endpoint Order](#webhook-endpoint-order)). |
| `Destination manifest unreadable` or `destinations.json could not be read` | `destinations.json` is corrupt or from an unknown version, so old bindings are unknown and a start with configured endpoints is refused. Restore the file from backup, or start once with `--accept-destination-remap` ([details](OPERATIONS.md#unreadable-manifests-and-manifest-versions)). |
| `Configuration warning: webhook endpoints ... share one webhook URL` | Two endpoints for the same alert type use one URL. Intended when their filters are disjoint; otherwise give one a different filter set or remove it. |
| Healthcheck fails after start | Check the logs for initial API/RSS errors and run `--list-dead-letter` for terminal delivery failures. |
| `Healthcheck failed: data_dir ... does not exist` | The `data_dir` bind mount did not come up, or `--data-dir` points at the wrong path. The healthcheck never creates the directory. |
| `Healthcheck failed: all N enabled RSS feed(s) failed ...` or `... no enabled RSS feed has succeeded since ...` | No feed succeeded within `3 x rss_poll_interval`. Check egress to the feed hosts, the error in the message, the log, and `rss_status.json`. |
| `RSS feeds degraded: M of N ...` | Warning only; the container stays healthy. The failing feed's error is in the log and under its `last_error` in `rss_status.json`. |
| `Healthcheck failed: RSS is enabled ... but no poll attempt has been recorded ...` | `data_dir` is not persisting RSS state; check the `/app/data` mount and that the container UID can write to it. Clears on the next successful RSS cycle. |
| `Healthcheck failed: RSS is enabled ... exhausted its rss_check_timeout ...` | Raise `rss_check_timeout` above `rss_worker_timeout` ([details](OPERATIONS.md#rss-cycle-budget-overrun)). |
| `Healthcheck failed: the API/RSS poller has completed no poll pass for ...` | The poller is wedged. Check the logs around the last completed pass named in the message; restart the container if it does not recover ([allowance](OPERATIONS.md#poller-progress-markers)). |
| Briefly `unhealthy` right after enabling RSS by hot reload or replacing the general feed list | Expected and self-healing; no action needed ([details](OPERATIONS.md#briefly-unhealthy-after-an-rss-hot-reload)). |
| `unhealthy` after a clock step or a host suspend/resume, with nothing else changed | A false red that heals after the next completed pass or a restart; no action needed ([details](OPERATIONS.md#clock-steps-and-host-suspend)). |
| `failed to load existing RSS status: ... unexpected end of JSON input` | A status file in `data_dir` is truncated, usually from a hard kill on an older build. Restore the `data_dir` backup, or delete the named file and accept that recent items may be re-delivered once. |
| `Failed to load API status before API state mutation` / `Skipping API status save: api_status.json is not loaded` | `api_status.json` is corrupted, so alerts in the retention window are re-delivered every cycle. Restore the `data_dir` backup, or delete `api_status.json` and accept one round of re-delivery. |
| `API rate limited; retry after ...` / API polling looks stopped | A 429 cooldown, not a hang; polling resumes on its own once the `Retry-After` deadline passes ([details](OPERATIONS.md#api-rate-limit)). |

## Platform Support

Both Discord and Slack are fully supported with the same ransomware data. The only difference:

- **Discord**: Supports emoji in messages (including country flags 🇺🇸🇩🇪)
- **Slack**: Uses Block Kit formatting with country flag emoji support (when `show_unicode_flags` is enabled)

Both platforms:

- Receive identical ransomware alerts and RSS feeds
- Support customizable field ordering
- Defang ransomware claim/post URLs (`http` -> `hxxp`) where they are included in alert fields
- Can be configured independently with separate webhooks

## Acknowledgments

- [ransomware.live](https://ransomware.live) for providing the threat intelligence API
- Go's standard HTTP and XML packages for lightweight webhook delivery and RSS parsing
- The cybersecurity community for RSS feed sources
