//go:build !linux

package run

// CloseInheritedFilesOnExec does nothing where builds are never walled.
func CloseInheritedFilesOnExec() error { return nil }
