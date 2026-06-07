// Package setupcheck is agent-creance's cheap "has setup run?" precondition for
// the run command. Before launching a caged session, run asks setupcheck whether
// the one-time `agent-creance setup` was completed, so it can refuse up front
// with a clear pointer instead of failing mid-launch with a confusing error.
//
// Two things prove setup ran (design.md "Commands", "The proxy and the
// credential story"):
//
//   - the mitmproxy CA is trusted — setup imports it into the login Keychain.
//     setupcheck probes presence of a certificate named "mitmproxy" via the
//     Keychain seam. This is deliberately the *cheap* check: presence proves the
//     import happened, not that the trust dialog was confirmed (security
//     add-trusted-cert returns 0 even on cancel, design.md). The robust live
//     verification — spawn mitmproxy, curl through it, validate the chain — stays
//     setup/doctor's job; run must not pay that cost on every launch.
//   - the skill is installed — setup writes ~/.claude/skills/agent-creance/SKILL.md.
//     setupcheck checks the file is present via the FileSystem seam.
//
// All access goes through the sysdep seams, so the check is unit-testable without
// a logged-in macOS session.
package setupcheck

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// CACommonName is the common name of the mitmproxy-generated CA certificate that
// setup installs into the login Keychain.
const CACommonName = "mitmproxy"

// SkillFileRel is the agent-creance skill location, relative to the user's home
// directory. setup writes it (InstallSkill) and run's precondition check looks for
// it; sharing one constant keeps the writer and the checker from drifting apart.
var SkillFileRel = filepath.Join(".claude", "skills", "agent-creance", "SKILL.md")

// Status is the outcome of the setup-precondition check.
type Status int

const (
	// StatusOK means both the trusted CA and the skill are present — run can proceed.
	StatusOK Status = iota
	// StatusCANotTrusted means the mitmproxy CA is not in the login Keychain, so
	// setup has not installed (or not finished installing) it.
	StatusCANotTrusted
	// StatusSkillMissing means the CA is present but the skill file is absent.
	StatusSkillMissing
	// StatusKeychainLocked means the login keychain is locked, so the CA presence
	// can't be read; the user must unlock it (the same blocking-prompt hazard cred
	// reports, spike S2).
	StatusKeychainLocked
)

// Result is the outcome of Verify.
type Result struct {
	Status Status
}

// OK reports whether setup has been completed (both preconditions present).
func (r Result) OK() bool { return r.Status == StatusOK }

// Message returns the human-facing refusal message for the result, or "" when
// setup is OK. The strings are deterministic so they can be pinned in tests.
func (r Result) Message() string {
	switch r.Status {
	case StatusOK:
		return ""
	case StatusCANotTrusted:
		return msgCANotTrusted
	case StatusSkillMissing:
		return msgSkillMissing
	case StatusKeychainLocked:
		return msgKeychainLocked
	default:
		return ""
	}
}

const (
	msgCANotTrusted = "Setup has not been completed: the mitmproxy CA is not trusted. " +
		"Run `agent-creance setup` first."
	msgSkillMissing = "Setup has not been completed: the agent-creance skill is not installed. " +
		"Run `agent-creance setup` first."
	msgKeychainLocked = "The login keychain is locked, so the mitmproxy CA can't be verified. " +
		"Unlock your login keychain and retry."
)

// Verify checks the two setup preconditions cheaply. Like cred.Detect, the
// expected outcomes (CA absent, skill absent, keychain locked) are data encoded
// in Result.Status, not errors; Verify returns a non-nil error only for a
// genuinely unexpected failure (a Keychain error that is neither absent nor
// locked, an unresolvable home directory, or an unexpected stat failure).
//
// The CA is checked first: it is the heavier precondition (it needs the one-time
// sudo trust step), so pointing at it first gives the most actionable message.
func Verify(kc sysdep.Keychain, fsys sysdep.FileSystem, paths sysdep.PathResolver) (Result, error) {
	switch _, err := kc.FindCertificate(CACommonName); {
	case err == nil:
		// CA present — fall through to the skill check.
	case errors.Is(err, sysdep.ErrKeychainLocked):
		return Result{Status: StatusKeychainLocked}, nil
	case errors.Is(err, sysdep.ErrItemNotFound):
		return Result{Status: StatusCANotTrusted}, nil
	default:
		return Result{}, fmt.Errorf("setupcheck: keychain lookup: %w", err)
	}

	home, err := paths.UserHomeDir()
	if err != nil {
		return Result{}, fmt.Errorf("setupcheck: resolve home dir: %w", err)
	}
	path := filepath.Join(home, SkillFileRel)
	switch _, err := fsys.Stat(path); {
	case err == nil:
		return Result{Status: StatusOK}, nil
	case errors.Is(err, fs.ErrNotExist):
		return Result{Status: StatusSkillMissing}, nil
	default:
		return Result{}, fmt.Errorf("setupcheck: stat %s: %w", path, err)
	}
}
