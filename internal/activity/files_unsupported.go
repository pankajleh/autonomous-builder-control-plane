//go:build !linux

package activity

import "os"

func openDirectory(string) (*os.File, error)          { return nil, ErrUnavailable }
func privateDirectory(string, bool) (*os.File, error) { return nil, ErrUnavailable }
func openRegular(string, int, bool) (*os.File, error) { return nil, ErrUnavailable }
func readFile(string, int64, bool) ([]byte, error)    { return nil, ErrUnavailable }
func lockFile(*os.File) error                         { return ErrUnavailable }
func physicalID(os.FileInfo) string                   { return "" }
func generationFileID(*os.File) string                { return "" }

func duplicateFile(*os.File) (*os.File, error) { return nil, ErrUnavailable }
