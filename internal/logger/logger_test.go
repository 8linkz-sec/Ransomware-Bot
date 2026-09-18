package logger

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func preserveLoggerState(t *testing.T) {
	t.Helper()
	std := logrus.StandardLogger()
	oldOut := std.Out
	oldFormatter := std.Formatter
	oldLevel := std.Level
	oldHooks := make(logrus.LevelHooks, len(std.Hooks))
	for level, hooks := range std.Hooks {
		oldHooks[level] = append([]logrus.Hook(nil), hooks...)
	}

	logFileWriterMu.Lock()
	oldLogFileWriter := logFileWriter
	oldServiceHookInstalled := serviceHookInstalled
	logFileWriterMu.Unlock()

	t.Cleanup(func() {
		if err := Close(); err != nil {
			t.Errorf("close logger: %v", err)
		}
		logrus.SetOutput(oldOut)
		logrus.SetFormatter(oldFormatter)
		logrus.SetLevel(oldLevel)
		std.Hooks = oldHooks
		logFileWriterMu.Lock()
		logFileWriter = oldLogFileWriter
		serviceHookInstalled = oldServiceHookInstalled
		logFileWriterMu.Unlock()
	})
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input     string
		expected  logrus.Level
		shouldErr bool
	}{
		{"TRACE", logrus.TraceLevel, false},
		{"DEBUG", logrus.DebugLevel, false},
		{"INFO", logrus.InfoLevel, false},
		{"WARNING", logrus.WarnLevel, false},
		{"WARN", logrus.WarnLevel, false},
		{"ERROR", logrus.ErrorLevel, false},
		{"debug", logrus.DebugLevel, false},
		{"info", logrus.InfoLevel, false},
		{"warning", logrus.WarnLevel, false},
		{"error", logrus.ErrorLevel, false},
		{"INVALID", logrus.InfoLevel, true},
		{"", logrus.InfoLevel, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			level, err := parseLogLevel(tt.input)
			if tt.shouldErr {
				if err == nil {
					t.Error("expected error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if level != tt.expected {
					t.Errorf("expected %v, got %v", tt.expected, level)
				}
			}
		})
	}
}

func TestNewLoggerCreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	logPath := filepath.Join(tmpDir, "test.log")
	rotCfg := LogRotationConfig{
		MaxSizeMB:  10,
		MaxBackups: 3,
		MaxAgeDays: 7,
		Compress:   false,
	}

	err := NewLogger("INFO", logPath, rotCfg)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Verify log file was created
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("expected log file to be created")
	}
	if _, ok := logrus.StandardLogger().Formatter.(*logrus.JSONFormatter); !ok {
		t.Fatalf("formatter type = %T, want *logrus.JSONFormatter", logrus.StandardLogger().Formatter)
	}
}

func TestNewLoggerInvalidLevel(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	logPath := filepath.Join(tmpDir, "test.log")
	rotCfg := LogRotationConfig{MaxSizeMB: 10, MaxBackups: 3, MaxAgeDays: 7}

	err := NewLogger("INVALID", logPath, rotCfg)
	if err == nil {
		t.Error("expected error for invalid log level")
	}
}

func TestNewStdoutLogger(t *testing.T) {
	preserveLoggerState(t)

	if err := NewStdoutLogger("INFO"); err != nil {
		t.Fatalf("NewStdoutLogger() error = %v", err)
	}
	if logrus.GetLevel() != logrus.InfoLevel {
		t.Fatalf("logrus level = %v, want %v", logrus.GetLevel(), logrus.InfoLevel)
	}
	if logFileWriter != nil {
		t.Fatal("NewStdoutLogger left a file logger configured")
	}
	formatter, ok := logrus.StandardLogger().Formatter.(*logrus.JSONFormatter)
	if !ok {
		t.Fatalf("formatter type = %T, want *logrus.JSONFormatter", logrus.StandardLogger().Formatter)
	}
	if formatter.TimestampFormat != time.RFC3339 {
		t.Fatalf("TimestampFormat = %q, want %q", formatter.TimestampFormat, time.RFC3339)
	}
}

func TestNewStdoutLoggerAddsServiceField(t *testing.T) {
	preserveLoggerState(t)

	if err := NewStdoutLogger("INFO"); err != nil {
		t.Fatalf("NewStdoutLogger() error = %v", err)
	}

	var buf bytes.Buffer
	logrus.SetOutput(&buf)
	logrus.Info("probe")

	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatalf("log output is not JSON: %v; output=%q", err, buf.String())
	}
	if fields["service"] != serviceName {
		t.Fatalf("service field = %v, want %q in output %q", fields["service"], serviceName, buf.String())
	}
}

func TestNewStdoutLoggerInvalidLevel(t *testing.T) {
	preserveLoggerState(t)

	if err := NewStdoutLogger("INVALID"); err == nil {
		t.Fatal("expected invalid log level error")
	}
}

func TestSetLevelUsesLoggerLevelPolicy(t *testing.T) {
	preserveLoggerState(t)

	if err := SetLevel("debug"); err != nil {
		t.Fatalf("SetLevel(debug) error = %v", err)
	}
	if logrus.GetLevel() != logrus.DebugLevel {
		t.Fatalf("logrus level = %v, want debug", logrus.GetLevel())
	}
	if err := SetLevel("INVALID"); err == nil {
		t.Fatal("SetLevel(INVALID) error = nil, want invalid level error")
	}
}

func TestCloseWithoutInit(t *testing.T) {
	preserveLoggerState(t)

	if err := Close(); err != nil {
		t.Errorf("Close() on nil logger should not error, got: %v", err)
	}
	if err := Close(); err != nil {
		t.Errorf("second Close() on nil logger should not error, got: %v", err)
	}
}

func TestRotatingFileWriterRotatesAndPrunesBackups(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")

	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{
		MaxSizeMB:  1,
		MaxBackups: 1,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}

	chunk := bytes.Repeat([]byte("x"), bytesPerMegabyte/2)
	for i := 0; i < 6; i++ {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	backups, err := filepath.Glob(filepath.Join(tmpDir, "bot-*.log*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1; backups=%v", len(backups), backups)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("active log missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("active log is empty after rotation")
	}
}

func TestLoggingWrappersEmitStructuredEntries(t *testing.T) {
	preserveLoggerState(t)

	if err := NewStdoutLogger("TRACE"); err != nil {
		t.Fatalf("NewStdoutLogger() error = %v", err)
	}

	var buf bytes.Buffer
	logrus.SetOutput(&buf)

	Trace("trace message")
	Debug("debug message")
	Info("info message")
	Warn("warn message")
	Error("error message")
	WithField("key", "value").Info("with field")
	WithFields(Fields{"a": "1", "b": "2"}).Info("with fields")
	WithError(os.ErrNotExist).Warn("with error")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 8 {
		t.Fatalf("log line count = %d, want 8; output=%q", len(lines), buf.String())
	}

	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("log line is not JSON: %v; line=%q", err, line)
		}
		entries = append(entries, fields)
	}

	wantLevels := []string{"trace", "debug", "info", "warning", "error", "info", "info", "warning"}
	wantMsgs := []string{
		"trace message", "debug message", "info message", "warn message",
		"error message", "with field", "with fields", "with error",
	}
	for i, entry := range entries {
		if entry["level"] != wantLevels[i] {
			t.Errorf("entry %d level = %v, want %q", i, entry["level"], wantLevels[i])
		}
		if entry["msg"] != wantMsgs[i] {
			t.Errorf("entry %d msg = %v, want %q", i, entry["msg"], wantMsgs[i])
		}
	}
	if entries[5]["key"] != "value" {
		t.Errorf("WithField entry key = %v, want value", entries[5]["key"])
	}
	if entries[6]["a"] != "1" || entries[6]["b"] != "2" {
		t.Errorf("WithFields entry fields = %v, want a=1 b=2", entries[6])
	}
	if entries[7]["error"] != os.ErrNotExist.Error() {
		t.Errorf("WithError entry error = %v, want %q", entries[7]["error"], os.ErrNotExist.Error())
	}
}

func TestNewLoggerReplacesPreviousFileWriter(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	cfg := LogRotationConfig{MaxSizeMB: 10, MaxBackups: 3, MaxAgeDays: 7}
	pathA := filepath.Join(tmpDir, "a.log")
	pathB := filepath.Join(tmpDir, "b.log")

	if err := NewLogger("INFO", pathA, cfg); err != nil {
		t.Fatalf("NewLogger(pathA) error = %v", err)
	}
	if err := NewLogger("INFO", pathB, cfg); err != nil {
		t.Fatalf("NewLogger(pathB) error = %v", err)
	}

	// The first writer must be released. Its lock file stays on disk by
	// design (advisory-lock convention), so the proof is that the lock is
	// free and a fresh writer can take pathA again.
	if _, err := os.Stat(pathA + ".lock"); err != nil {
		t.Fatalf("first owner lock file should remain on disk: %v", err)
	}
	assertLockIsFree(t, pathA+".lock")
	reclaimed, err := newRotatingFileWriter(pathA, cfg)
	if err != nil {
		t.Fatalf("a fresh writer could not reclaim pathA after replacement: %v", err)
	}
	if err := reclaimed.Close(); err != nil {
		t.Fatalf("Close(reclaimed) error = %v", err)
	}
	if _, err := os.Stat(pathB + ".lock"); err != nil {
		t.Fatalf("second owner lock missing: %v", err)
	}
}

func TestNewLoggerPropagatesWriterError(t *testing.T) {
	preserveLoggerState(t)

	if err := NewLogger("INFO", "", LogRotationConfig{MaxSizeMB: 1}); err == nil {
		t.Fatal("NewLogger with empty path should fail")
	}
	logFileWriterMu.Lock()
	writer := logFileWriter
	logFileWriterMu.Unlock()
	if writer != nil {
		t.Fatal("logFileWriter should be nil after failed NewLogger")
	}
}

func TestNewStdoutLoggerClosesPreviousFileWriter(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	logPath := filepath.Join(tmpDir, "app.log")
	if err := NewLogger("INFO", logPath, LogRotationConfig{MaxSizeMB: 10}); err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	if err := NewStdoutLogger("INFO"); err != nil {
		t.Fatalf("NewStdoutLogger() error = %v", err)
	}

	logFileWriterMu.Lock()
	writer := logFileWriter
	logFileWriterMu.Unlock()
	if writer != nil {
		t.Fatal("logFileWriter should be nil after NewStdoutLogger")
	}
	if out := logrus.StandardLogger().Out; out != os.Stdout {
		t.Fatalf("logrus output = %v, want os.Stdout after NewStdoutLogger", out)
	}
	// The lock file stays on disk; the owner lock itself must be released.
	if _, err := os.Stat(logPath + ".lock"); err != nil {
		t.Fatalf("owner lock file should remain on disk: %v", err)
	}
	assertLockIsFree(t, logPath+".lock")
}

func TestEnsureServiceHookInstallsOnlyOnce(t *testing.T) {
	preserveLoggerState(t)

	serviceHookInstalled = false
	before := len(logrus.StandardLogger().Hooks[logrus.InfoLevel])
	ensureServiceHook()
	afterFirst := len(logrus.StandardLogger().Hooks[logrus.InfoLevel])
	ensureServiceHook()
	afterSecond := len(logrus.StandardLogger().Hooks[logrus.InfoLevel])

	if afterFirst != before+1 {
		t.Fatalf("first ensureServiceHook added %d hooks, want 1", afterFirst-before)
	}
	if afterSecond != afterFirst {
		t.Fatalf("second ensureServiceHook added %d hooks, want 0", afterSecond-afterFirst)
	}
}

func TestServiceHookKeepsExistingServiceField(t *testing.T) {
	entry := &logrus.Entry{Data: logrus.Fields{"service": "custom"}}
	if err := (serviceHook{}).Fire(entry); err != nil {
		t.Fatalf("Fire() error = %v", err)
	}
	if entry.Data["service"] != "custom" {
		t.Fatalf("service field = %v, want custom (must not be overwritten)", entry.Data["service"])
	}
	if got := (serviceHook{}).Levels(); len(got) != len(logrus.AllLevels) {
		t.Fatalf("Levels() length = %d, want %d", len(got), len(logrus.AllLevels))
	}
}

func TestNewRotatingFileWriterRejectsEmptyPath(t *testing.T) {
	for _, path := range []string{"", "   "} {
		if _, err := newRotatingFileWriter(path, LogRotationConfig{MaxSizeMB: 1}); err == nil {
			t.Fatalf("newRotatingFileWriter(%q) error = nil, want error", path)
		}
	}
}

func TestNewRotatingFileWriterDefaultsMaxSize(t *testing.T) {
	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{MaxSizeMB: 0})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	if writer.maxSizeBytes != bytesPerMegabyte {
		t.Fatalf("maxSizeBytes = %d, want %d", writer.maxSizeBytes, bytesPerMegabyte)
	}
}

func TestNewRotatingFileWriterFailsWhenPathIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	if err := os.Mkdir(logPath, 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	if _, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1}); err == nil {
		t.Fatal("newRotatingFileWriter on a directory should fail")
	}
	// The owner lock acquired before the failed open must be released again.
	// The lock file itself stays on disk, so freeness is what proves it.
	assertLockIsFree(t, logPath+".lock")
}

func TestNewRotatingFileWriterFailsWhenParentIsFile(t *testing.T) {
	tmpDir := t.TempDir()
	parent := filepath.Join(tmpDir, "parent")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	logPath := filepath.Join(parent, "bot.log")
	if _, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1}); err == nil {
		t.Fatal("newRotatingFileWriter below a regular file should fail")
	}
}

func TestNewRotatingFileWriterFailsWhenLockPathIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	if err := os.Mkdir(logPath+".lock", 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	if _, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1}); err == nil {
		t.Fatal("newRotatingFileWriter with a directory at the lock path should fail")
	}
}

// TestRotatingFileWriterWriteFailsWhenReopenBlocked covers the lazy-open path
// of Write when the owner lock is genuinely held by someone else. The rival
// writer is never closed, so its first Write is what triggers openLocked and
// the refusal; the error must be the lock conflict, not the closed sentinel.
func TestRotatingFileWriterWriteFailsWhenReopenBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")

	owner, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter(owner) error = %v", err)
	}
	defer owner.Close()

	rival := &rotatingFileWriter{
		filename:     logPath,
		lockPath:     logPath + ".lock",
		maxSizeBytes: bytesPerMegabyte,
	}
	n, err := rival.Write([]byte("blocked\n"))
	if err == nil {
		_ = rival.Close()
		t.Fatal("Write() should fail while another owner holds the lock")
	}
	if n != 0 {
		t.Fatalf("Write() n = %d, want 0 when the reopen is blocked", n)
	}
	if !strings.Contains(err.Error(), "already owned by another running") {
		t.Fatalf("Write() error = %v, want the live-owner refusal", err)
	}
	if errors.Is(err, errWriterClosed) {
		t.Fatalf("Write() error = %v, want a lock conflict and not the closed sentinel", err)
	}
}

func TestRotatingFileWriterWritePropagatesRotationError(t *testing.T) {
	tmpDir := t.TempDir()
	// "[" makes the backup glob pattern malformed, so cleanupBackups fails
	// during rotation and Write must surface the error.
	logPath := filepath.Join(tmpDir, "bot[.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1, MaxBackups: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	chunk := bytes.Repeat([]byte("x"), bytesPerMegabyte/2)
	var writeErr error
	for i := 0; i < 4; i++ {
		if _, writeErr = writer.Write(chunk); writeErr != nil {
			break
		}
	}
	if writeErr == nil {
		t.Fatal("Write() should fail once rotation hits the malformed backup pattern")
	}
	if !strings.Contains(writeErr.Error(), "list log backups") {
		t.Fatalf("Write() error = %v, want backup listing failure", writeErr)
	}
}

func TestRotateLockedReportsFileCloseError(t *testing.T) {
	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	// Close the handle underneath the writer; rotateLocked's own close then fails.
	if err := writer.file.Close(); err != nil {
		t.Fatalf("direct file Close() error = %v", err)
	}
	if err := writer.rotateLocked(time.Now().UTC()); err == nil {
		t.Fatal("rotateLocked() should report the file close error")
	}
}

func TestRotateLockedSkipsEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	if err := writer.rotateLocked(time.Now().UTC()); err != nil {
		t.Fatalf("rotateLocked() on empty file error = %v", err)
	}

	backups, err := filepath.Glob(filepath.Join(tmpDir, "bot-*.log*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("empty file rotation created backups: %v", backups)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("active log missing after empty rotation: %v", err)
	}
}

func TestRotatingFileWriterCompressedRotation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{
		MaxSizeMB:  1,
		MaxBackups: 3,
		Compress:   true,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	chunk := bytes.Repeat([]byte("x"), bytesPerMegabyte/2)
	var writeErr error
	for i := 0; i < 3; i++ {
		if _, writeErr = writer.Write(chunk); writeErr != nil {
			break
		}
	}

	compressed, err := filepath.Glob(filepath.Join(tmpDir, "bot-*.log.gz"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	uncompressed, err := filepath.Glob(filepath.Join(tmpDir, "bot-*.log"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}

	if writeErr != nil {
		t.Fatalf("Write() error = %v", writeErr)
	}
	if len(compressed) == 0 {
		t.Fatal("expected at least one compressed backup")
	}
	if len(uncompressed) != 0 {
		t.Fatalf("uncompressed backups left behind: %v", uncompressed)
	}
}

func TestCloseReturnsFileCloseError(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}

	if err := writer.file.Close(); err != nil {
		t.Fatalf("direct file Close() error = %v", err)
	}
	if err := writer.Close(); err == nil {
		t.Fatal("Close() should report the double file close error")
	}
	// The owner lock must still be released despite the file error. A nil
	// handle alone does not prove that, so check the lock itself is free.
	if writer.lockFile != nil {
		t.Fatal("lockFile is not nil after Close()")
	}
	assertLockIsFree(t, logPath+".lock")
}

func TestCloseReturnsLockCloseError(t *testing.T) {
	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}

	if err := writer.lockFile.Close(); err != nil {
		t.Fatalf("direct lock Close() error = %v", err)
	}
	// The error now surfaces from filelock.Release on the already-closed
	// handle ("unlock log owner lock ..."), not from the handle's Close.
	err = writer.Close()
	if err == nil {
		t.Fatal("Close() should report the double lock close error")
	}
	if !strings.Contains(err.Error(), "unlock log owner lock") {
		t.Fatalf("Close() error = %v, want it to name the failed unlock", err)
	}
}

func TestNextBackupNameSkipsExistingCandidates(t *testing.T) {
	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	fixed := time.Date(2026, 1, 2, 3, 4, 5, 678901234, time.UTC)
	timestamp := fixed.Format("20060102T150405.000000000Z")

	first := writer.nextBackupName(fixed)
	want := filepath.Join(tmpDir, "bot-"+timestamp+".log")
	if first != want {
		t.Fatalf("nextBackupName() = %q, want %q", first, want)
	}

	if err := os.WriteFile(first, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	second := writer.nextBackupName(fixed)
	wantSecond := filepath.Join(tmpDir, "bot-"+timestamp+".1.log")
	if second != wantSecond {
		t.Fatalf("nextBackupName() with occupied slot = %q, want %q", second, wantSecond)
	}

	// A compressed leftover also blocks a slot.
	if err := os.WriteFile(wantSecond+".gz", nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	third := writer.nextBackupName(fixed)
	wantThird := filepath.Join(tmpDir, "bot-"+timestamp+".2.log")
	if third != wantThird {
		t.Fatalf("nextBackupName() with occupied gz slot = %q, want %q", third, wantThird)
	}
}

func TestNextBackupNameFallsBackWhenAllSlotsTaken(t *testing.T) {
	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	fixed := time.Date(2026, 1, 2, 3, 4, 5, 678901234, time.UTC)
	timestamp := fixed.Format("20060102T150405.000000000Z")

	for i := 0; i < 1000; i++ {
		name := filepath.Join(tmpDir, "bot-"+timestamp+".log")
		if i > 0 {
			name = filepath.Join(tmpDir, fmt.Sprintf("bot-%s.%d.log", timestamp, i))
		}
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}

	got := writer.nextBackupName(fixed)
	base := filepath.Base(got)
	if !strings.HasPrefix(base, "bot-"+timestamp+"-") || !strings.HasSuffix(base, ".log") {
		t.Fatalf("fallback name = %q, want prefix %q and suffix .log", base, "bot-"+timestamp+"-")
	}
	if fileExists(got) {
		t.Fatalf("fallback name %q already exists", got)
	}
}

func TestCleanupBackupsRemovesExpiredAndExcess(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{
		MaxSizeMB:  1,
		MaxBackups: 1,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	expired := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	recentA := filepath.Join(tmpDir, "bot-20260119T000000.000000000Z.log")
	recentB := filepath.Join(tmpDir, "bot-20260119T000000.000000000Z.1.log")
	for _, path := range []string{expired, recentA, recentB} {
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	if err := os.Chtimes(expired, now.AddDate(0, 0, -10), now.AddDate(0, 0, -10)); err != nil {
		t.Fatalf("Chtimes(expired) error = %v", err)
	}
	// Identical modtimes force the sort tiebreak on the path name.
	sameTime := now.AddDate(0, 0, -1)
	for _, path := range []string{recentA, recentB} {
		if err := os.Chtimes(path, sameTime, sameTime); err != nil {
			t.Fatalf("Chtimes(%q) error = %v", path, err)
		}
	}
	// A directory matching the backup pattern must be ignored.
	if err := os.Mkdir(filepath.Join(tmpDir, "bot-dir.log"), 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	if err := writer.cleanupBackups(now); err != nil {
		t.Fatalf("cleanupBackups() error = %v", err)
	}

	if fileExists(expired) {
		t.Fatal("expired backup should have been removed")
	}
	// With equal modtimes the lexically greater path is kept
	// ("...Z.log" sorts after "...Z.1.log").
	if !fileExists(recentA) {
		t.Fatal("lexically greater recent backup should be kept")
	}
	if fileExists(recentB) {
		t.Fatal("excess backup should have been removed")
	}
}

func TestCleanupBackupsPropagatesGlobError(t *testing.T) {
	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot[.log"), LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	err = writer.cleanupBackups(time.Now().UTC())
	if err == nil {
		t.Fatal("cleanupBackups() with malformed pattern should fail")
	}
	if !strings.Contains(err.Error(), "list log backups") {
		t.Fatalf("cleanupBackups() error = %v, want backup listing failure", err)
	}
}

func TestCompressBackupFileErrors(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("missing source", func(t *testing.T) {
		err := CompressBackupFile(filepath.Join(tmpDir, "missing.log"))
		if err == nil || !strings.Contains(err.Error(), "open log backup for compression") {
			t.Fatalf("error = %v, want open failure", err)
		}
	})

	t.Run("stale target is replaced", func(t *testing.T) {
		source := filepath.Join(tmpDir, "stale-target.log")
		if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		// A pre-existing ".gz" can only be a leftover from a crashed
		// compression (nextBackupName never targets an occupied name), so
		// CompressBackupFile must replace it rather than refuse forever.
		if err := os.WriteFile(source+".gz", []byte("stale, truncated"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		err := CompressBackupFile(source)
		if err != nil {
			t.Fatalf("CompressBackupFile() error = %v, want success replacing a stale target", err)
		}
		if fileExists(source) {
			t.Fatal("source must be removed after successful compression")
		}
		if fileExists(source + ".gz.tmp") {
			t.Fatal("compression temp file must not survive")
		}
	})

	t.Run("unreadable source", func(t *testing.T) {
		dirSource := filepath.Join(tmpDir, "dirsource.log")
		if err := os.Mkdir(dirSource, 0o750); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}

		err := CompressBackupFile(dirSource)
		if err == nil {
			t.Fatal("compressing a directory should fail")
		}
		if fileExists(dirSource + ".gz") {
			t.Fatal("partial compressed file must be removed on failure")
		}
		// The temp file is created before the copy, so the copy-error path
		// must sweep it too; without this the ".gz.tmp" leaks on every failed
		// compression.
		if fileExists(dirSource + ".gz.tmp") {
			t.Fatal("compression temp file must be removed on a failed copy")
		}
	})
}

// TestCompressBackupFilePublishAndSweepErrors is split out from
// TestCompressBackupFileErrors to keep gocyclo under the operator's central
// lint profile threshold; same defect-pinning intent (§4.3 (B)).
func TestCompressBackupFilePublishAndSweepErrors(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("stale temp file cannot be swept", func(t *testing.T) {
		source := filepath.Join(tmpDir, "stale-tmp-sweep.log")
		if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		// A non-empty directory at the ".gz.tmp" name makes the leading sweep's
		// os.Remove fail with something other than IsNotExist.
		tempPath := source + ".gz.tmp"
		if err := os.Mkdir(tempPath, 0o750); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(tempPath, "occupant"), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(occupant) error = %v", err)
		}

		err := CompressBackupFile(source)
		if err == nil || !strings.Contains(err.Error(), "remove stale compressed log backup temp file") {
			t.Fatalf("CompressBackupFile() error = %v, want sweep failure", err)
		}
	})

	t.Run("publish rename fails", func(t *testing.T) {
		source := filepath.Join(tmpDir, "rename-fails.log")
		if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		// A directory at the final ".gz" name makes the publish rename fail;
		// nextBackupName never targets an occupied name, so this only happens
		// through direct manipulation like this test's.
		if err := os.Mkdir(source+".gz", 0o750); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}

		err := CompressBackupFile(source)
		if err == nil || !strings.Contains(err.Error(), "publish compressed log backup") {
			t.Fatalf("CompressBackupFile() error = %v, want publish failure", err)
		}
		if fileExists(source + ".gz.tmp") {
			t.Fatal("compression temp file must not survive a failed publish")
		}
	})
}

// The tests below rely on Windows file sharing semantics: files opened by the
// Go standard library are not opened with FILE_SHARE_DELETE, so renaming or
// removing a file that still has an open handle fails deterministically. On
// other platforms these operations succeed, so the error branches are not
// reachable there and the tests skip.

func TestRotateLockedFailsWhenRenameBlocked(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows sharing semantics to block the backup rename")
	}

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	chunk := bytes.Repeat([]byte("x"), bytesPerMegabyte)
	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	blocker, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer blocker.Close()

	_, writeErr := writer.Write(chunk)
	if writeErr == nil {
		t.Fatal("Write() should fail while an open handle blocks the rename")
	}
	if !strings.Contains(writeErr.Error(), "rotate log file") {
		t.Fatalf("Write() error = %v, want rotate failure", writeErr)
	}
}

func TestRotateLockedReportsStatError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires an invalid Windows filename to break os.Stat")
	}

	// "?" is invalid in Windows filenames; os.Stat fails with a syntax error
	// that is not IsNotExist, which rotateLocked must surface.
	tmpDir := t.TempDir()
	writer := &rotatingFileWriter{
		filename:     filepath.Join(tmpDir, "bo?t.log"),
		lockPath:     filepath.Join(tmpDir, "bo?t.log.lock"),
		maxSizeBytes: bytesPerMegabyte,
	}

	err := writer.rotateLocked(time.Now().UTC())
	if err == nil {
		t.Fatal("rotateLocked() should report the stat error")
	}
	if !strings.Contains(err.Error(), "stat log file before rotation") {
		t.Fatalf("rotateLocked() error = %v, want stat failure", err)
	}
}

func TestCleanupBackupsReportsExpiredRemoveError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows sharing semantics to block the removal")
	}

	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{
		MaxSizeMB:  1,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	expired := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	if err := os.WriteFile(expired, []byte("backup"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chtimes(expired, now.AddDate(0, 0, -10), now.AddDate(0, 0, -10)); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	blocker, err := os.Open(expired)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer blocker.Close()

	err = writer.cleanupBackups(now)
	if err == nil {
		t.Fatal("cleanupBackups() should report the blocked removal")
	}
	if !strings.Contains(err.Error(), "remove expired log backup") {
		t.Fatalf("cleanupBackups() error = %v, want expired removal failure", err)
	}
}

func TestCleanupBackupsReportsExcessRemoveError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows sharing semantics to block the removal")
	}

	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{
		MaxSizeMB:  1,
		MaxBackups: 1,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	newer := filepath.Join(tmpDir, "bot-20260119T000000.000000000Z.log")
	older := filepath.Join(tmpDir, "bot-20260118T000000.000000000Z.log")
	for path, age := range map[string]int{newer: -1, older: -2} {
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		when := now.AddDate(0, 0, age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("Chtimes() error = %v", err)
		}
	}

	// The older backup exceeds MaxBackups and would be removed; block it.
	blocker, err := os.Open(older)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer blocker.Close()

	err = writer.cleanupBackups(now)
	if err == nil {
		t.Fatal("cleanupBackups() should report the blocked removal")
	}
	if !strings.Contains(err.Error(), "remove old log backup") {
		t.Fatalf("cleanupBackups() error = %v, want old backup removal failure", err)
	}
}

func TestListBackupsSkipsGhostDirectoryEntries(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows name normalization to produce a ghost entry")
	}

	tmpDir := t.TempDir()
	writer, err := newRotatingFileWriter(filepath.Join(tmpDir, "bot.log"), LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	// A trailing-dot name (created via the \\?\ namespace) shows up in the
	// directory listing but os.Stat on the normalized name reports NotExist,
	// so listBackups must skip it.
	ghost := `\\?\` + filepath.Join(tmpDir, "bot-ghost.log.")
	if err := os.WriteFile(ghost, []byte("ghost"), 0o600); err != nil {
		t.Fatalf("WriteFile(ghost) error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(ghost); err != nil {
			t.Errorf("Remove(ghost) error = %v", err)
		}
	})

	regular := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	if err := os.WriteFile(regular, []byte("backup"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	backups, err := writer.listBackups()
	if err != nil {
		t.Fatalf("listBackups() error = %v", err)
	}
	if len(backups) != 1 || backups[0].paths[0] != regular {
		t.Fatalf("listBackups() = %v, want only %q", backups, regular)
	}
}

func TestRotatingFileWriterRejectsSecondOwnerForSamePath(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	cfg := LogRotationConfig{MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 7}

	first, err := newRotatingFileWriter(logPath, cfg)
	if err != nil {
		t.Fatalf("first newRotatingFileWriter() error = %v", err)
	}
	defer first.Close()

	if second, err := newRotatingFileWriter(logPath, cfg); err == nil {
		_ = second.Close()
		t.Fatal("second newRotatingFileWriter() succeeded while first owner was active")
	} else if !strings.Contains(err.Error(), "already owned by another running") {
		t.Fatalf("refusal error = %v, want it to name another running process", err)
	}

	// The refused writer must not have rewritten the live owner's marker: the
	// lock is acquired before the marker is truncated, never the other way
	// round. Reading through the owner's own handle is what makes this
	// portable — Windows refuses reads of a byte range locked by a different
	// handle.
	ownerMarker := make([]byte, 256)
	read, err := first.lockFile.ReadAt(ownerMarker, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read owner marker through the owner handle: %v", err)
	}
	wantPid := fmt.Sprintf("pid=%d\n", os.Getpid())
	if !strings.Contains(string(ownerMarker[:read]), wantPid) {
		t.Fatalf("owner marker = %q after the refusal, want it to still contain %q", string(ownerMarker[:read]), wantPid)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	// Releasing the owner lock leaves the lock file on disk by design.
	if _, err := os.Stat(logPath + ".lock"); err != nil {
		t.Fatalf("lock file should remain on disk after release: %v", err)
	}

	second, err := newRotatingFileWriter(logPath, cfg)
	if err != nil {
		t.Fatalf("newRotatingFileWriter() after owner close error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestExportedRotationHelpersMatchWriterMethods(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{
		MaxSizeMB:  1,
		MaxBackups: 3,
		MaxAgeDays: 7,
	})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	now := time.Date(2026, 1, 2, 3, 4, 5, 678901234, time.UTC)
	if got, want := NextBackupName(writer.filename, now), writer.nextBackupName(now); got != want {
		t.Fatalf("NextBackupName() = %q, want %q (writer.nextBackupName())", got, want)
	}

	backup := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	if err := os.WriteFile(backup, []byte("backup"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	old := now.AddDate(0, 0, -1)
	if err := os.Chtimes(backup, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	if err := CleanupBackups(writer.filename, writer.maxBackups, writer.maxAgeDays, now); err != nil {
		t.Fatalf("CleanupBackups() error = %v", err)
	}
	if err := writer.cleanupBackups(now); err != nil {
		t.Fatalf("writer.cleanupBackups() error = %v", err)
	}
	// Both calls must agree: the fixture used above is under maxAgeDays and
	// under maxBackups, so it must survive both.
	if !fileExists(backup) {
		t.Fatal("backup unexpectedly removed by CleanupBackups()/cleanupBackups()")
	}
}

func TestListBackupsGroupsCompressedAndUncompressedForms(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	stem := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	forms := []string{stem, stem + ".gz", stem + ".gz.tmp"}
	for _, path := range forms {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	newest := time.Now().Add(time.Hour)
	if err := os.Chtimes(stem+".gz.tmp", newest, newest); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	backups, err := writer.listBackups()
	if err != nil {
		t.Fatalf("listBackups() error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("listBackups() returned %d groups, want 1: %+v", len(backups), backups)
	}
	if len(backups[0].paths) != 3 {
		t.Fatalf("group paths = %v, want all 3 on-disk forms", backups[0].paths)
	}
	for _, form := range forms {
		found := false
		for _, p := range backups[0].paths {
			if p == form {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("group paths %v missing form %q", backups[0].paths, form)
		}
	}
	if !backups[0].modTime.Equal(newest) {
		t.Fatalf("group modTime = %v, want the newest member's mtime %v", backups[0].modTime, newest)
	}
}

func TestCompressBackupFilePublishesViaTempAndLeavesNoPartialGz(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	content := []byte("line one\nline two\n")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := CompressBackupFile(source); err != nil {
		t.Fatalf("CompressBackupFile() error = %v", err)
	}
	if fileExists(source) {
		t.Fatal("uncompressed source should be removed after successful compression")
	}
	if fileExists(source + ".gz.tmp") {
		t.Fatal("no .gz.tmp should survive a successful compression")
	}
	if !fileExists(source + ".gz") {
		t.Fatal("compressed backup missing")
	}

	gzFile, err := os.Open(source + ".gz")
	if err != nil {
		t.Fatalf("Open(.gz) error = %v", err)
	}
	defer gzFile.Close()
	reader, err := gzip.NewReader(gzFile)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll(gzip) error = %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("decompressed content = %q, want %q", got, content)
	}
}

// TestCompressBackupFileKeepsPublishedGzWhenSourceRemoveFails pins the second
// half of the 2026-09-04 L372 fix: when the final os.Remove(path) fails, the
// already-published .gz must survive intact, not be deleted by the error
// path. Restoring "_ = os.Remove(compressedPath)" on this branch, or
// swallowing the remove error entirely, both pass every other test in the
// package -- this is the only regression coverage for that half.
func TestCompressBackupFileKeepsPublishedGzWhenSourceRemoveFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows sharing semantics to block the source removal")
	}
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	content := []byte("line one\nline two\n")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// An open handle on the source blocks os.Remove(path) with Windows
	// sharing semantics, the same technique the neighbouring Windows-only
	// tests in this file use, without touching the compressed target.
	blocker, err := os.Open(source)
	if err != nil {
		t.Fatalf("Open(blocker) error = %v", err)
	}
	defer blocker.Close()

	err = CompressBackupFile(source)
	if err == nil || !strings.Contains(err.Error(), "remove uncompressed log backup") {
		t.Fatalf("CompressBackupFile() error = %v, want remove-uncompressed-backup failure", err)
	}
	if !fileExists(source + ".gz") {
		t.Fatal("published .gz must survive a failed source removal")
	}
	if !fileExists(source) {
		t.Fatal("blocked source should still be present")
	}

	gzFile, err := os.Open(source + ".gz")
	if err != nil {
		t.Fatalf("Open(.gz) error = %v", err)
	}
	defer gzFile.Close()
	reader, err := gzip.NewReader(gzFile)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll(gzip) error = %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("decompressed content = %q, want %q", got, content)
	}
}
