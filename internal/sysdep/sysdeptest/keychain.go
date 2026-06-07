package sysdeptest

import "github.com/tobyS/agent-creance/internal/sysdep"

// FakeKeychain is a scripted Keychain. You pre-load Items (service+account →
// secret) and optionally Errs (per-key error); set Locked to simulate a locked
// keychain. It records every lookup in Lookups so callers can assert the seam
// was queried with the expected service/account.
type FakeKeychain struct {
	// Items maps a service+account key (see keychainKey) to the secret bytes
	// FindGenericPassword should return.
	Items map[string][]byte
	// Certs maps a certificate common name to the PEM bytes FindCertificate should
	// return. A common name absent from the map yields sysdep.ErrItemNotFound.
	Certs map[string][]byte
	// Errs optionally maps a service+account key to an error to return instead.
	Errs map[string]error
	// Locked, when true, makes every lookup return sysdep.ErrKeychainLocked,
	// regardless of Items/Errs/Certs.
	Locked bool
	// Lookups records each FindGenericPassword call, in order.
	Lookups []KeychainQuery
	// CertLookups records each FindCertificate common name queried, in order.
	CertLookups []string
}

// KeychainQuery is one recorded FindGenericPassword call.
type KeychainQuery struct {
	Service string
	Account string
}

var _ sysdep.Keychain = (*FakeKeychain)(nil)

// NewFakeKeychain returns an empty, ready-to-populate fake.
func NewFakeKeychain() *FakeKeychain {
	return &FakeKeychain{
		Items: map[string][]byte{},
		Certs: map[string][]byte{},
		Errs:  map[string]error{},
	}
}

// WithItem is a builder helper: register a present item. Returns the receiver
// for chaining.
func (f *FakeKeychain) WithItem(service, account, secret string) *FakeKeychain {
	f.Items[keychainKey(service, account)] = []byte(secret)
	return f
}

// WithCertificate is a builder helper: register a present certificate by common
// name. Returns the receiver for chaining.
func (f *FakeKeychain) WithCertificate(commonName, pem string) *FakeKeychain {
	f.Certs[commonName] = []byte(pem)
	return f
}

func (f *FakeKeychain) FindGenericPassword(service, account string) ([]byte, error) {
	f.Lookups = append(f.Lookups, KeychainQuery{Service: service, Account: account})
	if f.Locked {
		return nil, sysdep.ErrKeychainLocked
	}
	key := keychainKey(service, account)
	if err, ok := f.Errs[key]; ok {
		return nil, err
	}
	if b, ok := f.Items[key]; ok {
		return append([]byte(nil), b...), nil
	}
	return nil, sysdep.ErrItemNotFound
}

func (f *FakeKeychain) FindCertificate(commonName string) ([]byte, error) {
	f.CertLookups = append(f.CertLookups, commonName)
	if f.Locked {
		return nil, sysdep.ErrKeychainLocked
	}
	if err, ok := f.Errs[commonName]; ok {
		return nil, err
	}
	if b, ok := f.Certs[commonName]; ok {
		return append([]byte(nil), b...), nil
	}
	return nil, sysdep.ErrItemNotFound
}

// keychainKey combines service and account into a single map key. The NUL
// separator cannot appear in either field, so the mapping is unambiguous.
func keychainKey(service, account string) string {
	return service + "\x00" + account
}
