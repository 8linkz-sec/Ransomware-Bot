package logger

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteAppendsRecordWhenRotationRenameFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows sharing semantics to block the backup rename")
	}

	logPath := filepath.Join(t.TempDir(), "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write(bytes.Repeat([]byte("x"), bytesPerMegabyte)); err != nil {
		t.Fatalf("fill log: %v", err)
	}
	ownerLock := writer.lockFile

	blocker, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Close() })

	record := []byte("record after failed rotation\n")
	n, writeErr := writer.Write(record)
	if writeErr == nil || !strings.Contains(writeErr.Error(), "rotate log file") {
		t.Fatalf("Write() error = %v, want rotation error", writeErr)
	}
	if n != len(record) {
		t.Fatalf("Write() byte count = %d, want %d", n, len(record))
	}
	if writer.lockFile != ownerLock {
		t.Fatal("failed rotation replaced or released the owner lock")
	}
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(contents, record) {
		t.Fatalf("active log does not end with the attempted record")
	}
	if err := blocker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("next record\n")); err != nil {
		t.Fatalf("Write() after blocker release: %v", err)
	}
}
