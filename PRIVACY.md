# Privacy Notice

This repository provides software that operators run in their own environment. The repository maintainer does not receive runtime bot data unless an operator sends logs, status files, or support material.

Operators are responsible for deciding whether their deployment is subject to GDPR/DSGVO, CCPA/CPRA, NIS2, customer contracts, or other local rules.

## In Short

**Nothing is collected by this project.** The bot has no telemetry, no analytics, no crash reporting, no update check, and no account or registration of any kind. There is no server operated by the maintainer for it to talk to. The entire source contains exactly four outbound network calls, and every one of them goes to an endpoint the operator configured or explicitly enabled:

- the ransomware.live API, when ransomware polling is enabled (`internal/api/client.go`). The base URL defaults to `https://api-pro.ransomware.live` if the operator leaves it unset, and is contacted only once polling is enabled with the operator's own API key;
- the RSS feed URLs from the operator's own config (`internal/rss/parser.go`);
- the operator's own Discord webhook (`internal/discord/webhook.go`);
- the operator's own Slack webhook (`internal/slack/webhook.go`).

That list is verifiable rather than a promise: search the source for `http.NewRequestWithContext` and those four are all of it. The only fixed reference to this project anywhere in a request is the `User-Agent` the bot identifies itself with when fetching a feed, so feed publishers can see who is polling them.

**What running it does require.** The bot is not self-contained, and using it means accepting other providers' terms:

- a **ransomware.live account and API key** — mandatory for ransomware polling, which is the bot's primary function;
- **Discord and/or Slack**, depending on which destinations are configured;
- the operators of any **RSS feeds** that are added.

Alert content is delivered into those services and is then stored and processed under their terms, their retention settings and their region, none of which this project controls or can change. The tables further down set out exactly what leaves the bot on each of those flows.

**What does stay on the operator's machine is real personal data.** Victim names, feed content and item identifiers are written to local status files, and to the delivery audit file and logs depending on configuration. "No data is collected by us" is not the same as "no data is stored" — the sections below describe what is kept locally, for how long, and how to reduce or delete it.

## Operator Role Model

For a self-hosted deployment, the operator normally acts as controller for the bot configuration, selected sources, selected destinations, retained local status files, logs, backups, and support bundles. The bot software is a local processing tool under the operator's control.

The repository maintainer does not operate a hosted bot service and is not a processor for runtime deployments unless an operator separately sends logs, status files, configuration, or other support material. When support material is shared, redact API keys, webhook URLs, private feed URLs, victim details, RSS descriptions, and other data that is not necessary for the request.

Operators should select their own lawful basis, transparency notices, retention period, workspace access rules, and processor agreements for Discord, Slack, RSS publishers, ransomware.live, backup providers, log pipelines, and any infrastructure provider that stores runtime files.

## Data Processed By The Bot

| Data category | Source | Purpose | Stored locally |
| --- | --- | --- | --- |
| Ransomware victim names, groups, countries, claim URLs, activity, and timestamps | ransomware.live API | Alert formatting, deduplication, retry, and delivery | `api_status.json`, `retry_status.json`, logs depending on log level |
| RSS titles, links, descriptions, authors, categories, feed names, and timestamps | Configured RSS feeds | Alert formatting, deduplication, retry, and recovery | `rss_status.json`, `retry_status.json`, logs depending on log level |
| Discord and Slack webhook target metadata | Local config | Alert delivery | Config files; destination IDs may appear in retry/dead-letter status |
| API keys and webhook URLs | Local config and environment | Authentication and delivery | Should remain only in local runtime config or environment |
| Operational logs | Local process and Docker logging | Troubleshooting and operational diagnostics | `logs/`, Docker JSON logs, or operator log pipeline |

## Third-Party Disclosures

When enabled by the operator, the bot sends alert content to:

- Discord webhook endpoints configured by the operator.
- Slack webhook endpoints configured by the operator.

The bot fetches data from:

- ransomware.live API when ransomware polling is enabled.
- RSS feed URLs configured by the operator.

Operators should ensure their Discord/Slack workspaces, RSS feed choices, and ransomware.live API usage match their internal policy and legal basis.

## Processor And Transfer Map

| Flow | Data leaving the bot | Recipient role to assess | Transfer notes |
| --- | --- | --- | --- |
| ransomware.live API polling | API key in the `X-API-KEY` header, request metadata such as source IP and user agent | Upstream source/API provider | No operator alert payload is sent to the API, but API access metadata may be logged by the provider. |
| RSS feed polling | Feed URL request, source IP, user agent, and any credentials embedded in private feed URLs | Feed publisher or private-feed provider | Private RSS URLs can be credentials; treat them like secrets and verify provider location and terms. |
| Discord webhook delivery | Rendered alert content, victim/RSS metadata selected by format config, destination webhook token in transit | Operator-selected communications provider | Discord stores and processes channel messages according to the workspace/server configuration and Discord terms. |
| Slack webhook delivery | Rendered alert content, victim/RSS metadata selected by format config, destination webhook token in transit | Operator-selected communications provider | Slack stores and processes channel messages according to the workspace configuration, data residency, retention, and Slack terms. |
| Local logs, status, backups, and support bundles | Status markers, retry/dead-letter payloads, health errors, and selected alert metadata | Operator infrastructure and any backup/logging/support provider | Keep runtime paths out of Git; restrict access, encrypt backups where needed, and define deletion procedures. |

Before enabling a destination, operators should verify whether the provider is a processor, independent controller, or another role under their policy, whether a DPA is required, which region or data-residency setting applies, and whether cross-border transfer safeguards such as SCCs or equivalent terms are needed.

## Local Storage And Retention

Default local state is under `data_dir` (`./data` locally, `/app/data` in Docker):

- `api_status.json`: API delivery state capped by item count.
- `rss_status.json`: RSS parsed and sent state; parsed RSS retention defaults to 365 days plus item caps.
- `retry_status.json`: retry queue and dead-letter state; retry and dead-letter retention defaults to 30 days plus item caps.
- `destinations.json`: destination IDs and a SHA-256 hash of each webhook URL. No URL, token or host is stored. Rewritten on every successful config load; not subject to retention pruning because it only ever holds one row per configured endpoint.
- `delivery_audit.jsonl` (and its rotated backups `delivery_audit-<timestamp>.jsonl[.gz]` in the same directory): append-only record of every delivery decision (delivered, filtered, quiet_hours, stale, deduplicated, pruned, retry_queued, dead_lettered) - carries the same personal-data classes as the state files above (victim/RSS titles, feed URLs, item keys). Bounded by size rather than by item count, since it is a line-oriented log, not a keyed store: the active file is rotated once it reaches `status_retention.audit_log_rotation.max_size_mb` (default 60 MB), and up to `max_backups` (default 20) rotated backups are kept, gzip-compressed, and pruned at the next rotation once they are older than `max_age_days` (default 365). Two limits of that mechanism are worth knowing before relying on it for a retention period: the age bound applies to rotated backups only, since the active file rotates by size alone; and pruning runs only when a rotation actually happens, so on an instance that stops writing - feeds removed, or simply low volume - existing backups are not removed on age alone. All three values are adjustable under `status_retention.audit_log_rotation`.
  Line order within the file is best-effort under concurrent writers - each line's own `occurred_at` timestamp is always accurate, but two lines can very rarely land in a different physical order than the events actually occurred in; nothing reads this file positionally today (only linear/filtered scans).

Default logs rotate at 10 MB, keep 30 backups, and remove files older than 90 days. Operators can adjust `log_rotation` in `configs/config_general.json`. Operators can adjust status-file caps and age limits with `status_retention`; the delivery audit log's own bounds are described in the bullet above.

Backups, host snapshots, support bundles, Docker build contexts, and copied logs can extend retention beyond the bot's cleanup rules. Treat them as in-scope operational data.

## Minimization Controls

Operators should:

1. Keep real `configs/config_general.json` files out of Git.
2. Keep `/app/data` and `/app/logs` on restricted-access storage.
3. Encrypt backups containing `data_dir`, configs, or logs.
4. Redact webhook URLs, API keys, victim details, and RSS descriptions before sharing support bundles.
5. Disable unused webhook targets and RSS categories.
6. Review retry and dead-letter output after outages and remove data that is no longer needed.

## Access And Deletion

The bot has no hosted control plane. Access, export, correction, and deletion requests must be handled by the deployment operator using their local files, backups, Slack workspace, Discord server, and log pipeline.

Before deleting local status data, stop the bot and understand that deleting delivery state can cause duplicate alerts after restart.
