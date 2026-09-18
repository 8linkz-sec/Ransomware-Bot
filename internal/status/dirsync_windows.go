//go:build windows

package status

// syncDir is a no-op on Windows. There is no directory-fsync equivalent:
// opening a directory succeeds, but Sync on that handle fails with
// ERROR_ACCESS_DENIED (verified 2026-09-03), so calling it could only ever
// produce a spurious warning. NTFS journals the rename's metadata itself, and
// Windows is a development host only.
func syncDir(string) error { return nil }
