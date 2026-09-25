package runadmission

import (
	"errors"
	"sort"
	"syscall"
	"unsafe"
)

// readDirAt returns the names of regular child files beneath a directory
// descriptor. The result is sorted so iteration order is deterministic. It is
// used only to enumerate the durable admission-binding records, never to resolve
// untrusted caller-supplied paths.
//
// A fresh directory descriptor is opened for every call: getdents advances the
// descriptor's read offset, so reading the shared descriptor directly would make
// a second enumeration return nothing.
func readDirAt(parent int) ([]string, error) {
	if parent < 0 {
		return nil, errors.New("invalid directory descriptor")
	}
	fd, err := syscall.Openat(parent, ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	const bufferBytes = 64 << 10
	buffer := make([]byte, bufferBytes)
	var names []string
	for {
		n, err := syscall.ReadDirent(fd, buffer)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		for offset := 0; offset < n; {
			reclen, ok := direntReclen(buffer[offset:n])
			if !ok {
				return nil, errors.New("malformed directory entry")
			}
			if offset+reclen > n {
				return nil, errors.New("directory entry overruns read buffer")
			}
			entry := buffer[offset : offset+reclen]
			name, ok := direntName(entry)
			if ok {
				names = append(names, name)
			}
			offset += reclen
		}
	}
	sort.Strings(names)
	return names, nil
}

// direntReclen reads the d_reclen field (native endian, offset 16 on Linux) for
// the directory entry at the start of buf.
func direntReclen(buf []byte) (int, bool) {
	if len(buf) < 20 {
		return 0, false
	}
	// d_reclen is an unsigned 16-bit field at offset 16 in struct linux_dirent64.
	reclen := int(*(*uint16)(unsafe.Pointer(&buf[16])))
	if reclen < 20 || reclen > len(buf) {
		return 0, false
	}
	return reclen, true
}

// direntName extracts the NUL-terminated d_name from a linux_dirent64 entry and
// returns it, skipping the "." and ".." entries.
func direntName(entry []byte) (string, bool) {
	// d_name starts at offset 19 in struct linux_dirent64.
	start := 19
	end := -1
	for index := start; index < len(entry); index++ {
		if entry[index] == 0 {
			end = index
			break
		}
	}
	if end <= start {
		return "", false
	}
	name := string(entry[start:end])
	if name == "." || name == ".." {
		return "", false
	}
	return name, true
}
