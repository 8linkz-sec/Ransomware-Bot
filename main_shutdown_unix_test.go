//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// configureShutdownProcAttr needs no special process attributes on Unix.
func configureShutdownProcAttr(*exec.Cmd) {}

// sendShutdownSignal delivers SIGTERM, which waitForShutdownSignal listens for.
func sendShutdownSignal(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}

// blockReadinessMarkerDirectory is the Unix half of
// TestRuntimeExitsWhenReadinessMarkerCannotBeWrittenSubprocess
// (main_readiness_marker_test.go; main_shutdown_windows_test.go carries the
// other half). The Windows trick -- a regular file where the marker's parent
// directory needs to be -- does not reproduce the same failure here: Go's
// os.IsNotExist only forgives ENOENT (see syscall.Errno.Is in the Go
// toolchain), while a POSIX unlink or mkdir through a non-directory path
// component fails with ENOTDIR, so the earlier stale-marker removal in
// runConfiguredScheduler would hit a real error there instead of no-opping,
// and the process would exit 1 for a different reason ("Failed to clear
// stale readiness marker") before ever reaching writeReadinessMarker.
//
// Instead this strips write permission from the marker's parent directory
// after creating it: the stale-marker removal's os.Remove of the
// not-yet-existing marker is a genuine ENOENT (looking up the missing name
// only needs the directory's search permission, which read+execute still
// grants) and no-ops exactly like the Windows case; MkdirAll is a no-op
// because the directory already exists; and the temp-file create inside
// writeProgressMarkerFile then fails with permission denied, so
// writeReadinessMarker fails and logs the same "Failed to write readiness
// marker" text the Windows path produces. This relies on the process not
// running as root -- true for a GitHub-hosted ubuntu-latest runner and for a
// normal developer account, both of which enforce directory write
// permission; it would not reproduce inside a container running as root.
func blockReadinessMarkerDirectory(t *testing.T, tmpDir string) string {
	t.Helper()
	if os.Geteuid() == 0 {
		// Root bypasses directory write permission entirely, so the chmod
		// 0500 obstruction below never blocks the temp-file create inside
		// writeProgressMarkerFile: writeReadinessMarker succeeds, the
		// subprocess this test starts never exits, and the test hangs until
		// the suite's own timeout kills it. True on a default (root) Docker
		// container user; false on a GitHub-hosted ubuntu-latest runner and
		// on a normal developer account, where this test still runs.
		t.Skip("root bypasses directory write permission")
	}
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.MkdirAll(blocker, 0700); err != nil {
		t.Fatalf("MkdirAll(blocker) error = %v", err)
	}
	// gosec G302 flags any chmod above 0600, but this is a DIRECTORY, where the
	// bits mean list/create/traverse rather than read/write/execute. 0500 is
	// exactly the point of the fixture: entering the directory must still work
	// (the x bit) while creating the readiness marker inside it must fail (no w
	// bit). A directory at 0600 could not be entered at all, so the "fix" the
	// rule asks for would break the test rather than harden anything.
	//nolint:gosec // 0500 on a directory removes write while keeping traversal
	if err := os.Chmod(blocker, 0500); err != nil {
		t.Fatalf("Chmod(blocker) error = %v", err)
	}
	t.Cleanup(func() {
		// Restore the owner's write bit so t.TempDir's cleanup can remove the
		// directory; 0700 is the ordinary mode of a private directory.
		//nolint:gosec // directory mode, see the 0500 note above
		_ = os.Chmod(blocker, 0700)
	})
	return filepath.Join(blocker, "ransomware-bot.ready")
}
