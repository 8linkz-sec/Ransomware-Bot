# Ransomware News Bot

A high-performance bot written in **Go** that delivers ransomware alerts via **Discord**, **Slack**, and approved Slack-compatible webhooks. The bot fetches data from the ransomware.live API and multiple RSS feeds, providing regular cybersecurity updates to your communication channels.

The original bot from vx-underground no longer works, so I built a new one (<https://github.com/vxunderground/ThreatIntelligenceDiscordBot>).

⚠️ Disclaimer
This bot is 100% vibe-coded - I provide no guarantee for 100% security.

<img src="https://github.com/user-attachments/assets/699b63de-043e-40cc-9fb8-e396cf55ce79" alt="Screenshot of Ransomware News Bot notification output" width="524" height="377">

## Contents

- [Local Development Setup](#local-development-setup)
- [CLI Flags](#cli-flags)
- [Prerequisites](#prerequisites)
- [Configuration](#configuration)
- [Config Hot-Reload](#config-hot-reload)
- [Docker Compatibility](#docker-compatibility)
- [First-Run Troubleshooting](#first-run-troubleshooting)
- [Platform Support](#platform-support)
- [Acknowledgments](#acknowledgments)

## Local Development Setup

Requirements:

- Git
- Go 1.27.x
- Docker and Docker Compose, optional for container validation

Fresh clone:

```bash
git clone https://github.com/8linkz-sec/Ransomware-Bot.git
cd Ransomware-Bot
go mod download
go test ./...
go run . --check-config --config-dir ./configs
```

For local runtime testing, copy the example config and add only local credentials:

```bash
cp configs/config_general.example.json configs/config_general.json
go run . --check-config --config-dir ./configs
go run . --dry-run --config-dir ./configs --data-dir ./data
```

Do not commit local credentials, status files, logs, or private feed URLs.

Run a single package with `go test ./internal/<package> -count=1`; the
*Deployment Smoke Tests* section below covers Docker Compose validation.

`go test ./...` also runs in CI (`.github/workflows/test.yml`) on every pull
request and on push to `main`, on Linux — the platform the bot actually
deploys to, rather than the Windows development host. It runs the suite once
plain and once with `-race`, as two separate steps so a data race is
distinguishable from a logic failure. `.github/pull_request_template.md`'s
`go test ./...` checkbox now confirms the run locally reproduced the CI
result rather than substituting for it.

## CLI Flags

```bash
# Normal operation
./ransomware-news-bot --config-dir ./configs

# Validate configuration without starting the bot
./ransomware-news-bot --check-config --config-dir ./configs

# Validate runtime health for container/orchestrator probes
./ransomware-news-bot --healthcheck --config-dir ./configs

# Dry-run mode: run one cycle, log what WOULD be sent, then exit
./ransomware-news-bot --dry-run --config-dir ./configs

# Override data directory
./ransomware-news-bot --data-dir /custom/data/path

# List terminally failed deliveries (dead letters) with recovery details
./ransomware-news-bot --list-dead-letter --config-dir ./configs

# Show version
./ransomware-news-bot --version
# Example: Ransomware News Bot v1.2.1 (commit abc123, built 2026-09-05T12:00:00Z)
```

| Flag | Description |
| --- | --- |
| `--config-dir` | Directory containing JSON config files (default: `./configs`) |
| `--data-dir` | Directory for persistent data (overrides config and `DATA_DIR` env) |
| `--check-config` | Validate configuration and exit (prints `Configuration valid` or `Configuration invalid`, exit code 0 or 1). Since the destination-remap check was added, its exit code also depends on `destinations.json` in `data_dir` and no longer on the config files alone — point `--data-dir` at the production data directory when using it as a pre-flight check (see *Destination IDs And The Endpoint Order*). |
| `--accept-destination-remap` | Accept a changed webhook-endpoint-to-destination-ID mapping once, rewrite `destinations.json`, and start. Only meaningful together with a normal start; ignored by `--check-config` and `--healthcheck`. |
| `--healthcheck` | Validate config, the data directory, an optional scheduler readiness marker, two optional per-poller progress markers (unhealthy when a poller has completed no pass for three cycles of its poll interval plus its worst-case pass duration — see *Poller Progress Markers* below), terminal delivery failures, the last ransomware API check when ransomware delivery is enabled (reporting a 401/403 API auth suspension by name), and RSS feed health: unhealthy when RSS is enabled but `data_dir` shows no poll attempt at all and the RSS progress marker shows the last completed pass did not overrun its cycle budget (a pass that did overrun gets a distinct message instead, naming `rss_check_timeout`/`rss_worker_timeout` — see the same section; a genuinely fresh start, before any RSS progress marker exists yet, is never flagged), and otherwise unhealthy only when no enabled feed has succeeded within three `rss_poll_interval` periods — whether or not the feeds report an error; a single failing feed, and even a transient failure that hits every feed at once, is a warning line with exit 0. `data_dir` must already exist and be writable; the check never creates it. |
| `--dry-run` | Run a single cycle, log would-send previews and empty-state summaries with `[DRY-RUN]`, don't send or modify tracker. Exits `1` if the API check or the RSS check reported a genuine failure (a fetch error, `api_key` empty with a ransomware webhook enabled, or a real RSS batch parse failure — never a cycle-budget overrun or a shutdown cancel), else `0`. |
| `--list-dead-letter` | List terminally failed deliveries with operator recovery details and exit |
| `--locale` | CLI output language, `en` or `de` (also via `BOT_LOCALE` env) |
| `--version` | Show version information and exit |
| `--help`, `-h` | Print usage text and exit 0 (a genuine parse error, such as an unknown flag, still exits 2). The usage text is printed to stderr, not stdout, in both cases. |

## Prerequisites

### API Key

The ransomware.live API allows **3,000 calls per day** (API Pro). You need to request an API key at: <https://www.ransomware.live/api>

### Discord Webhooks

To create Discord webhooks:

1. Go to your Discord server settings
2. Navigate to "Integrations" → "Webhooks"
3. Click "Create Webhook"
4. Choose the channel and copy the webhook URL
5. Add the URL to the configuration (see Configuration section below)

### Slack Webhooks

To create Slack webhooks:

1. Go to <https://api.slack.com/apps>
2. Create a new app or select an existing one
3. Navigate to "Incoming Webhooks" and activate it
4. Click "Add New Webhook to Workspace"
5. Select the channel and copy the webhook URL
6. Add the URL to the configuration (see Configuration section below)

⚠️ **Security Warning**: Never share your webhook URLs with others - they provide direct access to post messages in your channels.

## Configuration

### 1. General Configuration (configs/config_general.json)

The repository tracks `configs/config_general.example.json` as a template. Copy it to
`configs/config_general.json` for local/runtime use, then put real API keys and webhook URLs only in the
local file:

```bash
cp configs/config_general.example.json configs/config_general.json
```

Do not commit `configs/config_general.json` after adding real credentials.

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

For Slack, use the same `api_key` and enable `slack_webhooks.ransomware.url`
with an incoming webhook URL. The full example below shows optional logging,
polling, retry, quiet-hours, and multi-channel settings.

```json
{
  "log_level": "INFO",
  "log_file_path": "./logs/bot.log",
  "log_rotation": {
    "max_size_mb": 10,
    "max_backups": 30,
    "max_age_days": 90,
    "compress": true
  },
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
    "audit_log_rotation": {
      "max_size_mb": 60,
      "max_backups": 20,
      "max_age_days": 365,
      "compress": true
    }
  },
  "discord_webhooks": {
    "ransomware": {
      "enabled": false,
      "url": "",
      "quiet_hours": {
        "enabled": false,
        "start": "10pm",
        "end": "7am",
        "timezone": "Europe/Berlin"
      }
    },
    "rss": {
      "enabled": false,
      "url": "",
      "urls": [],
      "targets": []
    },
    "government": {
      "enabled": false,
      "url": ""
    }
  },
  "slack_webhooks": {
    "ransomware": {
      "enabled": false,
      "url": ""
    },
    "rss": {
      "enabled": false,
      "url": ""
    },
    "government": {
      "enabled": false,
      "url": ""
    }
  },
  "slack_compatible_webhook_hosts": [],
  "slack_compatible_webhooks": {
    "ransomware": {
      "enabled": false,
      "url": ""
    },
    "rss": {
      "enabled": false,
      "url": ""
    },
    "government": {
      "enabled": false,
      "url": ""
    }
  }
}
```

**Configuration Options:**

| Field | Description | Example |
| --- | --- | --- |
| `log_level` | Logging verbosity | `"TRACE"`, `"DEBUG"`, `"INFO"`, `"WARNING"`, `"WARN"`, `"ERROR"` |
| `log_file_path` | Rotating application log file path | `"./logs/bot.log"`, `"/var/log/ransomware-bot/bot.log"` |
| `log_rotation` | Log retention settings: `max_size_mb` 1-1000 MB, `max_backups` 0-50, `max_age_days` 0-365, `compress` true/false; at least one of `max_backups` or `max_age_days` must be greater than 0 | `{"max_size_mb": 10, "max_backups": 30, "max_age_days": 90, "compress": true}` |
| `api_key` | Ransomware.live API key; required when a ransomware webhook is enabled | `""` |
| `api_base_url` | Ransomware API base URL; override only for a compatible mirror or proxy | `"https://api-pro.ransomware.live"` |
| `api_request_timeout` | Timeout for one ransomware.live HTTP request attempt; valid range 1s-5m | `"30s"`, `"45s"`, `"2m"` |
| `api_max_retries` | Maximum ransomware.live HTTP attempts before failing one fetch; valid range 1-10 | `3`, `4`, `10` |
| `api_retry_delay` | Base delay for API HTTP retry backoff unless `Retry-After` is provided; valid range 100ms-1m | `"1s"`, `"500ms"`, `"5s"` |
| `api_poll_interval` | API checking frequency; minimum 1m | `"1h"`, `"30m"`, `"15m"` |
| `api_check_timeout` | Deadline for one API poll cycle, including fetch, send, and retry recovery; minimum 1m | `"5m"`, `"10m"` |
| `rss_poll_interval` | RSS checking frequency; minimum 1m | `"30m"`, `"15m"`, `"5m"` |
| `rss_check_timeout` | Deadline for one RSS poll cycle (fetch, delivery and recovery); minimum 1m. When it expires, feeds that already answered are processed normally and the remaining feeds are treated as not polled — they are not recorded as failures. Set it above `rss_worker_timeout`. | `"10m"`, `"15m"` |
| `max_rss_workers` | Concurrent RSS feed workers; valid range 1-10 | `5`, `8`, `10` |
| `rss_worker_timeout` | Budget for one RSS feed batch — the whole worker pool, not a single feed; valid range 5s-5m. A feed that has not answered when it expires is recorded as a feed timeout. | `"30s"`, `"1m"`, `"2m"` |
| `discord_delay` | Delay between Discord messages; valid range 400ms-30s | `"2s"`, `"1s"`, `"500ms"` |
| `slack_delay` | Delay between Slack messages; valid range 1s-30s | `"2s"`, `"1s"`, `"3s"` |
| `webhook_request_timeout` | Timeout for one Discord, Slack, or Slack-compatible webhook HTTP request; valid range 1s-5m | `"10s"`, `"20s"` |
| `webhook_max_retries` | Maximum webhook HTTP attempts for provider-declared rate limits; valid range 1-10. Transport errors and ambiguous 5xx responses are not automatically retried. | `3`, `5` |
| `webhook_retry_delay` | Base delay for webhook rate-limit retries when the provider omits `Retry-After`; valid range 100ms-1m | `"1s"`, `"500ms"` |
| `url` / `urls` / `targets` | Primary webhook URL, additional URLs with shared settings, or endpoint-specific targets with their own filters and quiet hours | `{"url":"https://...","targets":[{"url":"https://...","filters":{"include_countries":["DE"]}}]}` |
| `slack_compatible_webhook_hosts` | Approved hostnames for Slack-compatible self-hosted or sovereign endpoints | `["hooks.eu.example"]` |
| `slack_compatible_webhooks` | Slack-compatible webhook targets using the same `ransomware`, `rss`, and `government` shape as Slack | `{"rss":{"enabled":true,"url":"https://hooks.eu.example/services/rss"}}` |
| `rss_retry_count` | Failed feed retry attempts; valid range 0-10 | `3`, `5`, `10` |
| `rss_retry_delay` | Delay between retries; minimum 1s | `"2s"`, `"5s"`, `"10s"` |
| `rss_max_item_age` | Max age for RSS items to be posted (empty = disabled, negative rejected). Items older than this are skipped for the current cycle but are not marked as sent, so increasing the value later can still recover them. Items whose publication date cannot be read are exempt from this limit by design, not skipped as "too old": treating an unknown age as "too old" would drop every item of a feed whose date format the parser cannot read. Such an item is delivered once, pruned after `status_retention.rss_parsed_max_age`, and delivered again if the publisher still carries it in the feed at that point, once per retention period. The signal to act on is the one-per-feed WARN `RSS feed timestamp could not be parsed`, which names the feed and the raw value. | `""`, `"48h"`, `"72h"` |
| `max_api_entries_per_cycle` | Max ransomware API alerts delivered per webhook target in one poll cycle; valid range 1-1000 | `100`, `50`, `250` |
| `max_rss_entries_per_cycle` | Max RSS alerts delivered per webhook target in one poll cycle, including recovery backlog; valid range 1-1000 | `100`, `50`, `250` |
| `data_dir` | Directory for persistent status data (resolved to absolute path) | `"./data"`, `"/opt/bot/data"` |
| `retry_max_attempts` | Number of **retries after the first send**, per item and destination, before the item is dead-lettered; `N` allows `N + 1` sends in total (`5` = the first send plus five retries = six sends, `1` = two sends). Valid range 0-100, so `100` allows up to 101 sends of one item to one destination; `0` = unlimited retries within `retry_window` (only legal together with a non-zero `retry_window`) | `5`, `10`, `0` |
| `retry_window` | Max time to retry failed sends; 0 = unlimited only when `retry_max_attempts` is set, negative rejected | `"24h"`, `"48h"`, `"0"` |
| `status_retention` | Local status retention bounds for `api_status.json`, `rss_status.json`, and `retry_status.json`: item caps 1-1000000 and age windows 1h-43800h, plus `audit_log_rotation` (`delivery_audit.jsonl` size-based rotation: `max_size_mb` 1-1000 MB, `max_backups` 0-50, `max_age_days` 0-365, `compress` true/false) | `{"max_api_sent_items":100000,"max_rss_parsed_items":10000,"rss_parsed_max_age":"8760h","max_rss_sent_items":100000,"max_retry_queue_items":10000,"retry_queue_max_age":"720h","max_dead_letter_items":10000,"dead_letter_max_age":"720h","audit_log_rotation":{"max_size_mb":60,"max_backups":20,"max_age_days":365,"compress":true}}` |

At least one persistent retry bound must stay active. Do not set both `retry_max_attempts` and `retry_window` to `0`.

Status retention is count-based for API sent markers to avoid replaying old alerts after upstream reintroductions. RSS parsed entries are retained until delivered to active targets and then pruned by count or age; retry and dead-letter records use their configured queue age and count bounds.

`delivery_audit.jsonl` is bounded differently: it is a line-oriented log, not a keyed store, so it is rotated by size rather than by item count. `max_size_mb` here (as in `log_rotation`) is measured in **MiB** (1,048,576 bytes), not decimal MB, despite the config key's name. The defaults bound it at roughly **1,260 MiB uncompressed worst case** (`max_size_mb x (max_backups + 1)` = 60 x 21; about **1,322 MB** if you are sizing a decimal-MB volume) and about **300 MiB** (~315 MB decimal) in practice with `compress: true` (`max_backups x max_size_mb / 5` for the compressed backups plus the still-growing active file). No warning is emitted until that product exceeds 5,000 (MiB; ~5,243 MB decimal), so size the `data_dir` volume for the uncompressed figure, not the compressed one — and see the next paragraph: the first rotation of a pre-existing, already-large `delivery_audit.jsonl` can briefly exceed even that.

**First rotation of a pre-existing `delivery_audit.jsonl` does not stay under the ceiling above.** If the file is already multi-gigabyte when the bot first starts on a build with this rotation enabled — for example, upgrading from a deployment that had been running an earlier build without size-based rotation — the whole legacy file becomes one oversized backup (rotation prunes by count and age, never by size, so an over-cap backup is never shrunk), and with `compress: true` (the default) a growing `.gz.tmp` sits beside it while background compression runs. Peak `data_dir` usage during that one rotation is therefore roughly the steady-state ceiling above **plus the legacy file's own size, plus about a fifth of that size again** for the in-flight compression. Size the volume for that peak, not just the steady-state figure, before the first start on an existing large `delivery_audit.jsonl`. Running out of space does not just lose audit lines (rotation and compression failures are warn-only, and delivery is unaffected); it can also make `SavePendingChanges` fail for `api_status.json`, `rss_status.json`, and `retry_status.json`, which causes re-delivery on the next cycle.

Terminal delivery failures are kept in `retry_status.json` as dead-letter items. Inspect them with:

```bash
go run . --list-dead-letter --config-dir ./configs
```

Pass `--data-dir` when the runtime data directory is outside the configured path.

**Webhook Configuration:**

- Discord, Slack, and Slack-compatible delivery support three separate webhooks for different alert types
- `ransomware` - Alerts from ransomware.live API
- `rss` - General cybersecurity RSS feeds
- `government` - Government agency alerts (CISA, NCSC, etc.)
- Each webhook can be independently enabled/disabled
- Use `url` for the primary endpoint, `urls` for additional endpoints with the same filters and quiet hours, or `targets` when each endpoint needs its own filters or quiet-hours policy
- `slack_compatible_webhooks` reuse Slack Block Kit JSON for approved self-hosted or sovereign endpoints listed in `slack_compatible_webhook_hosts`
- Each webhook supports optional `filters` for include/exclude rules (see below)
- Each webhook supports optional `quiet_hours` to pause delivery during specific time windows
- Set a real webhook URL before changing `enabled` to `true`. Placeholder webhook URLs and placeholder API keys fail `--check-config` for enabled ransomware alerting.

Two endpoints of the same webhook block may point at the same URL. This is allowed and each
endpoint delivers independently, so it is a supported way to give one channel two different filter
sets. Where those filter sets overlap, the same alert is posted to that channel once per endpoint.
Configuration load and `--check-config` therefore print a warning naming the endpoints, for
example `discord.ransomware` and `discord.ransomware.2`; neither the URL nor its token is ever
logged. The warning is informational — the configuration stays valid and the bot starts normally.
Endpoint names follow the position in the flattened `url` → `urls` → `targets` list (`.2`, `.3`, …
for the second and later endpoints); for the `rss` and `government` blocks the runtime destination
IDs carry an additional feed-type segment, such as `discord.rss.general.2`. Endpoints of
*different* alert types that share a URL are not warned about: sending ransomware and RSS alerts to
one channel is a normal setup, and no single alert can reach both. Endpoints of the *same* alert
type are warned about even when they are configured in different platform blocks, because every
alert of that type is delivered to all of them.

### Destination IDs And The Endpoint Order

Every webhook endpoint gets a **positional** destination ID. Within one webhook block the endpoints
are numbered by their position in the flattened `url` → `urls` → `targets` list, and the ID is what
the bot stores: deduplication markers, the retry queue and dead letters are all keyed by
`(item, destination ID)`, never by URL. A `ransomware` block gets two ID families:
`<platform>.ransomware[.N]` for API alerts and `<platform>.rss.ransomware[.N]` for ransomware RSS
feeds; `rss` and `government` blocks get `<platform>.rss.general[.N]` and
`<platform>.rss.government[.N]`.

**The endpoint list is append-only.** Add new endpoints at the end, and delete only from the end.
Reordering endpoints, inserting one in front of an existing one, or deleting one that is not the
last moves every later endpoint's URL onto a different ID — the stored history of one channel then
applies to another: alerts are suppressed for the channel that should receive them, and queued
retries are delivered to the wrong webhook. Blanking a URL counts as deleting it, because blank
entries are dropped before numbering. Converting `urls` to `targets` while keeping the order is
safe, and enabling or disabling a whole block is safe — but the endpoint order inside a block is
guarded even while the block is disabled, so reordering it will refuse the next start.

To make that mistake impossible to miss, the bot keeps `destinations.json` in `data_dir`. It maps
each destination ID to a SHA-256 hash of the webhook URL it was last used with — hashes only, never
a URL and never a token. It is written on every successful config load, it is created silently on
the first start, and deleting it simply recreates it on the next start.

If the URL behind an existing ID has changed since the last run, the bot **refuses to start** and
logs an ERROR naming the affected IDs, what changed, the consequence, and the way out;
`--check-config` prints the same text and exits 1, and `--healthcheck` reports
`destination remap pending` and exits 1. A hot reload that would remap is rejected with a WARN and
the running configuration stays active. Example:

```text
Destination remap detected (endpoints reordered): discord.ransomware<-discord.ransomware.2,
discord.ransomware.2<-discord.ransomware. Stored dedup markers, retry-queue rows and dead
letters are keyed by the destination ID, so starting with this configuration would silently
suppress alerts for one endpoint and deliver another endpoint's queued messages to the wrong
webhook. Restore the previous endpoint order (append new endpoints at the end of
url/urls/targets, delete only from the end), or start once with --accept-destination-remap to
accept the new mapping and rewrite /app/data/destinations.json.
```

`X<-Y` reads "the endpoint now stored under X was previously stored under Y"; several previous
owners are joined with `+`, which happens when two endpoints share a URL. `X<-*` means the URL now
behind X was not in the manifest at all, which is what a rotated webhook token looks like. The
headline in brackets is only a hint — a single edit can reorder, insert and rotate at once, and the
movement list is the authoritative description. There are exactly **two ways out**:

1. **Restore the previous order** in `configs/config_general.json` and start normally. Nothing is
   lost; this is the right answer whenever the reorder was accidental.
2. **Start once with `--accept-destination-remap`** if the new mapping is what you want. The
   manifest is rewritten to the new mapping and the bot starts. Understand the consequence first:
   every stored marker, queued retry and dead letter keeps its old ID, so the affected channels
   inherit each other's history — one may go quiet for items it never received, and another may
   receive a queued message meant for its neighbour. To start clean instead, stop the bot, delete
   `destinations.json` **and** the status files in `data_dir`, and accept that recent alerts may be
   re-delivered.

Deleting endpoints from the end is fine and needs no flag; the bot logs one INFO naming the removed
IDs and how many retry-queue and dead-letter rows still carry them. Those rows expire with
`retry_queue_max_age` / `dead_letter_max_age`.

The check compares URLs byte-exactly after trimming surrounding whitespace, the same normalisation
the deduplication key uses. Re-typing one endpoint's URL with a trailing slash or a different host
capitalisation therefore reads as a changed URL and refuses the start.

The guard is not a tombstone log: removing endpoints in one run and re-adding them in a different
order in a later run is not detected, because every ID leaves the manifest as removed and comes back
as new. Change the order in one edit, not in two.

### Per-Webhook Filters

Filters allow you to control which entries are sent to each webhook. All filter groups are AND-combined; values within a group are OR-combined. Omitting `filters` means all entries are accepted.

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

An entry that omits `filters` and/or `quiet_hours` inherits the parent block's value for that key, independently per key — exactly like `url`/`urls[]` endpoints already do. Set an explicit `"filters": {}` or `"quiet_hours": {}` to opt a target out of the block's value for that key instead: an empty object is non-nil and therefore matches every entry / never pauses delivery, unlike omitting the key. Loading or reloading a config where a target newly inherits a value logs one WARN per block naming every affected endpoint.

If a webhook block defines `filters` and/or `quiet_hours` but every one of its `targets[]` children already declares its own, that block-level value was previously dead configuration and never validated; once a target starts inheriting it (by omitting its own `filters`/`quiet_hours`), an invalid block-level value can make the config fail to load for the first time — for `quiet_hours` this includes a block with `enabled: false`, since `quiet_hours` validation has always ignored `enabled`. The error names the block, the field, why it is invalid, and that it came from inheritance; fix it by removing the invalid field from the block or by adding an explicit `"filters": {}` / `"quiet_hours": {}` to the target the error names.

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

- Formats are mixable: `"start": "22:00", "end": "7am"` works
- Midnight-crossing windows work: `"start": "10pm", "end": "7am"`
- Quiet hours are hot-reloadable (changes take effect within 60 seconds)

### 2. RSS Feeds Configuration (configs/config_feeds.json)

```json
{
  "ransomware_feeds": [],
  "government_feeds": [
    "https://www.cert.europa.eu/publications/security-advisories-rss",
    "https://www.cert.ssi.gouv.fr/feed/",
    "https://www.cisa.gov/cybersecurity-advisories/all.xml",
    "https://www.ncsc.gov.uk/api/1/services/v1/report-rss-feed.xml",
    "https://www.cisecurity.org/feed/advisories",
    "https://www.cyber.gc.ca/api/cccs/rss/v1/get?feed=alerts_advisories&lang=en"
  ],
  "general_feeds": [
    "https://grahamcluley.com/feed/",
    "https://krebsonsecurity.com/feed/",
    "https://www.darkreading.com/rss.xml",
    "https://www.bleepingcomputer.com/feed/",
    "https://www.schneier.com/feed/atom/",
    "https://securelist.com/feed/",
    "https://research.checkpoint.com/feed/",
    "https://www.proofpoint.com/us/rss.xml",
    "https://redcanary.com/feed/",
    "https://www.sentinelone.com/feed/",
    "https://www.microsoft.com/en-us/security/blog/feed/",
    "https://blog.talosintelligence.com/rss/",
    "https://isc.sans.edu/rssfeed_full.xml"
  ]
}
```

**Feed Categories:**

- `ransomware_feeds` - RSS feeds specific to ransomware threats (routed to ransomware webhook)
- `government_feeds` - Government cybersecurity alerts (routed to government webhook)
- `general_feeds` - General cybersecurity news and research (routed to RSS webhook)

💡 **Tip**: You can easily add, remove, or modify feed URLs in any category to customize your threat intelligence sources.

Feed URLs must be HTTPS, without credentials, sensitive query parameters or fragments, and must resolve to a public address — private, loopback, link-local, CGNAT, reserved and IPv6-transition addresses embedding those (NAT64 `64:ff9b::/96`, 6to4 `2002::/16`, ISATAP) are refused at load time and again at connect time, and the prefixes whose embedded IPv4 address cannot be recovered unambiguously (`::/96`, `64:ff9b:1::/48`, Teredo `2001::/32`) are refused outright. Feed fetches are direct and ignore proxy environment variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`), while webhook deliveries honour them.

### 3. Message Formatting (configs/config_format.json)

```json
{
  "show_unicode_flags": true,
  "display_locale": "en",
  "timestamp_format": "2006-01-02 15:04:05 MST",
  "display_timezone": "UTC",
  "show_empty_fields": true,
  "empty_field_text": "N/A",
  "field_order": [
    "victim",
    "activity",
    "country",
    "website",
    "group",
    "discovered",
    "attack_date",
    "description",
    "screenshot",
    "post_url"
  ],
  "field_labels": {},
  "rss": {
    "title_text": "RSS Feed Update",
    "show_author": false,
    "field_order": [
      "title",
      "description",
      "link",
      "author",
      "categories",
      "published",
      "feed_title"
    ],
    "field_labels": {}
  },
  "discord": {
    "show_icons": true,
    "ransomware_color": "#ff0000",
    "rss_color": "#0099ff",
    "government_color": "#ffa500",
    "description_max_chars": 500,
    "field_labels": {}
  },
  "slack": {
    "title_text": "Ransomware Alert",
    "rss_text": "RSS Feed Update",
    "description_max_chars": 500,
    "field_order": [
      "group",
      "victim",
      "country",
      "activity",
      "attack_date",
      "discovered",
      "screenshot",
      "post_url",
      "website",
      "description"
    ],
    "field_labels": {}
  }
}
```

**Formatting Options:**

- `show_unicode_flags` - Display country flag emojis (🇺🇸, 🇩🇪, etc.)
- `display_locale` - BCP 47 locale for country/region display names such as `"en"` or `"de"` (default: `"en"`)
- `timestamp_format` - Go time layout for user-visible alert timestamps (default: `"2006-01-02 15:04:05 MST"`)
- `display_timezone` - IANA timezone for user-visible alert timestamps such as `"UTC"` or `"Europe/Berlin"` (default: `"UTC"`)
- `show_empty_fields` - Include missing ransomware alert fields in Discord and Slack messages (default: `false`)
- `empty_field_text` - Placeholder text for missing ransomware alert fields (default: `"N/A"`)
- `field_order` - Default ransomware alert field order for Discord messages
- `field_labels` - Shared label overrides for Discord and Slack fields and fixed labels
- `rss.title_text` - Fallback title for untitled general RSS items
- `rss.show_author` - Forward RSS author/byline names to Discord and Slack only when explicitly set to `true` (default: `false`)
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

💡 **Tip**: Use `field_order` and `slack.field_order` for ransomware alert fields. Use `rss.field_order` for RSS metadata fields. Use `field_labels` maps to override labels such as `group`, `victim`, `post_url`, `source`, `ransomware_alert`, `ransomware_source`, `rss_update`, `government_rss_update`, `ransomware_rss_update`, `open_article`, `open_feed`, `open_website`, and `open_screenshot`. `show_empty_fields` and `empty_field_text` do not add missing RSS metadata fields.

**Available Fields (if available):**

- `id` - Unique entry identifier
- `victim` - Target organization
- `group` - Ransomware group name
- `country` - Country (with flag emoji if enabled)
- `activity` - Attack classification
- `attack_date` - When attack occurred
- `discovered` - When attack was discovered
- `post_url` - Link to ransom post/leak page
- `website` - Victim's website
- `description` - Attack details
- `screenshot` - Ransom note screenshot
- `published` - Publication timestamp

**Available RSS Fields:**

- `title` - Article title/header
- `description` - Article summary
- `link` - Article URL/button
- `author` - Article author; rendered only when `rss.show_author` is `true`
- `categories` - RSS categories/tags
- `published` - Publication timestamp
- `feed_title` / `source` - Source feed title
- `feed_url` - Source feed URL

## Config Hot-Reload

The bot automatically checks for config file changes every 60 seconds. When a change is detected, the config is reloaded and validated without restarting the bot.

**Reloadable at runtime:**
- Poll intervals and cycle deadlines (`api_poll_interval`, `api_check_timeout`, `rss_poll_interval`, `rss_check_timeout`)
- Delays (`discord_delay`, `slack_delay`)
- Log level (`log_level`)
- API key rotation (`api_key`)
- RSS/API delivery limits and RSS settings (`max_api_entries_per_cycle`, `max_rss_entries_per_cycle`, `rss_retry_count`, `rss_retry_delay`, `rss_worker_timeout`, `rss_max_item_age`, `max_rss_workers`)
- Feed URLs and webhook enabled/disabled flags
- Webhook endpoint lists — but only by **appending** or removing from the end; a reload that reorders, inserts or rotates an endpoint URL is rejected with a WARN and the running configuration stays active (see *Destination IDs And The Endpoint Order*)
- Webhook filters and quiet hours
- Format configuration

**Require restart:**
- `data_dir`, `log_file_path`, `log_rotation`
- `webhook_request_timeout`, `webhook_max_retries`, `webhook_retry_delay`

If a reloaded config fails validation, the current config remains active and the error is logged.

## Docker Compatibility

✅ **Tested on Unraid**: This container has been successfully tested on Unraid without any issues.
✅ **Lightweight**: Docker image size is approximately ~31MB

### Building the Container

```bash
# Clone the repository
git clone <repository-url> ransomware-news-bot
cd ransomware-news-bot

# Build the Docker image. The tag is always ransomware-news-bot:latest; the Git
# commit and build date are embedded via build args and reported by --version.
RANSOMWARE_BOT_IMAGE_TAG="latest"
docker build --platform "${RANSOMWARE_BOT_PLATFORM:-linux/amd64}" \
  --build-arg COMMIT="$(git rev-parse --short HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t "ransomware-news-bot:${RANSOMWARE_BOT_IMAGE_TAG}" .

# Save the image as a tar file (optional). The handoff filename is always fixed.
docker save -o ransomware-news-bot.tar "ransomware-news-bot:${RANSOMWARE_BOT_IMAGE_TAG}"

# Deployment handoff location on the target host
mkdir -p /mnt/user/appdata/ransomwarbot
cp ransomware-news-bot.tar /mnt/user/appdata/ransomwarbot/ransomware-news-bot.tar

# Or use Docker Compose to build and run
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose up -d --build
```

### Deployment Smoke Tests

Before replacing a running Compose deployment, build the candidate image and
validate the mounted configuration:

```bash
RANSOMWARE_BOT_IMAGE_TAG="latest"
docker compose build --pull ransomware-news-bot
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose run --rm ransomware-news-bot \
  --check-config --config-dir /app/configs --data-dir /app/data
```

After deployment, verify runtime health and terminal delivery failures:

```bash
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose up -d
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose run --rm ransomware-news-bot \
  --healthcheck --config-dir /app/configs --data-dir /app/data
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose run --rm ransomware-news-bot \
  --list-dead-letter --config-dir /app/configs --data-dir /app/data
```

Use `--dry-run` for a no-send single cycle when validating a changed config or
new feed set before enabling production webhooks.

For controlled or sovereign builds, mirror the base images and Go module services, then override the build
inputs while keeping immutable digests:

```bash
docker build --platform "${RANSOMWARE_BOT_PLATFORM:-linux/amd64}" \
  --build-arg GO_BASE_IMAGE="registry.example.eu/mirror/golang:1.27-alpine@sha256:<digest>" \
  --build-arg RUNTIME_BASE_IMAGE="registry.example.eu/mirror/alpine:3.24@sha256:<digest>" \
  --build-arg ALPINE_REPOSITORY_BASE="https://apk-mirror.example.eu/alpine/v3.24" \
  --build-arg GOPROXY="https://go-proxy.example.eu,direct" \
  --build-arg GOSUMDB="sum.golang.org" \
  --build-arg COMMIT="$(git rev-parse --short HEAD)" \
  -t "ransomware-news-bot:$(git rev-parse --short HEAD)" .
```

### Updating to a Security Fix

Security fixes are documented in [CHANGELOG.md](CHANGELOG.md). For Docker Compose deployments, preserve
`/app/data`, rebuild from the fixed commit, and use the commit SHA as the image tag.

If the running deployment still uses the old service name `ransomware-bot`, run
`docker compose down` under that old name first: the service is now called
`ransomware-news-bot`, and `docker compose up` would otherwise start a second
container beside the old one, both writing to the same `./data` bind mount.

```bash
git fetch --tags
git pull --ff-only
RANSOMWARE_BOT_IMAGE_TAG="$(git rev-parse --short HEAD)"
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose build --pull
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose up -d
RANSOMWARE_BOT_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}" docker compose run --rm ransomware-news-bot --version
```

After restart, confirm the deployment is healthy and review terminal delivery failures:

```bash
./ransomware-news-bot --healthcheck --config-dir ./configs --data-dir ./data
./ransomware-news-bot --list-dead-letter --config-dir ./configs --data-dir ./data
```

If an advisory names leaked credentials, rotate the ransomware.live API key and affected Discord or Slack
webhooks before restarting the bot.

### Rollback

Before a deploy, record the currently running image tag and back up persistent
state:

```bash
PREVIOUS_IMAGE_TAG="${RANSOMWARE_BOT_IMAGE_TAG}"
tar -czf "ransomware-bot-data-$(date -u +%Y%m%dT%H%M%SZ).tgz" ./data
```

If the candidate fails smoke tests or starts producing terminal delivery
failures, return to the last known-good tag and keep the existing `./data`
directory unless the failed release corrupted status files:

```bash
RANSOMWARE_BOT_IMAGE_TAG="${PREVIOUS_IMAGE_TAG}" docker compose up -d --no-build
RANSOMWARE_BOT_IMAGE_TAG="${PREVIOUS_IMAGE_TAG}" docker compose run --rm ransomware-news-bot \
  --healthcheck --config-dir /app/configs --data-dir /app/data
```

Restore the `./data` backup only when status files are damaged or contain
bad-release-only state, then run `--list-dead-letter` to confirm recovery.

The Docker image does not bake runtime configs into the build. Keep your local
`configs/config_general.json` mounted at runtime via Compose or another volume mount.
The image does not set `DATA_DIR` by default, so `data_dir` from
`configs/config_general.json` remains effective unless you explicitly provide a
`DATA_DIR` environment override.
The container sets `RANSOMWARE_BOT_READY_FILE=/tmp/ransomware-bot.ready`, so
Docker healthchecks only pass after the scheduler has completed its initial
API/RSS checks and written the readiness marker.
A 401 or 403 auth suspension of the ransomware API keeps `--healthcheck` failing, and therefore the
container `unhealthy`, until an automatic re-probe succeeds or `api_key`/`api_base_url` changes.
A `data_dir` that does not exist keeps the container `unhealthy` with a message naming the path and the
hint that the volume may not be mounted; the check never creates the directory, so a failed bind mount
is visible instead of silently starting from empty state.
RSS staleness is measured against `3 x rss_poll_interval` (90 minutes at the shipped default): the
check turns `unhealthy` only when no enabled feed has produced a successful poll inside that window.

#### Poller Progress Markers

Two further files live beside the readiness marker: `ransomware-bot.ready.api`
and `ransomware-bot.ready.rss` (only when `RANSOMWARE_BOT_READY_FILE` is set;
the image sets it, so this is always in effect in the container). Each is
written only when its poller *completes* a pass — never when a pass is
skipped because the previous one is still running — so a poller wedged inside
a hung fetch, delivery send, or retry-queue pass now shows up as `unhealthy`
instead of the container reporting healthy forever. The allowance is three
cycles of that poller's poll interval plus its worst-case pass duration: the
RSS side counts `rss_check_timeout` once (it bounds the whole cycle), and the
API side counts `api_check_timeout` twice **as a floor, not the true worst
case** — each enabled ransomware delivery destination gets its own
`api_check_timeout` budget for delivery (one destination is the common case
this floor was written for), so the real worst-case pass duration is
`(2 + N) x api_check_timeout` (`N` = the number of enabled ransomware delivery
destinations): one budget for the fetch, one per destination for delivery, one
for the retry queue. The `x3` multiplier on top absorbs any realistic `N`, so
this is not a false-red risk in practice, but the allowance is not, strictly,
the worst-case bound its own name implies for a deployment with many enabled
destinations. At the shipped defaults the counted-as-2 formula gives 3h30m for
the API poller and 2h for the RSS poller; at every interval and timeout set
to their 1-minute floor it is still 9 and 6 minutes — well above the roughly
3-minute window Docker's own `--interval=60s --retries=3` needs to flip a
container `unhealthy`. Each marker also carries the allowance the process
computed from its own live config when it last wrote the file, and
`--healthcheck` always uses the larger of that recorded value and the value
the config on disk implies right now — so editing `api_poll_interval` or
`rss_poll_interval` up or down can never turn a healthy bot `unhealthy`: the
old marker still covers a lowered interval until the poller has had a chance
to complete a pass under it, and the freshly-computed value covers a raised
one immediately. A missing, unreadable or corrupted marker is never treated
as unhealthy by itself (fail open) — it is a fault in the bot, not something
worth restarting the container over.

Two narrower false-red shapes exist and are not fully closed by the design
above: a **forward wall-clock step** (an NTP correction, a manual clock
change) larger than the allowance, and a **host suspend/resume** (Go timers
run off the monotonic clock, which does not advance across a suspend, while
the timestamp recorded in the marker is wall-clock time) — see the
troubleshooting table below; both are reproduced against the real binary.

RSS delivery has one further gate: if RSS is enabled but `data_dir` shows no
poll attempt at all, `--healthcheck` reads the RSS progress marker's own
record of whether its last completed pass overran its `rss_check_timeout`
budget before any feed answered. A pass that overran gets a message naming
`rss_check_timeout`/`rss_worker_timeout`, not the data_dir wording below, and
this is accurate the moment that pass completes — no fixed further wait, and
correct on every subsequent cycle, not just the first. Only a pass that
genuinely completed within its budget and still recorded nothing points at a
volume mount silently failing to persist anything; a fresh start, before any
RSS progress marker exists yet, is never flagged. See the troubleshooting
table below for both operator-facing messages and what to check for each.

Docker does not notify operators when a healthcheck turns unhealthy by itself.
For Compose deployments, schedule the host-side alert check and set the webhook
destination outside the repository. `docker-compose.yml` deliberately sets no
`container_name`, so Compose v2 names the container `<project>-<service>-1`
where `<project>` defaults to your clone directory name (or an explicit `-p`/
`COMPOSE_PROJECT_NAME`), not a fixed string — find the real name before
scripting against it:

```bash
docker compose ps --format "table {{.Name}}"
```

A clone directory named exactly `Ransomware-Bot`, as this repository ships,
produces `ransomware-bot-ransomware-news-bot-1`:

```powershell
$env:HEALTH_ALERT_WEBHOOK_URL = "https://hooks.example.invalid/ops"
$env:HEALTH_ALERT_WEBHOOK_TYPE = "slack"
pwsh ./scripts/check-compose-health.ps1 -ContainerName ransomware-bot-ransomware-news-bot-1
```

A differently named clone directory (or an overridden project name) needs the
name `docker compose ps` reports instead; alternatively set
`$env:RANSOMWARE_BOT_CONTAINER_NAME` before calling the script, which reads it
as the default for `-ContainerName`.

`docker-compose.yml` runs the container as UID/GID `1000:1000` and app path `/app` by default, requires
`RANSOMWARE_BOT_IMAGE_TAG`, and defaults to `linux/amd64`. Override these when needed:

```bash
BOT_UID=$(id -u) BOT_GID=$(id -g) APP_DIR=/app RANSOMWARE_BOT_IMAGE_TAG="$(git rev-parse --short HEAD)" RANSOMWARE_BOT_PLATFORM=linux/amd64 docker compose up -d --build
```

### Required Volume Mounts

- **Configuration**: `${APP_DIR:-/app}/configs` - Mount your config directory here
- **Logs**: `${APP_DIR:-/app}/logs` - Application logs with rotation
- **Data**: `${APP_DIR:-/app}/data` - **Critical for persistence** (status tracking/deduplication)
  and `destinations.json`, which is what makes the endpoint-order check work across restarts; losing the volume loses that check as well as the dedup state

On Linux hosts, prepare writable bind mounts before the first Compose start:

```bash
mkdir -p logs data
chown -R "${BOT_UID:-1000}:${BOT_GID:-1000}" logs data
```

Or run the repository setup script before `docker compose up`:

```powershell
pwsh ./scripts/prepare-compose-host.ps1
```

The script creates `logs/` and `data/` and, on non-Windows hosts, applies the configured `BOT_UID`/`BOT_GID` ownership.

Compose gives the bot a 45 second stop grace period so it can flush status files
before Docker sends SIGKILL. Use the same timeout for manual stops:

```bash
docker compose stop -t 45 ransomware-news-bot
```

Compose keeps up to 30 Docker JSON log files at 50 MB each. If your incident
response policy requires guaranteed 90-day retention, ship Docker logs and
`./logs` to a central log store instead of relying only on local rotation.

Run only one bot instance per `/app/data` volume. At startup the scheduler creates
an exclusive `.ransomware-bot.lock` file in `data_dir`; a second writer using the
same directory exits instead of racing the status files. Shutdown releases the
operating-system lock and leaves the marker file in place intentionally.

The log file is protected the same way: `logs/bot.log.lock` is an
operating-system lock held for the life of the process. It stays on disk after a
shutdown or a crash and is reused on the next start — do not delete it while the
bot is running.

⚠️ **Important**: Without the `/app/data` volume, all processed items tracking will be lost on container restart, causing duplicate Discord messages.

## First-Run Troubleshooting

Run `go run . --check-config --config-dir ./configs` before starting the bot.
Common setup failures:

| Symptom | Fix |
| --- | --- |
| `Config directory ... does not exist` | Run from the repository root or pass the absolute `--config-dir`. |
| `api_key is required` | Set a real ransomware.live API key when ransomware webhooks are enabled. |
| `webhook URL is not valid` | Use full Discord or Slack incoming webhook URLs, not channel URLs or placeholders. |
| `data_dir` or log path errors | Create the directory and ensure the process/container UID can write to it. |
| `Could not initialize file logger; falling back to stdout-only logging` | Another bot process already owns this log file. Check for a second container or a stray process writing the same `logs/` directory; the owner's pid and host are in `logs/bot.log.lock`. A lock left behind by a crashed process is recovered automatically and needs no manual cleanup. |
| `Data directory's filesystem does not support file locking (ENOLCK/EINVAL)` | `data_dir` is on a filesystem that does not support advisory locking at all — some 9p and CIFS/SMB mounts return `ENOLCK`/`EINVAL`, or the wider `ENOSYS`/`EOPNOTSUPP`/`ENOTSUP` class, for this. The bot keeps starting (refusing to start was considered and rejected, since it would turn a working install into a dead one on an update for a risk that may never apply to that deployment) but nothing prevents a second `ransomware-bot` instance from writing the same `data_dir`. If the log directory shares that same unlockable mount with `data_dir` (true for the shipped `docker-compose.yml`, where `./logs` and `./data` are siblings, but not guaranteed in general), the log file's owner lock degrades the same way, so file logging also falls back to stdout only and `bot.log` stays empty — which means this warning itself is then visible only on stdout (`docker logs`), never in `bot.log`; check stdout either way. `--healthcheck` reports the same condition as information in its normal success summary; it never fails the check for this. A FUSE mount (including an Unraid `/mnt/user/...` share) is not reliably caught by any of this: when the FUSE server sets `no_flock`, the lock can succeed locally instead of returning an error, so neither this warning nor the healthcheck line fires for that case — an open, undetectable limitation. **Fix:** move `data_dir` (and, if it shares the mount, the log directory) to a filesystem that supports locking — on Unraid, `/mnt/cache` or a disk share instead of `/mnt/user` avoids the risk either way. |
| `quiet_hours` time or timezone errors | Use `HH:MM`, `10pm`, or `10:30 PM`, and an IANA timezone such as `Europe/Berlin`. |
| Healthcheck fails after start | Check logs for initial API/RSS errors and run `--list-dead-letter` for terminal delivery failures. |
| Feeds fail with connection errors only in a proxied network | Feed fetches ignore `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` by design; the feed host must be reachable directly. |
| `Destination remap detected ...` | The webhook endpoint order changed since the last run. Restore the previous order in `config_general.json`, or start once with `--accept-destination-remap` after reading *Destination IDs And The Endpoint Order*. |
| `Healthcheck failed: data_dir ... does not exist` | The bind mount for `data_dir` did not come up, or `--data-dir` points at the wrong path. `--healthcheck` and `--list-dead-letter` never create the directory, so this is the unmounted-volume case rather than a first-run one; on a first run the directory is created by the normal start or by `--check-config`. |
| `Healthcheck failed: all N enabled RSS feed(s) failed ...` | A total RSS outage that has lasted longer than `3 x rss_poll_interval`. Check egress from the container to the feed hosts, the named error in the message, and `--list-dead-letter` for deliveries that already went terminal. |
| `Healthcheck failed: no enabled RSS feed has succeeded since ...` | No enabled feed produced a successful poll inside `3 x rss_poll_interval`, even though the feeds may report no error. The bot is not polling, or every poll is being answered in a way that never succeeds; check the log and `rss_status.json`. |
| `RSS feeds degraded: M of N ...` | A warning only — the container stays healthy and the exit code is 0. The failing feed's error is in the log and in `rss_status.json` under that feed's `last_error`. |
| `Healthcheck failed: the API/RSS poller has completed no poll pass for ...` | That poller is wedged (hung inside a fetch, a webhook send, or the retry queue) rather than merely idle — see *Poller Progress Markers* above for the allowance formula. Check the logs around the last completed pass named in the message for what the poller was doing, and consider restarting the container if it does not recover on its own. |
| `Healthcheck failed: RSS is enabled (N feed(s) configured) but no poll attempt has been recorded since the scheduler finished starting` | `data_dir` is not persisting RSS state at all — most commonly an unwritable or misconfigured `/app/data` bind mount. Verify the mount and that the container's UID can write to it (see *Required Volume Mounts*); once fixed, this clears on the next successful RSS cycle without a restart. |
| `Healthcheck failed: RSS is enabled (N feed(s) configured) but the last completed poll pass exhausted its rss_check_timeout (...) before any feed answered; raise rss_check_timeout above rss_worker_timeout (...) so feeds have time to complete` | The RSS cycle budget is too small for these feeds: `rss_check_timeout` (default 10m, floor 1m) is expiring before `rss_worker_timeout` (default 30s, up to 5m) even gets a chance to time out a slow feed, so no feed ever completes within the cycle. Raise `rss_check_timeout` above `rss_worker_timeout` (or reduce the number/slowness of the configured feeds). This clears on the next RSS cycle that completes within its budget, without a restart — check `rss_check_timeout` vs `rss_worker_timeout`, not the data_dir mount (a different message, above, covers that case). |
| Container turns briefly `unhealthy` right after enabling RSS via a config edit (hot reload), or after wholesale-replacing the general feed list | Expected and self-healing: `data_dir` shows no RSS record yet for the newly-enabled (or newly-swapped) feed type until the first post-reload cycle completes — replacing the general feed list wholesale has the same effect, since the records for the de-configured feeds are pruned. Docker's own health window (`--retries=3` at `--interval=60s`) can restart the container before that first cycle lands, and the restart itself heals it, since a fresh start polls RSS synchronously before writing the readiness marker. No action needed; if it recurs, check that the new feed URLs and webhook are reachable. |
| Container turns `unhealthy` after a forward wall-clock step (NTP correction, manual clock change) or a host suspend/resume, with no change to the bot, its config, or the feeds | A false-red, not a real problem: the progress-marker allowance is measured against wall-clock time, so a clock jumping forward (or a host that was suspended, since Go's timers use the monotonic clock, which does not advance across a suspend, while the recorded marker timestamp is wall-clock) can make a healthy poller look stale. Self-heals one poll interval after the step or the resume, once the next pass completes and rewrites the marker; Docker may restart the container before that happens (`--retries=3` at `--interval=60s`), and the restart heals it immediately, the same way a fresh start always does. The deploy target is an Unraid host, so a sleeping host is not hypothetical — expect this after a host wakes from suspend. No action needed. |
| `Healthcheck failed: destination remap pending` | Same cause, seen through the container health probe. The bot will not start until the order is restored or the remap is accepted once. |
| `Destination manifest unreadable; rewriting it` | `destinations.json` is corrupt or was written by a newer build. The bot rewrites it and starts, but the endpoint-order check is skipped for that one start; verify the endpoint order by hand before restarting. |
| `destinations.json could not be read` / `Destination manifest unreadable` (from `--check-config` or `--healthcheck`) | Same corrupt-or-newer-build manifest, seen from the two pre-flight surfaces instead of a real start. This is a warning only, exit code unchanged (0 for `--check-config`, nil/0 for `--healthcheck`); on `--healthcheck` it is printed only when the check reaches its normal success summary, i.e. only if the dead-letter, API-auth-suspension, and RSS-health checks all pass first. Verify the endpoint order by hand, same as above. |
| `failed to load existing RSS status: ... unexpected end of JSON input` | A status file in `data_dir` is truncated or zero-length, usually from a hard kill on an older build (fixed as of this release). Restore the `data_dir` backup, or delete the named file to start with empty state for that store and accept that recent items may be re-delivered once. |
| `Configuration warning: webhook endpoints ... share one webhook URL` | Two endpoints for the same alert type point at the same URL — in one webhook block, or in two platform blocks of the same type. Intended when their filters are disjoint; if the filters overlap, that channel receives the alert once per endpoint. Give one endpoint a different filter set, or remove it. |
| `Failed to load API status before API state mutation` / `Skipping API status save: api_status.json is not loaded` | `api_status.json` in `data_dir` is present but corrupted (zero-length or truncated), usually from a hard kill on an older build. The bot starts, but every ransomware alert inside the retention window is re-delivered every cycle and nothing is persisted until this is fixed. Restore the `data_dir` backup, or delete `api_status.json` to start with empty state and accept one round of re-delivery. |
| A file was unexpectedly removed from `logs/` | Before this release, log-rotation cleanup used a wider filename match than its own backup names and could delete an unrelated file that happened to start with the log file's stem and contain its extension (e.g. `bot-notes.log.bak` next to `bot.log`). Fixed as of this release — cleanup now only ever removes files matching its own rotation naming scheme. Avoid naming other files in `logs/` starting with the log file's stem as a precaution on older builds. |
| `API rate limited; retry after ...` / API polling looks stopped | A 429 from ransomware.live with a `Retry-After` header parks the API client: further ransomware polls return this error immediately, with no network call, until the given deadline. This is by design, not a hang — polling resumes automatically on the next scheduled poll once the deadline passes, no restart needed. `Retry-After` is capped at 60 seconds, so with the default `api_poll_interval` (1 hour) the cooldown is always over long before the next cycle; only a much shorter custom interval can see it span two polls. |

Use `--dry-run` after config validation to preview formatted messages without
sending webhooks or modifying status files.

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
