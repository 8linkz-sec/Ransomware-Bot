package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	log "github.com/sirupsen/logrus"
)

type deliveredAuditPauseHook struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *deliveredAuditPauseHook) Levels() []log.Level { return log.AllLevels }

func (h *deliveredAuditPauseHook) Fire(entry *log.Entry) error {
	if entry.Message == "Failed to append delivery audit event" && entry.Data["outcome"] == status.DeliveryAuditOutcomeDelivered {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return nil
}

func TestAPIStatusCannotPersistBeforeDeliveredAuditCompletes(t *testing.T) {
	s := newTestScheduler(t)
	auditPath := filepath.Join(s.config.DataDir, "delivery_audit.jsonl")
	if err := os.Mkdir(auditPath, 0o700); err != nil {
		t.Fatal(err)
	}

	hook := &deliveredAuditPauseHook{entered: make(chan struct{}), release: make(chan struct{})}
	previous := make(log.LevelHooks)
	for level, hooks := range log.StandardLogger().Hooks {
		previous[level] = append([]log.Hook(nil), hooks...)
	}
	log.AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(previous) })
	defer func() {
		select {
		case <-hook.release:
		default:
			close(hook.release)
		}
	}()

	entry := model.RansomwareEntry{ID: "audit-order", Group: "example", Victim: "Example"}
	key := model.GenerateRansomwareEntryKey(entry)
	const destinationID = "slack.ransomware"
	target := apiDeliveryTarget{
		url:           "https://hooks.slack.test/services/audit-order",
		destinationID: destinationID,
		messenger:     "slack",
		send: func(context.Context, string, model.RansomwareEntry, *notifyfmt.FormatOptions) error {
			return nil
		},
	}
	done := make(chan bool, 1)
	go func() {
		done <- s.sendAPIEntryToTarget(context.Background(), s.config, entry, target, key, "Example")
	}()
	select {
	case <-hook.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("successful API send did not reach its delivered audit")
	}

	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.IsAPIItemSentToDestination(key, destinationID) {
		t.Fatal("api_status.json persisted the sent marker before the delivered audit call completed")
	}

	close(hook.release)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("successful API send reported failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("API send did not complete after releasing audit hook")
	}
	postSend, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !postSend.IsAPIItemSentToDestination(key, destinationID) {
		t.Fatal("successful API send did not persist its sent marker after the audit call completed")
	}
}

func TestAPIRetryStatusCannotPersistBeforeDeliveredAuditCompletes(t *testing.T) {
	s := newTestScheduler(t)
	s.slackWebhookSender = &recordingWebhookSender{}
	if err := os.Mkdir(filepath.Join(s.config.DataDir, "delivery_audit.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}

	hook := &deliveredAuditPauseHook{entered: make(chan struct{}), release: make(chan struct{})}
	previous := make(log.LevelHooks)
	for level, hooks := range log.StandardLogger().Hooks {
		previous[level] = append([]log.Hook(nil), hooks...)
	}
	log.AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(previous) })
	defer func() {
		select {
		case <-hook.release:
		default:
			close(hook.release)
		}
	}()

	job := apiRetryJobForTest("audit-order-retry", status.MessengerSlack.String())
	done := make(chan bool, 1)
	go func() {
		done <- s.processAPIRetryJob(context.Background(), s.config, job, func() {})
	}()
	select {
	case <-hook.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("successful API retry did not reach its delivered audit")
	}

	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.IsAPIItemSentToDestination(job.primaryKey, job.destinationID) {
		t.Fatal("api_status.json persisted the retry success marker before the delivered audit call completed")
	}

	close(hook.release)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("successful API retry reported failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("API retry did not complete after releasing audit hook")
	}
	postSend, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !postSend.IsAPIItemSentToDestination(job.primaryKey, job.destinationID) {
		t.Fatal("successful API retry did not persist its sent marker after the audit call completed")
	}
}
