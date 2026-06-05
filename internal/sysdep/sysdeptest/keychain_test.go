package sysdeptest

import (
	"errors"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestFakeKeychainFindsRegisteredItem(t *testing.T) {
	kc := NewFakeKeychain().WithItem("Claude Code-credentials", "toby", "secret-token")
	got, err := kc.FindGenericPassword("Claude Code-credentials", "toby")
	if err != nil {
		t.Fatalf("FindGenericPassword: %v", err)
	}
	if string(got) != "secret-token" {
		t.Errorf("secret = %q, want %q", got, "secret-token")
	}
	if len(kc.Lookups) != 1 || kc.Lookups[0] != (KeychainQuery{Service: "Claude Code-credentials", Account: "toby"}) {
		t.Errorf("Lookups = %+v, want one matching query", kc.Lookups)
	}
}

func TestFakeKeychainAbsentItemIsNotFound(t *testing.T) {
	kc := NewFakeKeychain()
	if _, err := kc.FindGenericPassword("svc", "acct"); !errors.Is(err, sysdep.ErrItemNotFound) {
		t.Errorf("FindGenericPassword(absent) error = %v, want ErrItemNotFound", err)
	}
}

func TestFakeKeychainLockedWinsOverItem(t *testing.T) {
	kc := NewFakeKeychain().WithItem("svc", "acct", "x")
	kc.Locked = true
	if _, err := kc.FindGenericPassword("svc", "acct"); !errors.Is(err, sysdep.ErrKeychainLocked) {
		t.Errorf("FindGenericPassword(locked) error = %v, want ErrKeychainLocked", err)
	}
}

func TestFakeKeychainScriptedErr(t *testing.T) {
	kc := NewFakeKeychain()
	sentinel := errors.New("securityd boom")
	kc.Errs[keychainKey("svc", "acct")] = sentinel
	if _, err := kc.FindGenericPassword("svc", "acct"); !errors.Is(err, sentinel) {
		t.Errorf("FindGenericPassword error = %v, want %v", err, sentinel)
	}
}
