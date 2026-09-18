package logger

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filelock"
	"github.com/sirupsen/logrus"
)

const lockHelperLogPathEnv = "RANSOMWARE_BOT_LOCK_HELPER_LOG_PATH"

// assertLockIsFree fails the test when lockPath is still held by an advisory
// lock. A second flock on a separate open file description conflicts on Unix,
// and LockFileEx conflicts across handles inside one process on Windows, so
// this discriminates on both platforms.
func assertLockIsFree(t *testing.T, lockPath string) {
	t.Helper()

	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open lock file %q: %v", lockPath, err)
	}
	defer file.Close()

	if err := filelock.Acquire(file); err != nil {
		t.Fatalf("lock %q is still held: %v", lockPath, err)
	}
	_ = filelock.Release(file)
}

// TestLogOwnerLockRecoversOrphanLockFile pins the crash-recovery behaviour: a
// lock file left behind by a process that died without releasing it carries no
// kernel lock, so the next start takes it over instead of refusing forever.
func TestLogOwnerLockRecoversOrphanLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	logPath := filepath.Join(tmpDir, "bot.log")
	lockPath := logPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("pid=99999\nhost=dead\n"), 0o600); err != nil {
		t.Fatalf("seed orphan lock file: %v", err)
	}

	if err := NewLogger("INFO", logPath, LogRotationConfig{MaxSizeMB: 1}); err != nil {
		t.Fatalf("NewLogger() with an orphan lock file error = %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file was not created after recovering the orphan lock: %v", err)
	}

	// The marker is only readable once the lock is released: on Windows
	// LockFileEx locks byte range [0,1), so reading a held lock file from a
	// second handle fails.
	if err := Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file after close: %v", err)
	}
	marker := string(content)
	if strings.Contains(marker, "pid=99999") {
		t.Fatalf("orphan marker was not overwritten: %q", marker)
	}
	wantPid := fmt.Sprintf("pid=%d\n", os.Getpid())
	if !strings.Contains(marker, wantPid) {
		t.Fatalf("lock marker = %q, want it to contain %q", marker, wantPid)
	}
	for _, field := range []string{"host=", "created_at="} {
		if !strings.Contains(marker, field) {
			t.Fatalf("lock marker = %q, want it to contain %q", marker, field)
		}
	}
}

// TestLogOwnerLockSurvivesOwnerAndRecoversAfterKill proves the release is done
// by the kernel and not by user-space cleanup: the child is killed with
// TerminateProcess (Windows) / SIGKILL (Unix), which runs no deferred code.
func TestLogOwnerLockSurvivesOwnerAndRecoversAfterKill(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	lockPath := logPath + ".lock"

	cmd := exec.Command(os.Args[0], "-test.run=TestLogOwnerLockHelperProcess", "-test.v") //nolint:gosec // G204: re-execs this test binary with a fixed -test.run flag, not external input
	cmd.Env = append(os.Environ(), lockHelperLogPathEnv+"="+logPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	childPid := cmd.Process.Pid

	if err := waitForHelperLock(stdout); err != nil {
		t.Fatalf("helper process did not report the lock: %v", err)
	}

	// While the child lives, this process must be refused.
	if writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1}); err == nil {
		_ = writer.Close()
		t.Fatal("newRotatingFileWriter() succeeded while the child process owned the lock")
	} else if !strings.Contains(err.Error(), "already owned by another running") {
		t.Fatalf("refusal error = %v, want it to mention another running process", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper process: %v", err)
	}
	_ = cmd.Wait()

	// The lock file itself is not cleaned up by the kill; only the kernel lock is.
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file missing after the owner was killed: %v", err)
	}

	// The refused attempt above must not have touched the live owner's marker.
	// The lock has to be acquired BEFORE the marker is rewritten; with the two
	// steps in the other order a refused rival truncates the owner's marker
	// (and on Unix, where flock does not block writes, replaces it with its
	// own pid). This is only readable now that the child is gone, because
	// Windows refuses to read a byte range another handle has locked.
	marker, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file after the owner was killed: %v", err)
	}
	wantChildPid := fmt.Sprintf("pid=%d\n", childPid)
	if !strings.Contains(string(marker), wantChildPid) {
		t.Fatalf(
			"lock marker = %q, want the killed owner's %q (a refused rival must not rewrite it)",
			string(marker),
			wantChildPid,
		)
	}

	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for {
		writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
		if err == nil {
			if err := writer.Close(); err != nil {
				t.Fatalf("Close() after recovery error = %v", err)
			}
			return
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("lock was not released by the kernel after the owner was killed: %v", lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForHelperLock(stdout interface{ Read([]byte) (int, error) }) error {
	buf := make([]byte, 512)
	seen := ""
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		n, err := stdout.Read(buf)
		if n > 0 {
			seen += string(buf[:n])
			if strings.Contains(seen, "LOCKED") {
				return nil
			}
		}
		if err != nil {
			return fmt.Errorf("read helper output: %w (output so far: %q)", err, seen)
		}
	}
	return fmt.Errorf("timed out waiting for the helper process (output so far: %q)", seen)
}

// TestLogOwnerLockHelperProcess is the child body of the subprocess kill test.
func TestLogOwnerLockHelperProcess(t *testing.T) {
	logPath := os.Getenv(lockHelperLogPathEnv)
	if logPath == "" {
		t.Skip("helper process only")
	}

	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		fmt.Printf("HELPER-ERROR %v\n", err)
		os.Exit(1)
	}
	if _, err := writer.Write([]byte("child owns the log\n")); err != nil {
		fmt.Printf("HELPER-ERROR %v\n", err)
		os.Exit(1)
	}
	fmt.Println("LOCKED")
	_ = os.Stdout.Sync()
	select {}
}

// TestRotatingFileWriterWriteAfterCloseIsRefused replaces
// TestRotatingFileWriterWriteReopensAfterClose: a closed writer must never
// reopen the log file, because that resurrects the owner lock of a process
// that is shutting down.
func TestRotatingFileWriterWriteAfterCloseIsRefused(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	lockPath := logPath + ".lock"

	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	if _, err := writer.Write([]byte("before close\n")); err != nil {
		t.Fatalf("Write() before Close() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	sizeBefore := fileSize(t, logPath)

	n, err := writer.Write([]byte("after close\n"))
	if n != 0 {
		t.Fatalf("Write() after Close() n = %d, want 0", n)
	}
	if !errors.Is(err, errWriterClosed) {
		t.Fatalf("Write() after Close() error = %v, want errWriterClosed", err)
	}
	if writer.lockFile != nil {
		t.Fatal("lockFile is not nil after Close()")
	}
	if got := fileSize(t, logPath); got != sizeBefore {
		t.Fatalf("log file grew after a write to a closed writer: %d -> %d", sizeBefore, got)
	}
	assertLockIsFree(t, lockPath)
}

// TestNewLoggerFailureAfterSuccessResetsOutputToStdout pins that a failed
// re-initialisation stops writing to the log file it just closed.
func TestNewLoggerFailureAfterSuccessResetsOutputToStdout(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	logPath := filepath.Join(tmpDir, "a.log")
	lockPath := logPath + ".lock"
	if err := NewLogger("INFO", logPath, LogRotationConfig{MaxSizeMB: 1}); err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	if err := NewLogger("INFO", "   ", LogRotationConfig{MaxSizeMB: 1}); err == nil {
		t.Fatal("NewLogger() with a blank path should fail")
	}

	if out := logrus.StandardLogger().Out; out != os.Stdout {
		t.Fatalf("logrus output = %v, want os.Stdout after a failed NewLogger", out)
	}
	logFileWriterMu.Lock()
	writer := logFileWriter
	logFileWriterMu.Unlock()
	if writer != nil {
		t.Fatal("logFileWriter should be nil after a failed NewLogger")
	}

	sizeBefore := fileSize(t, logPath)
	logrus.Info("line after failed re-init")
	if got := fileSize(t, logPath); got != sizeBefore {
		t.Fatalf("record after a failed NewLogger reached the log file: %d -> %d", sizeBefore, got)
	}
	assertLockIsFree(t, lockPath)
}

// TestNewStdoutLoggerResetsOutputBeforeClosingWriter pins the ordering inside
// resetOutputAndCloseCurrentLocked: logrus is writing somewhere else before the
// file writer is closed.
func TestNewStdoutLoggerResetsOutputBeforeClosingWriter(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	logPath := filepath.Join(tmpDir, "bot.log")
	if err := NewLogger("INFO", logPath, LogRotationConfig{MaxSizeMB: 1}); err != nil {
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
		t.Fatalf("logrus output = %v, want os.Stdout", out)
	}

	sizeBefore := fileSize(t, logPath)
	logrus.Info("line after switching to stdout")
	if got := fileSize(t, logPath); got != sizeBefore {
		t.Fatalf("record after NewStdoutLogger reached the log file: %d -> %d", sizeBefore, got)
	}
}

// TestResetOutputBeforeCloseSurvivesConcurrentLogging pins the ORDER inside
// resetOutputAndCloseCurrentLocked, which the state assertions above cannot:
// they only see the final state, which is the same either way. While other
// goroutines log continuously, switching the logger must never leave logrus
// pointed at an already-closed writer, because logrus reports such a failed
// write on stderr ("Failed to write to log, log writer is closed") and the
// record is lost from the file stream. The close and the SetOutput do not
// share a mutex, so closing first is observable from a logging goroutine.
//
// The test can only under-detect, never fail spuriously: nothing else in this
// package writes that string to stderr.
func TestResetOutputBeforeCloseSurvivesConcurrentLogging(t *testing.T) {
	tmpDir := t.TempDir()
	preserveLoggerState(t)

	// Park stdout in a file: the writers below are deliberately noisy.
	quiet, err := os.OpenFile(filepath.Join(tmpDir, "stdout.txt"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open stdout sink: %v", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = quiet.Close()
		t.Fatalf("os.Pipe() error = %v", err)
	}

	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = quiet, writer

	var stderrText strings.Builder
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		captured, _ := io.ReadAll(reader)
		stderrText.Write(captured)
	}()

	restore := func() {
		os.Stdout, os.Stderr = origStdout, origStderr
		_ = writer.Close()
		<-drained
		_ = quiet.Close()
		_ = reader.Close()
	}

	if err := NewLogger("INFO", filepath.Join(tmpDir, "bot.log"), LogRotationConfig{MaxSizeMB: 64}); err != nil {
		restore()
		t.Fatalf("NewLogger() error = %v", err)
	}

	stop := make(chan struct{})
	var writers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					logrus.Info("concurrent record")
				}
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	switchErr := NewStdoutLogger("INFO")
	time.Sleep(100 * time.Millisecond)
	close(stop)
	writers.Wait()
	restore()

	if switchErr != nil {
		t.Fatalf("NewStdoutLogger() error = %v", switchErr)
	}
	if strings.Contains(stderrText.String(), "Failed to write to log") {
		t.Fatalf(
			"logrus wrote to the file writer after it was closed; the output must be reset first:\n%s",
			stderrText.String(),
		)
	}
}

// TestLogOwnerLockFileModeIsPrivate pins the 0o600 permission bits on the lock
// file, matching the log file itself.
func TestLogOwnerLockFileModeIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not report POSIX permission bits for the lock file")
	}

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	info, err := os.Stat(logPath + ".lock")
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("lock file mode = %o, want 600", mode)
	}
}

// TestRotationKeepsOwnerLock guards the invariant that rotation never touches
// the sidecar lock: the same handle stays held across rotations and no backup
// path can match the lock file.
func TestRotationKeepsOwnerLock(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")
	lockPath := logPath + ".lock"

	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1, MaxBackups: 5})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	lockHandle := writer.lockFile
	if lockHandle == nil {
		t.Fatal("owner lock handle is nil after construction")
	}

	for i := 0; i < 2; i++ {
		writer.mu.Lock()
		err := writer.rotateLocked(time.Now().UTC().Add(time.Duration(i) * time.Second))
		writer.mu.Unlock()
		if err != nil {
			t.Fatalf("rotateLocked() #%d error = %v", i, err)
		}
		if _, err := writer.Write([]byte("after rotation\n")); err != nil {
			t.Fatalf("Write() after rotation #%d error = %v", i, err)
		}
		if writer.lockFile != lockHandle {
			t.Fatalf("owner lock handle changed during rotation #%d", i)
		}
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("lock file missing after rotation #%d: %v", i, err)
		}
	}

	matches, err := filepath.Glob(writer.backupPattern())
	if err != nil {
		t.Fatalf("Glob(backupPattern) error = %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no backups were produced, the rotation guard would be vacuous")
	}
	for _, match := range matches {
		if strings.HasSuffix(match, ".lock") {
			t.Fatalf("backup pattern matched the owner lock file: %q", match)
		}
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info.Size()
}
