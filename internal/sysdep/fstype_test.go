package sysdep

import "testing"

// OSFilesystemTyper.FSType calls statfs(2), exercised by the integration test.
// The NUL-trimming of the C f_fstypename array is pure, so we table-test it here.

func TestFstypeName(t *testing.T) {
	mk := func(s string) []byte {
		b := make([]byte, 16) // mirror the C char[16], NUL-padded
		copy(b, s)
		return b
	}
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "apfs", raw: mk("apfs"), want: "apfs"},
		{name: "smbfs", raw: mk("smbfs"), want: "smbfs"},
		{name: "empty", raw: mk(""), want: ""},
		{name: "no trailing NUL fills the array", raw: []byte("exactly_16_chars"), want: "exactly_16_chars"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fstypeName(tc.raw); got != tc.want {
				t.Errorf("fstypeName(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
