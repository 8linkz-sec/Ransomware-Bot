// Package filelock provides the exclusive, non-blocking advisory file lock used
// by the data_dir lock and the log owner lock. The operating system releases the
// lock when the holding process dies, so a lock file left behind by a SIGKILL,
// an OOM kill or a power loss is never stale.
package filelock
