package setupcheck

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const testHome = "/home/toby"

// skillPath is the absolute path Verify stats for the skill, given testHome.
var skillPath = filepath.Join(testHome, ".claude", "skills", "agent-creance", "SKILL.md")

func TestVerify(t *testing.T) {
	cases := []struct {
		name         string
		keychain     func() *sysdeptest.FakeKeychain
		skillPresent bool
		wantStatus   Status
		wantOK       bool
		wantMsg      string // substring expected in Message(); "" means empty message
	}{
		{
			name: "ca trusted and skill present",
			keychain: func() *sysdeptest.FakeKeychain {
				return sysdeptest.NewFakeKeychain().WithCertificate(CACommonName, "-----BEGIN CERTIFICATE-----")
			},
			skillPresent: true,
			wantStatus:   StatusOK,
			wantOK:       true,
		},
		{
			name:       "ca not trusted",
			keychain:   sysdeptest.NewFakeKeychain,
			wantStatus: StatusCANotTrusted,
			wantMsg:    "agent-creance setup",
		},
		{
			name: "ca trusted but skill missing",
			keychain: func() *sysdeptest.FakeKeychain {
				return sysdeptest.NewFakeKeychain().WithCertificate(CACommonName, "x")
			},
			skillPresent: false,
			wantStatus:   StatusSkillMissing,
			wantMsg:      "agent-creance setup",
		},
		{
			name: "login keychain locked",
			keychain: func() *sysdeptest.FakeKeychain {
				kc := sysdeptest.NewFakeKeychain()
				kc.Locked = true
				return kc
			},
			wantStatus: StatusKeychainLocked,
			wantMsg:    "locked",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kc := tc.keychain()
			fsys := sysdeptest.NewFakeFileSystem()
			if tc.skillPresent {
				fsys.Files[skillPath] = []byte("# skill")
			}
			paths := sysdeptest.NewFakePathResolver()
			paths.HomeDir = testHome

			got, err := Verify(kc, fsys, paths)
			if err != nil {
				t.Fatalf("Verify returned error: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", got.Status, tc.wantStatus)
			}
			if got.OK() != tc.wantOK {
				t.Errorf("OK() = %v, want %v", got.OK(), tc.wantOK)
			}

			// The Keychain seam must be queried for the mitmproxy CA.
			if len(kc.CertLookups) != 1 || kc.CertLookups[0] != CACommonName {
				t.Errorf("CertLookups = %+v, want [%s]", kc.CertLookups, CACommonName)
			}

			msg := got.Message()
			if tc.wantMsg == "" {
				if msg != "" {
					t.Errorf("Message() = %q, want empty", msg)
				}
			} else if !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("Message() = %q, want it to contain %q", msg, tc.wantMsg)
			}
		})
	}
}

// TestVerifyKeychainError verifies an unexpected Keychain failure (neither absent
// nor locked) is surfaced as an error rather than a refusal Status.
func TestVerifyKeychainError(t *testing.T) {
	boom := errors.New("securityd exploded")
	kc := sysdeptest.NewFakeKeychain()
	kc.Errs[CACommonName] = boom

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome

	if _, err := Verify(kc, sysdeptest.NewFakeFileSystem(), paths); !errors.Is(err, boom) {
		t.Errorf("Verify error = %v, want it to wrap %v", err, boom)
	}
}

// TestVerifyHomeDirError verifies an unresolvable home dir (during the skill
// check) is surfaced as an error.
func TestVerifyHomeDirError(t *testing.T) {
	boom := errors.New("no home")
	kc := sysdeptest.NewFakeKeychain().WithCertificate(CACommonName, "x")
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeErr = boom

	if _, err := Verify(kc, sysdeptest.NewFakeFileSystem(), paths); !errors.Is(err, boom) {
		t.Errorf("Verify error = %v, want it to wrap %v", err, boom)
	}
}

// TestVerifyStatError verifies a genuine stat failure (not fs.ErrNotExist) on the
// skill file is surfaced as an error.
func TestVerifyStatError(t *testing.T) {
	boom := errors.New("permission denied")
	kc := sysdeptest.NewFakeKeychain().WithCertificate(CACommonName, "x")
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.StatErrs[skillPath] = boom

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome

	if _, err := Verify(kc, fsys, paths); !errors.Is(err, boom) {
		t.Errorf("Verify error = %v, want it to wrap %v", err, boom)
	}
}
