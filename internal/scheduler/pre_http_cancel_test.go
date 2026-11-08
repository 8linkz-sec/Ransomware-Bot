package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// doneCallDeadlineContext models a deadline that fires at a chosen Done check.
type doneCallDeadlineContext struct{ *doneCallCancelContext }

func (c *doneCallDeadlineContext) Err() error {
	if c.doneCallCancelContext.Err() != nil {
		return context.DeadlineExceeded
	}
	return nil
}

func preSenderStopContexts() []struct {
	name   string
	new    func(int) context.Context
	reason string
} {
	return []struct {
		name   string
		new    func(int) context.Context
		reason string
	}{
		{name: "cancel", reason: "context_cancelled", new: func(at int) context.Context { return newDoneCallCancelContext(at) }},
		{name: "deadline", reason: "context_deadline_exceeded", new: func(at int) context.Context {
			return &doneCallDeadlineContext{newDoneCallCancelContext(at)}
		}},
	}
}

func TestAPIPreSenderContextEndDoesNotCreateRetryAttempt(t *testing.T) {
	for _, stop := range preSenderStopContexts() {
		for _, stopAt := range []int{1, 2} {
			t.Run(stop.name+"/"+map[int]string{1: "before_lock", 2: "after_lock"}[stopAt], func(t *testing.T) {
				s := newTestScheduler(t)
				sender := &recordingWebhookSender{}
				entry := model.RansomwareEntry{ID: "pre-send-cancel", Group: "example", Victim: "Example"}
				target := apiDeliveryTarget{
					url:           "https://hooks.slack.test/services/pre-send-cancel",
					destinationID: "slack.ransomware",
					messenger:     "slack",
					send:          sender.SendRansomwareEntry,
				}

				if s.sendAPIEntryToTarget(stop.new(stopAt), s.config, entry, target,
					model.GenerateRansomwareEntryKey(entry), "Example") {
					t.Fatal("pre-sender context end reported a successful send")
				}
				if got := sender.apiCallCount(); got != 0 {
					t.Fatalf("sender calls = %d, want 0", got)
				}
				if queued := s.statusTracker.GetRetryItemsByType(status.RetryItemTypeAPI.String()); len(queued) != 0 {
					t.Fatalf("queued attempts = %d, want 0 before any sender call", len(queued))
				}
			})
		}
	}
}

func TestAPIRetryPreSenderContextEndPreservesAttemptCount(t *testing.T) {
	for _, stop := range preSenderStopContexts() {
		t.Run(stop.name, func(t *testing.T) {
			hook := logtest.NewGlobal()
			defer hook.Reset()
			s := newTestScheduler(t)
			s.config.RetryMaxAttempts = 1
			s.config.RetryWindow = time.Hour
			sender := &recordingWebhookSender{}
			s.slackWebhookSender = sender
			entry := model.RansomwareEntry{ID: "pre-replay-cancel", Group: "example", Victim: "Example"}
			const destinationID = "slack.ransomware"
			const webhookURL = "https://hooks.slack.test/services/pre-replay-cancel"
			key := model.GenerateRansomwareEntryKey(entry)
			if !s.statusTracker.EnqueueRetryForDestination(status.RetryRequest{
				ItemKey: key, DestinationID: destinationID, Messenger: status.MessengerSlack,
				ItemType: status.RetryItemTypeAPI, Title: "Example", LastError: "first failure",
				MaxAttempts: 1, RetryWindow: time.Hour,
			}) {
				t.Fatal("initial retry enqueue failed")
			}
			if err := s.statusTracker.SavePendingChanges(); err != nil {
				t.Fatal(err)
			}
			queued := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String())
			if len(queued) != 1 {
				t.Fatalf("initial queued items = %d, want 1", len(queued))
			}
			job := apiRetryJob{
				item: queued[0].Item, queueKey: queued[0].QueueKey, destinationID: destinationID,
				webhook: apiRetryWebhookInfo{url: webhookURL}, entry: entry, primaryKey: key, title: "Example",
			}
			changed := false
			// Done() call 1: processAPIRetryJob's outer check. Call 2: the first
			// sendWithWebhookLock check, before the sender closure is invoked.
			if s.processAPIRetryJob(stop.new(2), s.config, job, func() { changed = true }) {
				t.Fatal("pre-sender context end should stop replay processing")
			}
			if changed {
				t.Fatal("pre-sender context end changed the retry row")
			}
			logEntry := findTestLogEntry(hook, "Context ended before API retry send")
			if logEntry == nil || logEntry.Data["skip_reason"] != stop.reason {
				t.Fatalf("pre-sender skip log = %+v, want reason %q", logEntry, stop.reason)
			}
			if got := sender.apiCallCount(); got != 0 {
				t.Fatalf("sender calls = %d, want 0", got)
			}
			if err := s.statusTracker.SavePendingChanges(); err != nil {
				t.Fatal(err)
			}
			reloaded, err := status.NewTracker(s.config.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			items := reloaded.GetRetryItemsByType(status.RetryItemTypeAPI.String())
			if len(items) != 1 || items[0].RetryCount != 1 || items[0].LastError != "first failure" {
				t.Fatalf("retry row after pre-sender context end = %+v, want original count 1 and error", items)
			}
			if dead := reloaded.GetDeadLetterItems(); len(dead) != 0 {
				t.Fatalf("dead letters = %d, want 0", len(dead))
			}
		})
	}
}

func TestRSSPreSenderContextEndDoesNotCreateRetryAttempt(t *testing.T) {
	for _, stop := range preSenderStopContexts() {
		t.Run(stop.name, func(t *testing.T) {
			s := newTestScheduler(t)
			sender := &recordingWebhookSender{}
			s.slackWebhookSender = sender
			entry := model.RSSEntry{
				Title: "Pre-send cancel", GUID: "pre-send-cancel", FeedURL: "https://feeds.test/rss.xml",
				FeedTitle: "Example", Published: time.Now().UTC(),
			}
			target := webhookTarget{
				url: "https://hooks.slack.test/services/pre-rss-cancel", destinationID: "slack.rss.general", messenger: "slack",
			}
			// Done() call 1: RSS entry check. Call 2: sendWithWebhookLock before
			// sender invocation. The zero send delay adds no context check.
			outcome := s.sendRSSEntryToTarget(stop.new(2), s.config, config.FeedTypeGeneral,
				target, freshRSSDeliveryItem(entry), rssDeliveryContext{mode: rssDeliveryModeFresh, total: 1})
			if outcome != rssDeliveryOutcomeCanceled {
				t.Fatalf("RSS outcome = %v, want canceled", outcome)
			}
			if got := sender.rssCallCount(); got != 0 {
				t.Fatalf("sender calls = %d, want 0", got)
			}
			if queued := s.statusTracker.GetRetryItemsByType(status.RetryItemTypeRSS.String()); len(queued) != 0 {
				t.Fatalf("queued attempts = %d, want 0 before any sender call", len(queued))
			}
		})
	}
}
