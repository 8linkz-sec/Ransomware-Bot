# Security Policy

## Supported Versions

Security fixes are handled for the current `main` branch and the most recent released container/image tag, if one exists. Older private forks or locally modified deployments are supported on a best-effort basis only after the affected commit or image tag is provided.

## Private Vulnerability Reporting

Do not report suspected vulnerabilities through public GitHub issues, public Discord channels, or public Slack channels.

Preferred private reporting path:

1. Use GitHub private vulnerability reporting for this repository when it is enabled.
2. If private vulnerability reporting is not available, contact the repository owner through a private channel and include `Ransomware News Bot security report` in the subject or first line.

Never include live Discord webhook URLs, Slack webhook URLs, ransomware.live API keys, private RSS feeds, production status files, or unredacted logs in a public report.

## Scope

In scope:

- The Go application code in this repository.
- Dockerfile and Compose deployment defaults.
- Configuration parsing and validation.
- Discord, Slack, ransomware.live API, RSS parsing, retry, status, and dead-letter handling.
- Handling of secrets, webhook URLs, local status files, and logs.

Out of scope:

- Vulnerabilities in Discord, Slack, ransomware.live, GitHub, Docker Hub, or third-party RSS publishers unless this bot handles their data unsafely.
- Denial-of-service reports that require unrealistic local control of the host or unlimited resource consumption outside the bot process.
- Reports based only on a CI policy or lint rule being disabled, where that is a deliberate configuration choice visible in the workflow or config files themselves and has no demonstrated security impact. If you believe such a choice does have a concrete security impact, describe that impact and it is in scope.

## Report Contents

Include as much of the following as possible:

- Affected commit, image tag, or release.
- Deployment mode: local binary, Docker, Compose, Unraid, or another orchestrator.
- A short impact statement.
- Reproduction steps or a minimal proof of concept.
- Redacted relevant config, status, and log snippets.
- Whether any credential or webhook URL may have been exposed.

## Response Targets

These are maintainer targets, not legal guarantees:

- Acknowledge a private report within 3 business days.
- Triage reproducible reports within 10 business days.
- For validated critical or actively exploited issues, publish mitigation guidance as soon as practical and target a fix within 7 days.
- For validated high severity issues, target a fix within 14 days.
- For medium and low severity issues, target the next normal maintenance batch.

## Coordinated Disclosure

Please allow time for validation, remediation, and user notification before public disclosure. The maintainer may ask for a shorter or longer embargo when live webhook credentials, user data, or third-party provider abuse is involved.

## Operator Actions After a Security Fix

Operators should:

1. Pull or build the fixed commit/image.
2. Rotate any credentials named in the advisory.
3. Run `./ransomware-news-bot --check-config --config-dir ./configs`.
4. Run `./ransomware-news-bot --healthcheck --config-dir ./configs` after restart.
5. Review `--list-dead-letter` output for failed alert deliveries.

Security fixes should also be recorded in `CHANGELOG.md` under `Unreleased` or the released version, with affected versions, mitigation notes, and verification commands where relevant.
