package cred

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

var update = flag.Bool("update", false, "update golden files")

const (
	testHome    = "/home/toby"
	testAccount = "toby"
)

// credPath is the absolute path Detect stats for the file-fallback case, given
// testHome.
var credPath = filepath.Join(testHome, ".claude", ".credentials.json")

func TestDetect(t *testing.T) {
	cases := []struct {
		name        string
		keychain    func() *sysdeptest.FakeKeychain
		filePresent bool
		wantStatus  Status
		wantOK      bool
		golden      string // golden file for Message(); "" means expect empty message
	}{
		{
			name: "keychain item present",
			keychain: func() *sysdeptest.FakeKeychain {
				return sysdeptest.NewFakeKeychain().WithItem(KeychainService, testAccount, `{"claudeAiOauth":{}}`)
			},
			wantStatus: StatusOK,
			wantOK:     true,
		},
		{
			name: "login keychain locked",
			keychain: func() *sysdeptest.FakeKeychain {
				kc := sysdeptest.NewFakeKeychain()
				kc.Locked = true
				return kc
			},
			wantStatus: StatusLocked,
			golden:     "refuse_locked.golden",
		},
		{
			name:        "keychain absent, credentials file present",
			keychain:    sysdeptest.NewFakeKeychain,
			filePresent: true,
			wantStatus:  StatusFileFallback,
			golden:      "refuse_file_fallback.golden",
		},
		{
			name:       "keychain absent, neither present",
			keychain:   sysdeptest.NewFakeKeychain,
			wantStatus: StatusMissing,
			golden:     "refuse_missing.golden",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kc := tc.keychain()
			fsys := sysdeptest.NewFakeFileSystem()
			if tc.filePresent {
				fsys.Files[credPath] = []byte(`{"claudeAiOauth":{}}`)
			}
			paths := sysdeptest.NewFakePathResolver()
			paths.HomeDir = testHome
			paths.Env["USER"] = testAccount

			got, err := Detect(kc, fsys, paths)
			if err != nil {
				t.Fatalf("Detect returned error: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", got.Status, tc.wantStatus)
			}
			if got.OK() != tc.wantOK {
				t.Errorf("OK() = %v, want %v", got.OK(), tc.wantOK)
			}

			// The Keychain seam must be queried with the S2 service name.
			if len(kc.Lookups) != 1 {
				t.Fatalf("Keychain lookups = %d, want 1", len(kc.Lookups))
			}
			if kc.Lookups[0].Service != KeychainService {
				t.Errorf("lookup service = %q, want %q", kc.Lookups[0].Service, KeychainService)
			}
			if kc.Lookups[0].Account != testAccount {
				t.Errorf("lookup account = %q, want %q", kc.Lookups[0].Account, testAccount)
			}

			checkGolden(t, tc.golden, got.Message())
		})
	}
}

// checkGolden compares msg against the named golden file (writing it under
// -update), or asserts msg is empty when golden == "".
func checkGolden(t *testing.T, golden, msg string) {
	t.Helper()
	if golden == "" {
		if msg != "" {
			t.Errorf("Message() = %q, want empty", msg)
		}
		return
	}
	path := filepath.Join("testdata", golden)
	if *update {
		if err := os.WriteFile(path, []byte(msg), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update?): %v", err)
	}
	if msg != string(want) {
		t.Errorf("Message() = %q, want %q (golden %s)", msg, want, golden)
	}
}

// TestDetectKeychainError verifies an unexpected Keychain failure (neither
// absent nor locked) is surfaced as an error rather than a refusal Status.
func TestDetectKeychainError(t *testing.T) {
	boom := errors.New("securityd exploded")
	kc := sysdeptest.NewFakeKeychain()
	kc.Errs[keychainKey(KeychainService, testAccount)] = boom

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	paths.Env["USER"] = testAccount

	_, err := Detect(kc, sysdeptest.NewFakeFileSystem(), paths)
	if !errors.Is(err, boom) {
		t.Errorf("Detect error = %v, want it to wrap %v", err, boom)
	}
}

// TestDetectHomeDirError verifies an unresolvable home dir (during the
// file-fallback check) is surfaced as an error.
func TestDetectHomeDirError(t *testing.T) {
	boom := errors.New("no home")
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeErr = boom
	paths.Env["USER"] = testAccount

	_, err := Detect(sysdeptest.NewFakeKeychain(), sysdeptest.NewFakeFileSystem(), paths)
	if !errors.Is(err, boom) {
		t.Errorf("Detect error = %v, want it to wrap %v", err, boom)
	}
}

// TestDetectStatError verifies a genuine stat failure (not fs.ErrNotExist) on
// the credentials file is surfaced as an error.
func TestDetectStatError(t *testing.T) {
	boom := errors.New("permission denied")
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.StatErrs[credPath] = boom

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	paths.Env["USER"] = testAccount

	_, err := Detect(sysdeptest.NewFakeKeychain(), fsys, paths)
	if !errors.Is(err, boom) {
		t.Errorf("Detect error = %v, want it to wrap %v", err, boom)
	}
}

// keychainKey mirrors the fake's service+account map key so error injection
// targets the exact lookup Detect performs.
func keychainKey(service, account string) string {
	return service + "\x00" + account
}
