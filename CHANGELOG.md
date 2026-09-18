# Changelog

## 1.2.1 - 2026-09-18

### Improved

- Config reload log lines no longer contain a fingerprint of your API key or webhook URLs.

## 1.2.0 - 2026-09-18

Biggest update so far: flexible routing, better filters, a hardened container.

### Upgrade notes

- Renamed to `ransomware-news-bot`. Stop the old container before starting the
  new one, or two bots run side by side.
- `config_general.json` is no longer shipped. Copy `config_general.example.json`.
- Run `--check-config` before upgrading. Feed and webhook URLs are checked
  more strictly.
- Feeds must use HTTPS.
- `retry_max_attempts` now counts retries after the first send (`5` = 6 sends).
- Docker Compose now needs `RANSOMWARE_BOT_IMAGE_TAG` (e.g. `latest`).
- Reordering webhook URLs blocks startup until you start once with
  `--accept-destination-remap`. Adding URLs at the end is fine.
- Rotate webhook or feed tokens that may have appeared in old logs.

### New

- Several webhooks per alert type, each with its own filters and quiet hours.
- Slack webhooks on your own approved hosts, not only `hooks.slack.com`.
- Filter on individual fields. Custom field labels, date formats and time zones; country names in your language.
- CERT-EU and CERT-FR feeds.
- Delivery log that shows why an alert was or was not sent.
- Real Docker healthcheck, plus `--healthcheck` and `--list-dead-letter`.
- More settings apply without a restart.

### Improved

- Fewer duplicate alerts.
- Protection against spoofed links and hidden characters in feed text.
- Go 1.27 and Alpine 3.24; fewer third-party dependencies.
- Hardened container: read-only file system, all capabilities dropped, base images pinned by digest.

## 1.1.0 - 2026-02-20

- Filters per webhook: group, country, keyword, category.
- Quiet hours per webhook.
- Retries for failed deliveries.
- Config changes apply without a restart.
- `--check-config` and `--dry-run`.
- Support for the new ransomware.live time format.

## 1.0.0 - 2025-08-04

- Ransomware alerts from ransomware.live and security RSS feeds to Discord.
- Slack support.
- Container runs as a non-root user.
- New feeds: Canadian Cyber Centre, Microsoft Security, Cisco Talos, SANS ISC.
- Fixed duplicate posts across feeds.
- Config folder moved to `/app/configs`. Update your volume mount.
