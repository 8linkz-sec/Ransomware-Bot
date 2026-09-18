// Package rss fetches and normalizes RSS feed entries.
//
// Parser instances fetch HTTP/HTTPS feeds with bounded response and item
// limits and normalize feed items into Entry values. ParseMultipleFeeds may
// return partial results together with per-feed errors, including timeout
// placeholders for feeds that did not return before the worker-pool deadline.
package rss
