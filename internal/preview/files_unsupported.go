//go:build !linux

package preview

import "os"

func openDirectory(string) (*os.File, error)          { return nil, ErrUnavailable }
func privateDirectory(string, bool) (*os.File, error) { return nil, ErrUnavailable }
func openRegular(string, int, bool) (*os.File, error) { return nil, ErrUnavailable }
func readFile(string, int64, bool) ([]byte, error)    { return nil, ErrUnavailable }
func lockFile(*os.File) error                         { return ErrUnavailable }
