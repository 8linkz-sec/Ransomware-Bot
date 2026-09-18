// Package status persists the bot's local runtime state.
//
// The package owns `api_status.json`, `rss_status.json`, and
// `retry_status.json`. API state stores health and per-destination sent markers.
// RSS state uses a two-phase parsed/sent model so parsed feed entries can be
// recovered for destinations that were paused or failed. Retry state stores
// replay payloads and terminal dead-letter records. The tracker keeps in-memory
// indexes and dirty flags; callers should call SavePendingChanges after batch
// mutations and handle its aggregated persistence error.
package status
