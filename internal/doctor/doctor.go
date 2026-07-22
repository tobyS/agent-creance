package doctor

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/cred"
	"github.com/tobyS/agent-creance/internal/prereq"
	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/setup"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// iCloudMarker is the path segment every iCloud Drive / FileProvider document
// container lives under on macOS. iCloud reports "apfs" + MNT_LOCAL via statfs, so
// it cannot be detected by filesystem type — only by path.
const iCloudMarker = "/Library/Mobile Documents/"

// Checker bundles the seams doctor needs. cli/doctor.go builds it from the App.
type Checker struct {
	Commander sysdep.Commander
	Tested    map[string]string
	Installer *setup.Installer
	Manager   *proxy.Manager
	Resolver  *state.Resolver
	Listeners sysdep.ListenerScanner
	FSType    sysdep.FilesystemTyper
	Paths     sysdep.PathResolver
	Keychain  sysdep.Keychain
	FS        sysdep.FileSystem
}

// Run executes every diagnostic and returns the Report. When fix is true, a found
// orphan proxy is cleaned. No check aborts the others: an environment failure
// becomes a Warn/Skipped finding (status-as-data), so doctor never crashes on a
// missing tool. The error return is reserved for a future truly-fatal condition;
// today it is always nil.
func (c *Checker) Run(ctx context.Context, fix bool) (Report, error) {
	var r Report
	r.Version = prereq.Check(ctx, c.Commander, prereq.DefaultTools(c.Tested))
	r.Missing = prereq.MissingInstructions(r.Version)
	r.CA = c.checkCA(ctx)
	r.Cred = c.checkCred()
	r.Minted = c.checkMintedCreds()
	r.Proxy = c.checkProxy(fix)
	r.Exposed = c.checkExposed(ctx)
	r.FS = c.checkFS()
	return r, nil
}

// checkCA runs the live CA verification, staying read-only: if no CA has been
// generated it reports "run setup" without generating one (Verify would otherwise
// materialise the CA by spawning mitmdump).
func (c *Checker) checkCA(ctx context.Context) CASection {
	gen, err := c.Installer.CAGenerated()
	if err != nil {
		return CASection{State: StatusWarn, Detail: "could not check CA: " + err.Error()}
	}
	if !gen {
		return CASection{State: StatusWarn, Detail: "CA not generated — run `agent-creance setup`"}
	}
	res, err := c.Installer.Verify(ctx)
	if err != nil {
		return CASection{State: StatusWarn, Detail: "could not verify (mitmproxy unavailable)"}
	}
	if !res.OK() {
		return CASection{State: StatusProblem, Detail: res.Message()}
	}
	return CASection{State: StatusOK, Detail: "trusted"}
}

// checkCred reports whether the host Claude credential is reachable, mirroring run's
// cred.Detect precondition (cli/run.go) so doctor answers "is my credential reachable,
// and if not, why" without starting a caged session. The wording comes from
// cred.Result.Message() so the run and doctor paths cannot drift. Severity (AC-0062): a
// locked keychain or an unsupported file-based credential is an actionable Problem; "not
// logged in" is a Warn (a precondition the user fixes by logging in, not a broken
// environment); an unexpected lookup failure degrades to Warn (status-as-data). doctor
// --fix deliberately does NOT act here — unlocking the keychain and running `claude` login
// are interactive user actions, not automatable fixes.
func (c *Checker) checkCred() CredSection {
	res, err := cred.Detect(c.Keychain, c.FS, c.Paths)
	if err != nil {
		return CredSection{State: StatusWarn, Detail: "could not check credential: " + err.Error()}
	}
	switch res.Status {
	case cred.StatusOK:
		return CredSection{State: StatusOK, Detail: "reachable"}
	case cred.StatusLocked, cred.StatusFileFallback:
		return CredSection{State: StatusProblem, Detail: res.Message()}
	case cred.StatusMissing:
		return CredSection{State: StatusWarn, Detail: res.Message()}
	default:
		// Defensive: a future cred.Status defaults to the hermeticity-safe Warn rather
		// than silently becoming actionable.
		return CredSection{State: StatusWarn, Detail: "credential state unknown"}
	}
}

// projectConfigFile is the per-project source config doctor loads to enumerate minted
// credentials. Mirrors cli.configFile; kept local so the doctor package needs no
// cli import.
const projectConfigFile = ".agent-creance.yaml"

// checkMintedCreds reports the authorization state of the project's minted credentials
// (AC-0069a) without prompting: a keychain:// reference is checked for existence
// (no secret read, no ACL prompt); an op:// reference is reported "configured"
// (resolving it would prompt Touch ID, which doctor must never do); an env://
// reference is checked against the environment. An unauthorized credential is a
// warning with the actionable next step — never a hard problem (it never changes the
// exit code), consistent with the broker-down advisory. A project with no minted
// credentials (or no loadable config) yields an empty section, which Render omits.
func (c *Checker) checkMintedCreds() MintedCredSection {
	cfg, err := config.NewLoader(c.FS, c.Paths).Load(projectConfigFile)
	if err != nil {
		return MintedCredSection{} // no/invalid config → nothing to report here
	}
	names := make([]string, 0, len(cfg.Credentials))
	for name, cr := range cfg.Credentials {
		if cr.IsMinted() {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var out []MintedCredStatus
	for _, name := range names {
		cr := cfg.Credentials[name]
		switch {
		case cr.GitHubApp != nil:
			out = append(out, c.checkMintedRef(name, "github-app", cr.GitHubApp.Key))
		case cr.OAuth2 != nil:
			out = append(out, c.checkMintedRef(name, "oauth2", cr.OAuth2.RefreshToken))
		}
	}
	return MintedCredSection{Creds: out}
}

// checkMintedRef checks one minted credential's secret reference without prompting.
func (c *Checker) checkMintedRef(name, kind, ref string) MintedCredStatus {
	authHint := "run `agent-creance credential authorize " + name + "`"
	switch {
	case strings.HasPrefix(ref, "keychain://"):
		service, account := parseKeychainRef(strings.TrimPrefix(ref, "keychain://"))
		found, err := c.Keychain.HasGenericPassword(service, account)
		switch {
		case errors.Is(err, sysdep.ErrKeychainLocked):
			return MintedCredStatus{name, kind, StatusWarn, "could not verify (keychain locked; unlock and re-run)"}
		case err != nil:
			return MintedCredStatus{name, kind, StatusWarn, "could not verify (" + err.Error() + ")"}
		case !found && kind == "oauth2":
			return MintedCredStatus{name, kind, StatusWarn, "not authorized yet — " + authHint}
		case !found:
			return MintedCredStatus{name, kind, StatusWarn, "app private key not found in the keychain — check the github_app key reference"}
		default:
			return MintedCredStatus{name, kind, StatusOK, "authorized"}
		}
	case strings.HasPrefix(ref, "op://"):
		// Resolving op:// would prompt Touch ID; report configured without prompting.
		return MintedCredStatus{name, kind, StatusOK, "configured (op:// — unlockable at run)"}
	case strings.HasPrefix(ref, "env://"):
		envName := strings.TrimPrefix(ref, "env://")
		if c.Paths.Getenv(envName) == "" {
			hint := authHint
			if kind == "github-app" {
				hint = "set the app-key environment variable"
			}
			return MintedCredStatus{name, kind, StatusWarn, "not authorized (" + ref + " is unset) — " + hint}
		}
		return MintedCredStatus{name, kind, StatusOK, "authorized"}
	default:
		return MintedCredStatus{name, kind, StatusWarn, "unrecognized reference " + ref}
	}
}

// parseKeychainRef splits "service[/account]" (the part after keychain://) into its
// service and optional account, mirroring the SecretResolver's parsing.
func parseKeychainRef(rest string) (service, account string) {
	service, account, _ = strings.Cut(rest, "/")
	return service, account
}

// checkProxy inspects the current project's proxy.lock and, when fix is set, cleans
// a detected orphan. A resolve/inspect failure degrades to "no proxy state".
func (c *Checker) checkProxy(fix bool) ProxySection {
	layout, err := c.Resolver.Resolve(".")
	if err != nil {
		return ProxySection{}
	}
	diag, err := c.Manager.Inspect(layout)
	if err != nil {
		return ProxySection{}
	}
	sec := ProxySection{Diag: diag}
	if fix && diag.Orphan {
		if res, err := c.Manager.CleanOrphan(layout); err == nil {
			sec.Cleaned = &res
		}
	}
	return sec
}

// checkExposed scans host listeners and reports those bound to all interfaces.
func (c *Checker) checkExposed(ctx context.Context) ExposedSection {
	all, err := c.Listeners.Listeners(ctx)
	if err != nil {
		return ExposedSection{State: StatusSkipped, Detail: "could not scan (lsof unavailable)"}
	}
	var exposed []sysdep.Listener
	for _, l := range all {
		if sysdep.IsExposed(l.Address) {
			exposed = append(exposed, l)
		}
	}
	if len(exposed) == 0 {
		return ExposedSection{State: StatusOK}
	}
	return ExposedSection{State: StatusWarn, Listeners: exposed}
}

// checkFS warns when the working dir or the state cache dir are on a filesystem
// where advisory locks are unreliable (network mounts, iCloud Drive).
func (c *Checker) checkFS() FSSection {
	cwd, _ := c.Paths.Abs(".")
	cache, _ := c.Resolver.CacheDir()
	targets := []struct{ label, path string }{
		{"working directory", cwd},
		{"state cache", cache},
	}
	var warnings []FSWarning
	for _, t := range targets {
		if t.path == "" {
			continue
		}
		info, resolved, ok := c.probeFS(t.path)
		if !ok {
			continue // could not statfs any ancestor — skip silently
		}
		if warn, reason := classifyFS(info, resolved); warn {
			warnings = append(warnings, FSWarning{Label: t.label, Path: t.path, FSType: info.Name, Reason: reason})
		}
	}
	if len(warnings) == 0 {
		return FSSection{State: StatusOK}
	}
	return FSSection{State: StatusWarn, Warnings: warnings}
}

// probeFS resolves symlinks and statfs's path, ascending to the nearest existing
// ancestor when the exact path does not exist yet (e.g. the cache dir before first
// use). It returns the statfs info, the resolved path (for the iCloud check), and
// whether any ancestor could be probed.
func (c *Checker) probeFS(path string) (sysdep.FSInfo, string, bool) {
	resolved := path
	if r, err := c.Paths.EvalSymlinks(path); err == nil {
		resolved = r
	}
	for p := resolved; ; {
		info, err := c.FSType.FSType(p)
		if err == nil {
			return info, resolved, true
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return sysdep.FSInfo{}, resolved, false
		}
		parent := filepath.Dir(p)
		if parent == p {
			return sysdep.FSInfo{}, resolved, false
		}
		p = parent
	}
}

// classifyFS decides whether a filesystem warrants a flock-reliability warning. It
// is pure so it is table-tested. iCloud (path-based) takes precedence over the
// generic network-mount reason.
func classifyFS(info sysdep.FSInfo, resolvedPath string) (bool, string) {
	if strings.Contains(resolvedPath, iCloudMarker) {
		return true, "iCloud Drive"
	}
	if !info.Local {
		return true, "network mount"
	}
	return false, ""
}
