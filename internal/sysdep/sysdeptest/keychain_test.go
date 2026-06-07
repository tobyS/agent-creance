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

func TestFakeKeychainFindsRegisteredCertificate(t *testing.T) {
	kc := NewFakeKeychain().WithCertificate("mitmproxy", "-----BEGIN CERTIFICATE-----")
	got, err := kc.FindCertificate("mitmproxy")
	if err != nil {
		t.Fatalf("FindCertificate: %v", err)
	}
	if string(got) != "-----BEGIN CERTIFICATE-----" {
		t.Errorf("pem = %q, want the registered PEM", got)
	}
	if len(kc.CertLookups) != 1 || kc.CertLookups[0] != "mitmproxy" {
		t.Errorf("CertLookups = %+v, want [mitmproxy]", kc.CertLookups)
	}
}

func TestFakeKeychainAbsentCertificateIsNotFound(t *testing.T) {
	kc := NewFakeKeychain()
	if _, err := kc.FindCertificate("mitmproxy"); !errors.Is(err, sysdep.ErrItemNotFound) {
		t.Errorf("FindCertificate(absent) error = %v, want ErrItemNotFound", err)
	}
}

func TestFakeKeychainLockedWinsOverCertificate(t *testing.T) {
	kc := NewFakeKeychain().WithCertificate("mitmproxy", "x")
	kc.Locked = true
	if _, err := kc.FindCertificate("mitmproxy"); !errors.Is(err, sysdep.ErrKeychainLocked) {
		t.Errorf("FindCertificate(locked) error = %v, want ErrKeychainLocked", err)
	}
}

func TestFakeKeychainAddTrustedCertRecordsPath(t *testing.T) {
	kc := NewFakeKeychain()
	if err := kc.AddTrustedCert("/home/toby/.mitmproxy/mitmproxy-ca-cert.pem"); err != nil {
		t.Fatalf("AddTrustedCert: %v", err)
	}
	if len(kc.AddedCerts) != 1 || kc.AddedCerts[0] != "/home/toby/.mitmproxy/mitmproxy-ca-cert.pem" {
		t.Errorf("AddedCerts = %+v, want one matching path", kc.AddedCerts)
	}
}

func TestFakeKeychainAddTrustedCertReturnsScriptedErr(t *testing.T) {
	kc := NewFakeKeychain()
	sentinel := errors.New("add-trusted-cert boom")
	kc.AddCertErr = sentinel
	if err := kc.AddTrustedCert("/x.pem"); !errors.Is(err, sentinel) {
		t.Errorf("AddTrustedCert error = %v, want %v", err, sentinel)
	}
	// Even on error the call is recorded, so tests can assert it was attempted.
	if len(kc.AddedCerts) != 1 {
		t.Errorf("AddedCerts = %+v, want the failed call recorded", kc.AddedCerts)
	}
}
