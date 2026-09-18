// Package scheduler orchestrates runtime polling, delivery, recovery, and
// shutdown for the bot.
//
// The scheduler runs separate API, RSS, config-reload, and signal/lifecycle
// loops. API cycles poll ransomware.live, apply per-destination filtering and
// quiet-hours rules, send bounded delivery batches, and replay API retry
// payloads. RSS cycles parse feeds by feed type, persist parsed entries, fan out
// fresh sends to enabled targets, and recover parsed-but-unsent entries. Dry-run
// mode uses in-memory status and logs previews instead of mutating state.
package scheduler
