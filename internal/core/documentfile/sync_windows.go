package documentfile

import "os"

// Windows FlushFileBuffers accepts the flushed regular file handle; a directory
// handle opened for ordinary reads does not support Sync. NTFS publishes the
// hard link atomically. Linux Compose additionally fsyncs the containing dir.
func syncDirectory(root *os.Root) error { return nil }
