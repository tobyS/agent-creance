package sysdep

import (
	"reflect"
	"testing"
)

// The real OSListenerScanner shells out to lsof, so its end-to-end behaviour is
// exercised only by the integration test. The parsing and the exposed-address
// classification are pure, so we table-test them here without lsof.

func TestParseLsof(t *testing.T) {
	// lsof -F pcn output: one field per line, first byte is the field id. A "p"
	// line starts a process (its "c" follows); each "n" is one socket.
	out := "" +
		"p501\n" +
		"cnode\n" +
		"n*:8080\n" +
		"n127.0.0.1:5000\n" +
		"p77\n" +
		"csshd\n" +
		"n*:22\n" +
		"p900\n" +
		"cControlCe\n" +
		"n[::1]:7000\n"

	got := ParseLsof([]byte(out))
	want := []Listener{
		{Command: "node", PID: 501, Address: "*:8080"},
		{Command: "node", PID: 501, Address: "127.0.0.1:5000"},
		{Command: "sshd", PID: 77, Address: "*:22"},
		{Command: "ControlCe", PID: 900, Address: "[::1]:7000"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLsof() = %+v, want %+v", got, want)
	}
}

func TestParseLsof_Empty(t *testing.T) {
	if got := ParseLsof(nil); got != nil {
		t.Errorf("ParseLsof(nil) = %+v, want nil", got)
	}
	if got := ParseLsof([]byte("")); got != nil {
		t.Errorf("ParseLsof(empty) = %+v, want nil", got)
	}
}

func TestIsExposed(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"*:8080", true}, // lsof wildcard (0.0.0.0 and ::)
		{"*:22", true},
		{"0.0.0.0:3000", true},
		{"[::]:443", true},
		{":::443", true},
		{"127.0.0.1:5000", false},
		{"[::1]:631", false},
		{"192.168.1.5:8080", false}, // specific interface, not all interfaces
		{"", false},
	}
	for _, tc := range cases {
		if got := IsExposed(tc.addr); got != tc.want {
			t.Errorf("IsExposed(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
