//go:build !linux

package serviceapi

func readProtectedFile(string, int) ([]byte, error) { return nil, ErrUnsafeConfig }
