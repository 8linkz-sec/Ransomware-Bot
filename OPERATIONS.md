# Ransomware News Bot - Operations Guide

This guide holds the operator detail behind [readme.md](readme.md): how destination IDs and the
endpoint-order check work, what `--healthcheck` evaluates, the finer points of selected config
options, special builds, and the long explanations behind the
[Troubleshooting](readme.md#troubleshooting) table. Setup, the config reference and the everyday
commands stay in the README.

## Contents

- [Destination IDs and the endpoint order](#destination-ids-and-the-endpoint-order)
- [Healthcheck](#healthcheck)
- [Configuration details](#configuration-details)
- [Building images](#building-images)
- [Troubleshooting details](#troubleshooting-details)

## Destination IDs and the endpoint order

The short rule is in the README: [append endpoints only at the end](readme.md#webhook-endpoint-order).
This section explains why and what the bot does about it.

### Positional IDs

Every webhook endpoint gets a **positional** destination ID. Within one webhook block the endpoints
are numbered by their position in the flattened `url` -> `urls` -> `targets` list (`.2`, `.3`, ...
for the second and later endpoints), and the ID is what the bot stores: deduplication markers, the
retry queue and dead letters are all keyed by `(item, destination ID)`, never by URL. A
`ransomware` block gets two ID families: `<platform>.ransomware[.N]` for API alerts and
`<platform>.rss.ransomware[.N]` for ransomware RSS feeds; `rss` and `government` blocks get
`<platform>.rss.general[.N]` and `<platform>.rss.government[.N]`.

Reordering endpoints, inserting one in front of an existing one, or deleting one that is not the
last moves every later endpoint's URL onto a different ID. The stored history of one channel then
applies to another: alerts are suppressed for the channel that should receive them, and queued
retries are delivered to the wrong webhook. Blanking a URL counts as deleting it, because blank
entries are dropped before numbering. Converting `urls` to `targets` while keeping the order is
safe, and enabling or disabling a whole block is safe, but the endpoint order inside a block is
guarded even while the block is disabled, so reordering it will refuse the next start.

### The `destinations.json` manifest

The bot keeps `destinations.json` in `data_dir`. It maps active destination IDs to SHA-256 hashes
of their webhook URLs and keeps the last hash for each removed ID. It stores hashes only, never a
URL or token. It is written on every successful config load and created silently on the first
start. Keep the file with the status files: deleting it loses the exact URL hashes. If sent
markers, retries, or dead letters remain, the bot treats their old destination bindings as unknown
and requires `--accept-destination-remap` before using new positional IDs. Unreadable status files
also require explicit acceptance. Poll and feed health records alone carry no destination binding.

If the URL behind an existing ID has changed since the last run, the bot **refuses to start** and
logs an ERROR naming the affected IDs, what changed, the consequence, and the way out;
`--check-config` prints the same text and exits 1, and `--healthcheck` reports
`destination remap pending` and exits 1. A hot reload that would remap is rejected with a WARN and
the running configuration stays active. Example:

```text
Destination remap detected (endpoints reordered): discord.ransomware<-discord.ransomware.2,
discord.ransomware.2<-discord.ransomware. Stored dedup markers, retry-queue rows and dead
letters are keyed by the destination ID, so starting with this configuration could silently
suppress alerts for one endpoint and deliver another endpoint's queued messages to the wrong
webhook. Restore the previous endpoint mapping (and destinations.json from backup if
unreadable), or start once with --accept-destination-remap to accept the new mapping and
rewrite /app/data/destinations.json.
```

`X<-Y` reads "the endpoint now stored under X was previously stored under Y"; several previous
owners are joined with `+`, which happens when two endpoints share a URL. `X<-*` means the URL now
behind X was not in the manifest at all, which is what a rotated webhook token looks like. The
headline in brackets is only a hint: a single edit can reorder, insert and rotate at once, and the
movement list is the authoritative description.

### The two ways out

1. **Restore the previous order** in `configs/config_general.json` and start normally. Nothing is
   lost; this is the right answer whenever the reorder was accidental.
2. **Start once with `--accept-destination-remap`** if the new mapping is what you want. The
   manifest is rewritten to the new mapping and the bot starts. Every stored marker, queued retry
   and dead letter keeps its old ID, so the affected channels inherit each other's history: one
   may go quiet for items it never received, and another may receive a queued message meant for
   its neighbour. To start clean instead, stop the bot, delete `destinations.json` **and** the
   status files in `data_dir`, and accept that recent alerts may be re-delivered.

### Removed endpoints and URL comparison

Deleting endpoints from the end is fine and needs no flag; the bot logs one INFO naming the removed
IDs and how many retry-queue and dead-letter rows still carry them. Those rows expire with
`retry_queue_max_age` / `dead_letter_max_age`. The manifest retains removed IDs and their hashes:
re-adding an endpoint at the same position with the same URL is safe, while reusing that position
for a different URL requires `--accept-destination-remap`. This also catches a swap made across
two starts with an empty or shortened endpoint list between them.

The check compares URLs byte-exactly after trimming surrounding whitespace, the same normalisation
the deduplication key uses. Re-typing one endpoint's URL with a trailing slash or a different host
capitalisation therefore reads as a changed URL and refuses the start.

### Unreadable manifests and manifest versions

An unreadable manifest (corrupt, or a version this build does not know) makes old bindings
unknown. With configured endpoints, startup, `--check-config`, `--healthcheck`, and hot reload
refuse the mapping until an operator explicitly accepts it at startup. An empty-endpoint start may
rewrite the file, but keeps the uncertainty for later IDs. A manifest write failure stops startup
or rejects a hot reload before new endpoints can send; check the `data_dir` volume and permissions
before retrying.

Version-1 manifests are upgraded on the next successful write. A version-1 manifest may already
have forgotten removed IDs, so during the upgrade the bot looks for those IDs in persisted sent
markers, retries and dead letters; reusing one requires explicit acceptance because its old URL
hash is unknowable. A marker without a destination ID has an irreversible hashed key; when the
upgrade sees such state, each new positional ID requires explicit acceptance until a clean
status-and-manifest reset, and the first accepted ID does not authorize later IDs. An ordinary
appended ID remains allowed when the stored state proves that it is fresh. The status check also
recovers IDs a version-2 manifest missed after a write error. If you intentionally reset all
status files, also remove `destinations.json` before starting to create a clean history.

Version-2 manifests contain the removed-ID history. Older binaries cannot enforce this format: on
rollback they treat it as unreadable and may rewrite it, losing that history. Keep a backup of
`destinations.json` before a rollback and restore it when returning to the newer binary; do not
change endpoint order while running the older binary.

### Shared webhook URLs

Two endpoints may point at the same URL, and each delivers independently, so this is a supported
way to give one channel two different filter sets. Where those filter sets overlap, the same alert
is posted to that channel once per endpoint. Configuration load and `--check-config` therefore
print a warning naming the endpoints, for example `discord.ransomware` and
`discord.ransomware.2`; neither the URL nor its token is ever logged. The warning is informational:
the configuration stays valid and the bot starts normally. Endpoints of *different* alert types
that share a URL are not warned about, because no single alert can reach both. Endpoints of the
*same* alert type are warned about even when they are configured in different platform blocks,
because every alert of that type is delivered to all of them.

## Healthcheck

The image runs `--healthcheck` as its Docker `HEALTHCHECK` (every 60 s, 3 retries). The check exits
0 and prints `Healthcheck valid`, or exits 1 and prints `Healthcheck failed: <reason>`.

### What it checks

In order, the first failure wins:

1. The config directory exists and the configuration loads.
2. `data_dir` exists and is writable. The check never creates it, so a failed bind mount shows up
   as `unhealthy` with a message naming the path, instead of silently starting from empty state.
3. No [destination remap](#destination-ids-and-the-endpoint-order) is pending.
4. The scheduler readiness marker exists (only when `RANSOMWARE_BOT_READY_FILE` is set). The image
   sets `RANSOMWARE_BOT_READY_FILE=/tmp/ransomware-bot.ready`, so Docker healthchecks only pass
   after the scheduler has completed its initial API/RSS checks and written the marker.
5. Neither poller is wedged (see [Poller progress markers](#poller-progress-markers)).
6. The dead-letter queue is empty; otherwise run `--list-dead-letter`.
7. When ransomware delivery is enabled, the last ransomware API check did not fail. A 401 or 403
   auth suspension is reported by name and keeps the check failing until an automatic re-probe
   succeeds or `api_key`/`api_base_url` changes.
8. RSS health, when RSS is enabled (see [RSS health](#rss-health)).

On success the summary may also carry warnings that never fail the check: an unreadable
destination manifest, degraded RSS feeds, and a `data_dir` filesystem without file locking.

### RSS health

- If RSS is enabled but `data_dir` shows no poll attempt at all, the check reads the RSS progress
  marker's own record of whether its last completed pass overran its `rss_check_timeout` budget
  before any feed answered. A pass that overran gets a message naming
  `rss_check_timeout`/`rss_worker_timeout`. Only a pass that completed within its budget and still
  recorded nothing points at a volume mount silently failing to persist anything. Both are
  accurate the moment that pass completes. A fresh start, before any RSS progress marker exists,
  is never flagged.
- Otherwise the check turns unhealthy only when no enabled feed has succeeded within
  `3 x rss_poll_interval` (90 minutes at the shipped default), whether or not the feeds report an
  error. A single failing feed, and even a transient failure that hits every feed at once, is a
  warning line with exit 0.

### Poller progress markers

Two further files live beside the readiness marker: `ransomware-bot.ready.api` and
`ransomware-bot.ready.rss` (only when `RANSOMWARE_BOT_READY_FILE` is set; the image sets it). Each
is written only when its poller *completes* a pass, never when a pass is skipped because the
previous one is still running, so a poller wedged inside a hung fetch, delivery send, or
retry-queue pass shows up as `unhealthy`.

The allowance is three cycles of the poller's poll interval plus its pass duration:

- RSS: `3 x (rss_poll_interval + rss_check_timeout)`; `rss_check_timeout` bounds the whole cycle.
- API: `3 x (api_poll_interval + 2 x api_check_timeout)`. Counting `api_check_timeout` twice is a
  floor, not the true worst case: each enabled ransomware delivery destination gets its own
  `api_check_timeout` budget, so the real worst-case pass is `(2 + N) x api_check_timeout` (one
  budget for the fetch, one per destination, one for the retry queue). The `x3` multiplier absorbs
  any realistic `N`, so this is not a false-red risk in practice.

At the shipped defaults this gives 3h30m for the API poller and 2h for the RSS poller; with every
interval and timeout at its 1-minute floor it is still 9 and 6 minutes, well above the roughly
3-minute window Docker's `--interval=60s --retries=3` needs to flip a container `unhealthy`.

Each marker also records the allowance the process computed from its live config, and the check
uses the larger of that value and the value the config on disk implies now. Editing
`api_poll_interval` or `rss_poll_interval` up or down therefore never turns a healthy bot
`unhealthy`. A missing, unreadable or corrupted marker is never treated as unhealthy by itself
(fail open).

Two narrow false-red cases remain: a **forward wall-clock step** (an NTP correction, a manual clock
change) larger than the allowance, and a **host suspend/resume** (Go timers run off the monotonic
clock, which does not advance across a suspend, while the marker timestamp is wall-clock time).
See [Clock steps and host suspend](#clock-steps-and-host-suspend).

### Alerting on an unhealthy container

Docker does not notify anyone when a healthcheck turns unhealthy. For Compose deployments, schedule
`scripts/check-compose-health.ps1` on the host and set the alert webhook outside the repository:

```powershell
$env:HEALTH_ALERT_WEBHOOK_URL = "https://hooks.example.invalid/ops"
$env:HEALTH_ALERT_WEBHOOK_TYPE = "slack"   # slack, discord or generic (default)
pwsh ./scripts/check-compose-health.ps1
```

The script finds the container by its Compose service label (`ransomware-news-bot`), so it works
whatever the project is called. If several Compose projects run this service on one host, pass
`-ComposeProject` (or `RANSOMWARE_BOT_COMPOSE_PROJECT`); to pin an exact container, pass
`-ContainerName` (or `RANSOMWARE_BOT_CONTAINER_NAME`). `docker compose ps` shows the real name.

## Configuration details

The [config table](readme.md#configuration-options) in the README gives each option in one line.
The points below need more than that.

### RSS cycle budgets

- `rss_check_timeout` is the deadline for one RSS poll cycle (fetch, delivery and recovery). When
  it expires, feeds that already answered are processed normally and the remaining feeds are
  treated as not polled; they are not recorded as failures. Set it above `rss_worker_timeout`.
- `rss_worker_timeout` is the budget for one RSS feed batch, the whole worker pool, not a single
  feed. A feed that has not answered when it expires is recorded as a feed timeout.

### RSS item age limit

`rss_max_item_age` items older than the limit are skipped for the current cycle but are not marked
as sent, so increasing the value later can still recover them.

Items whose publication date cannot be read are exempt from this limit by design: treating an
unknown age as "too old" would drop every item of a feed whose date format the parser cannot read.
Such an item is delivered once, pruned after `status_retention.rss_parsed_max_age`, and delivered
again if the publisher still carries it in the feed at that point, once per retention period. The
signal to act on is the one-per-feed WARN `RSS feed timestamp could not be parsed`, which names the
feed and the raw value.

### Retry counting

- `retry_max_attempts` counts **retries after the first send**, per item and destination, before
  the item is dead-lettered: `N` allows `N + 1` sends in total (`5` = six sends, `1` = two sends,
  `100` = up to 101 sends). `0` means unlimited retries within `retry_window` and is only legal
  together with a non-zero `retry_window`.
- `webhook_max_retries` applies to provider-declared rate limits. Transport errors and ambiguous
  5xx responses are not automatically retried.

### Status retention

API sent markers are retained by count to avoid replaying old alerts after upstream
reintroductions. RSS parsed entries are retained until delivered to active targets and then pruned
by count or age; retry and dead-letter records use their configured queue age and count bounds.

### Delivery audit log sizing

`delivery_audit.jsonl` is a line-oriented log, not a keyed store, so it is rotated by size
(`status_retention.audit_log_rotation`) rather than by item count. `max_size_mb` here (as in
`log_rotation`) is measured in **MiB** (1,048,576 bytes), despite the key's name.

The defaults bound it at roughly **1,260 MiB uncompressed worst case** (`max_size_mb x
(max_backups + 1)` = 60 x 21; about 1,322 MB decimal) and about **300 MiB** (~315 MB) in practice
with `compress: true` (`max_backups x max_size_mb / 5` for the compressed backups plus the active
file). No warning is emitted until that product exceeds 5,000 MiB, so size the `data_dir` volume
for the uncompressed figure.

**The first rotation of a pre-existing, large `delivery_audit.jsonl` exceeds that ceiling.** When a
deployment upgrades from a build without size-based rotation, the whole legacy file becomes one
oversized backup (rotation prunes by count and age, never by size), and with `compress: true` a
growing `.gz.tmp` sits beside it while compression runs. Peak usage during that one rotation is
the ceiling above **plus the legacy file's size, plus about a fifth of that size again**. Running
out of space does not just lose audit lines (rotation and compression failures are warn-only and
delivery is unaffected); it can also make saving `api_status.json`, `rss_status.json`, and
`retry_status.json` fail, which causes re-delivery on the next cycle.

### Target inheritance of filters and quiet hours

A `targets[]` entry that omits `filters` and/or `quiet_hours` inherits the parent block's value
for that key, independently per key, exactly like `url`/`urls[]` endpoints. An explicit
`"filters": {}` or `"quiet_hours": {}` opts the target out: an empty object matches every entry or
never pauses delivery, unlike omitting the key. Loading or reloading a config where a target newly
inherits a value logs one WARN per block naming every affected endpoint.

If a block defines `filters` and/or `quiet_hours` but every one of its targets declares its own,
that block-level value was dead configuration and never validated. Once a target starts
inheriting it, an invalid block-level value can make the config fail to load for the first time;
for `quiet_hours` this includes a block with `enabled: false`, since `quiet_hours` validation
ignores `enabled`. The error names the block, the field, why it is invalid, and that it came from
inheritance. Fix it by removing the invalid field from the block or by adding an explicit
`"filters": {}` / `"quiet_hours": {}` to the target the error names.

### Feed URL rules

Feed URLs must be HTTPS, without credentials, sensitive query parameters or fragments, and must
resolve to a public address. Private, loopback, link-local, CGNAT, reserved and IPv6-transition
addresses embedding those (NAT64 `64:ff9b::/96`, 6to4 `2002::/16`, ISATAP) are refused at load time
and again at connect time, and the prefixes whose embedded IPv4 address cannot be recovered
unambiguously (`::/96`, `64:ff9b:1::/48`, Teredo `2001::/32`) are refused outright. Feed fetches
are direct and ignore `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`, while webhook deliveries honour
them.

## Building images

The everyday build, update and rollback sequence is in the
[README](readme.md#build-update-and-roll-back).

### Controlled or sovereign builds

Mirror the base images, the Alpine package repositories and the Go module services, then override
the build inputs. The defaults use the line tags `golang:1.27-alpine` and `alpine:3.24`, and every
build runs `apk upgrade`, so a rebuild picks up current security updates; keep your mirror tags on
the same lines:

```bash
docker build --platform "${RANSOMWARE_BOT_PLATFORM:-linux/amd64}" \
  --build-arg GO_BASE_IMAGE="registry.example.eu/mirror/golang:1.27-alpine" \
  --build-arg RUNTIME_BASE_IMAGE="registry.example.eu/mirror/alpine:3.24" \
  --build-arg ALPINE_REPOSITORY_BASE="https://apk-mirror.example.eu/alpine/v3.24" \
  --build-arg GOPROXY="https://go-proxy.example.eu,direct" \
  --build-arg GOSUMDB="sum.golang.org" \
  --build-arg COMMIT="$(git rev-parse --short HEAD)" \
  -t "ransomware-news-bot:$(git rev-parse --short HEAD)" .
```

### Exporting an image

To move an image to a host without a build environment, save it as a tar file and load it there
with `docker load`:

```bash
docker save -o ransomware-news-bot.tar "ransomware-news-bot:${RANSOMWARE_BOT_IMAGE_TAG}"
```

## Troubleshooting details

Long explanations behind rows of the [README troubleshooting table](readme.md#troubleshooting).

### RSS cycle budget overrun

`... the last completed poll pass exhausted its rss_check_timeout (...) before any feed answered`
means `rss_check_timeout` (default 10m, floor 1m) expires before `rss_worker_timeout` (default 30s,
up to 5m) even gets a chance to time out a slow feed, so no feed ever completes within the cycle.
Raise `rss_check_timeout` above `rss_worker_timeout`, or reduce the number or slowness of the
configured feeds. It clears on the next RSS cycle that completes within its budget, without a
restart.

### Briefly unhealthy after an RSS hot reload

After enabling RSS through a config edit, or after replacing the general feed list wholesale (the
records for the removed feeds are pruned), `data_dir` has no RSS record for the new feeds until
the first post-reload cycle completes. Docker's health window (`--retries=3` at `--interval=60s`)
can restart the container before that cycle lands; the restart heals it, because a fresh start
polls RSS synchronously before writing the readiness marker. No action is needed; if it recurs,
check that the new feed URLs and the webhook are reachable.

### Clock steps and host suspend

The progress-marker allowance is measured against wall-clock time, so a clock jumping forward, or
a host that was suspended, can make a healthy poller look stale. It self-heals one poll interval
after the step or the resume, once the next pass completes and rewrites the marker. Docker may
restart the container before that; the restart heals it immediately. Expect this after a host
wakes from suspend. No action is needed.

### API rate limit

A 429 from ransomware.live with a `Retry-After` header parks the API client: further ransomware
polls return `API rate limited; retry after ...` immediately, with no network call, until the given
deadline. Polling resumes automatically on the next scheduled poll once the deadline passes, with
no restart. `Retry-After` is capped at 60 seconds, so with the default `api_poll_interval` (1 hour)
the cooldown is always over long before the next cycle; only a much shorter custom interval can
see it span two polls.
