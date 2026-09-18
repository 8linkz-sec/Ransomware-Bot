//go:build !windows

package logger

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestCompressBackupFileTreatsAnAlreadyRemovedSourceAsSuccess pins the
// os.IsNotExist tolerance on the final os.Remove(path): internal/status's
// delivery-audit rotator compresses in a fire-and-forget goroutine while its
// own CleanupBackups runs synchronously (audit_rotation.go), so a prune can
// delete the bare backup between this function's publish rename and its own
// removal of the source. The source already being gone is the outcome the
// function wants, not a WARN-worthy failure.
//
// The window is opened deterministically instead of by racing goroutines: the
// source is a FIFO, so CompressBackupFile's own os.Open blocks until this test
// opens the write end, and its io.Copy blocks until this test closes it. The
// path is unlinked in between, while the open descriptors keep the pipe (and
// therefore the copy) alive. Unix-only because Windows has no FIFO and refuses
// to unlink a file with an open handle at all.
func TestCompressBackupFileTreatsAnAlreadyRemovedSourceAsSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "bot-20260101T000000.000000000Z.log")
	if err := syscall.Mkfifo(source, 0o600); err != nil {
		t.Skipf("mkfifo(%q) unsupported on this filesystem: %v", source, err)
	}

	done := make(chan error, 1)
	go func() { done <- CompressBackupFile(source) }()

	// O_NONBLOCK so a writer that wins the race against the goroutine's
	// blocking open fails fast with ENXIO instead of deadlocking the test.
	var writer *os.File
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		writer, err = os.OpenFile(source, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("OpenFile(fifo write end) never succeeded: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	defer writer.Close()

	content := []byte("line one\nline two\n")
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("Write(fifo) error = %v", err)
	}
	// The compressor is still blocked in io.Copy here, so this unlink is
	// ordered strictly before its own os.Remove(path) without any sleeping.
	if err := os.Remove(source); err != nil {
		t.Fatalf("Remove(source) error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(fifo write end) error = %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CompressBackupFile() error = %v, want success when the source is already gone", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("CompressBackupFile() did not return")
	}

	if fileExists(source + ".gz.tmp") {
		t.Fatal("no .gz.tmp should survive a successful compression")
	}
	gzFile, err := os.Open(source + ".gz")
	if err != nil {
		t.Fatalf("Open(.gz) error = %v, want the published archive to survive", err)
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
