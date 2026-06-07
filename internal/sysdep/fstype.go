package sysdep

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// FSInfo describes the filesystem a path resides on. It is the input to doctor's
// flock-reliability warning: POSIX advisory locks (flock/fcntl) are unreliable on
// network mounts and on iCloud Drive, so doctor warns when the proxy.lock state
// dir or the working dir land on such a filesystem.
type FSInfo struct {
	// Name is the statfs f_fstypename, e.g. "apfs", "smbfs", "afpfs", "nfs",
	// "webdav". On modern macOS iCloud Drive reports "apfs" (it is FileProvider over
	// APFS, not a distinct mount), so the iCloud case is detected by path, not Name.
	Name string
	// Local is the MNT_LOCAL mount flag: false means a network/remote filesystem
	// (the robust, future-proof "is this remote?" signal — Name strings are not a
	// stable contract per Apple).
	Local bool
}

// FilesystemTyper reports the filesystem type of a path. The seam exists because
// the real implementation calls statfs(2) directly; tests wire the fake in
// sysdeptest. doctor (AC-0031) is the consumer.
type FilesystemTyper interface {
	// FSType returns the filesystem info for path. A non-existent path yields an
	// error satisfying errors.Is(err, fs.ErrNotExist) (statfs returns ENOENT, which
	// Go maps onto fs.ErrNotExist), so callers can ascend to an existing ancestor.
	FSType(path string) (FSInfo, error)
}

// OSFilesystemTyper is the production FilesystemTyper backed by statfs(2).
type OSFilesystemTyper struct{}

var _ FilesystemTyper = (*OSFilesystemTyper)(nil)

func (OSFilesystemTyper) FSType(path string) (FSInfo, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		// unix.Statfs returns a syscall.Errno; ENOENT already satisfies
		// errors.Is(err, fs.ErrNotExist), so callers can distinguish "absent".
		return FSInfo{}, err
	}
	return FSInfo{Name: fstypeName(st.Fstypename[:]), Local: st.Flags&unix.MNT_LOCAL != 0}, nil
}

// fstypeName converts the NUL-terminated C char[16] f_fstypename to a Go string,
// stopping at the first NUL.
func fstypeName(raw []byte) string {
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		return string(raw[:i])
	}
	return string(raw)
}
