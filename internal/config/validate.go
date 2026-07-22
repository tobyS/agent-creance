package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// validate checks cross-field schema constraints local to one document, recording
// every problem on verr (it does not stop at the first). It runs after applyDefaults,
// so every rule Mode is already non-empty and every credential Header is defaulted.
// Cross-layer checks (inject → defined credential) run later, in ValidateEffective.
func (c *Config) validate(verr *ValidationError) {
	validateRules(c.Network.Egress.Allow, "allow", verr)
	validateRules(c.Network.Egress.DenyAlways, "deny_always", verr)
	validateCredentials(c.Credentials, verr)
}

// validateRules checks one allow/deny list. The mode is validated uniformly across
// both lists: the Rule shape is uniform, so a stray mode: on a deny rule is better
// caught than silently ignored.
func validateRules(rules []Rule, list string, verr *ValidationError) {
	for i, r := range rules {
		ref := ruleRef(i, r)
		if r.Host == "" {
			verr.add("egress %s rule %s is missing a host", list, ref)
		} else {
			validateRuleHostMethods(r, list, ref, verr)
		}
		switch r.Mode {
		case ModeIntercept:
			// Fully intercepted: paths/methods are allowed (and optional).
		case ModePassthrough:
			// A passthrough host is a raw CONNECT tunnel with no TLS termination, so
			// path/method matching is impossible — carrying them is a config error,
			// not a silent no-op (docs/design.md "Per-host enforcement modes").
			if r.Paths != nil {
				verr.add("egress %s rule %s uses mode: passthrough, which cannot carry paths (use mode: intercept to filter by path, or drop paths for a passthrough tunnel)", list, ref)
			}
			if r.Methods != nil {
				verr.add("egress %s rule %s uses mode: passthrough, which cannot carry methods (use mode: intercept to filter by method, or drop methods for a passthrough tunnel)", list, ref)
			}
		default:
			verr.add("egress %s rule %s has unknown mode %q (want %q or %q)", list, ref, r.Mode, ModeIntercept, ModePassthrough)
		}
		validateRuleAuth(r, list, ref, verr)
	}
}

// validateRuleAuth checks the auth axis (AC-0068) local to one rule: inject and
// in_cage are mutually exclusive, and inject requires an intercepted host (a
// passthrough tunnel is never TLS-terminated, so the proxy cannot inject a header).
// in_cage on a passthrough rule is fine — the discussion's "Anthropic API key on the
// passthrough host is necessarily in-cage". The inject → defined-credential check is
// cross-layer and lives in ValidateEffective.
func validateRuleAuth(r Rule, list, ref string, verr *ValidationError) {
	if r.Inject != "" && r.InCage {
		verr.add("egress %s rule %s sets both inject and in_cage (a host is either injected or in-cage, not both)", list, ref)
	}
	if r.Inject != "" && r.Mode == ModePassthrough {
		verr.add("egress %s rule %s uses mode: passthrough with inject %q, but a passthrough tunnel is never TLS-terminated so the proxy cannot inject a credential (use mode: intercept)", list, ref, r.Inject)
	}
}

// validateCredentials checks each credentials: entry is well-formed on its own: a
// known-scheme source reference (op:// / keychain:// / env://, not resolved here), a
// valid value-template, and a sane target header. Whether an entry is referenced by a
// rule is a cross-layer concern (ValidateEffective).
func validateCredentials(creds map[string]Credential, verr *ValidationError) {
	// Sort names so accumulated messages are deterministic (map order is random).
	names := make([]string, 0, len(creds))
	for name := range creds {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cred := creds[name]
		if name == "" {
			verr.add("credentials has an entry with an empty name")
		}

		// Exactly one form: a static source, a github_app block, or an oauth2 block.
		forms := 0
		if cred.Source != "" {
			forms++
		}
		if cred.GitHubApp != nil {
			forms++
		}
		if cred.OAuth2 != nil {
			forms++
		}
		switch {
		case forms == 0:
			verr.add("credential %q defines no form: set a source (op:// / keychain:// / env://), a github_app: block, or an oauth2: block", name)
		case forms > 1:
			verr.add("credential %q sets more than one form (source / github_app / oauth2 are mutually exclusive)", name)
		case cred.Source != "":
			if err := sysdep.ValidateSecretRefSyntax(cred.Source); err != nil {
				verr.add("credential %q has an invalid source %q (want an op:// , keychain:// , or env:// reference)", name, cred.Source)
			}
		case cred.GitHubApp != nil:
			validateGitHubAppMint(name, cred.GitHubApp, verr)
		case cred.OAuth2 != nil:
			validateOAuth2Mint(name, cred.OAuth2, verr)
		}

		// The injected-header shape (template/header/username) applies to every form.
		if cred.Template == "" {
			verr.add("credential %q is missing a template (e.g. \"Bearer {token}\")", name)
		} else if err := validateTemplate(cred.Template, cred.Username); err != nil {
			verr.add("credential %q has an invalid template: %v", name, err)
		}
		if err := validateHeaderName(cred.Header); err != nil {
			verr.add("credential %q has an invalid header %q: %v", name, cred.Header, err)
		}
	}
}

// knownGitHubPermissionLevels is the set of access levels a GitHub App installation
// token may request per permission (a down-scope cap). GitHub uses read/write and,
// for a few permissions, admin.
var knownGitHubPermissionLevels = map[string]bool{"read": true, "write": true, "admin": true}

// validateGitHubAppMint checks a github_app: block: a valid key secret reference, a
// non-empty client_id, an owner/name repo, and known permission levels. It does not
// resolve the key or contact GitHub (that is minting, host-side at run).
func validateGitHubAppMint(name string, m *GitHubAppMint, verr *ValidationError) {
	if m.Key == "" {
		verr.add("credential %q github_app is missing a key (an op:// , keychain:// , or env:// reference to the app private key)", name)
	} else if err := sysdep.ValidateSecretRefSyntax(m.Key); err != nil {
		verr.add("credential %q github_app has an invalid key %q (want an op:// , keychain:// , or env:// reference)", name, m.Key)
	}
	if m.ClientID == "" {
		verr.add("credential %q github_app is missing a client_id", name)
	}
	if err := validateRepoSlug(m.Repo); err != nil {
		verr.add("credential %q github_app has an invalid repo %q: %v (want owner/name)", name, m.Repo, err)
	}
	levels := make([]string, 0, len(m.Permissions))
	for perm := range m.Permissions {
		levels = append(levels, perm)
	}
	sort.Strings(levels)
	for _, perm := range levels {
		if !knownGitHubPermissionLevels[m.Permissions[perm]] {
			verr.add("credential %q github_app permission %q has an unknown level %q (want read, write, or admin)", name, perm, m.Permissions[perm])
		}
	}
}

// validateOAuth2Mint checks an oauth2: block: a valid refresh_token secret
// reference, a non-empty client_id, and a plausible https token endpoint. Endpoint
// and scopes are already defaulted (applyDefaults) by the time validation runs.
func validateOAuth2Mint(name string, m *OAuth2Mint, verr *ValidationError) {
	if m.RefreshToken == "" {
		verr.add("credential %q oauth2 is missing a refresh_token (an op:// , keychain:// , or env:// reference)", name)
	} else if err := sysdep.ValidateSecretRefSyntax(m.RefreshToken); err != nil {
		verr.add("credential %q oauth2 has an invalid refresh_token %q (want an op:// , keychain:// , or env:// reference)", name, m.RefreshToken)
	}
	if m.ClientID == "" {
		verr.add("credential %q oauth2 is missing a client_id", name)
	}
	if !strings.HasPrefix(m.TokenEndpoint, "https://") {
		verr.add("credential %q oauth2 has an invalid token_endpoint %q (want an https:// URL)", name, m.TokenEndpoint)
	}
}

// validateRepoSlug checks a "owner/name" GitHub repository slug: exactly one slash,
// non-empty owner and name, no whitespace or control characters.
func validateRepoSlug(repo string) error {
	if repo == "" {
		return fmt.Errorf("empty")
	}
	if strings.IndexFunc(repo, isControlRune) >= 0 || strings.ContainsAny(repo, " \t") {
		return fmt.Errorf("contains whitespace or a control character")
	}
	owner, rname, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || rname == "" || strings.Contains(rname, "/") {
		return fmt.Errorf("not in owner/name form")
	}
	return nil
}

// validateHeaderName checks a credential's target header is a plausible HTTP field
// name: non-empty, no control characters, no whitespace or ':' (which would break the
// header the proxy writes).
func validateHeaderName(h string) error {
	if h == "" {
		return fmt.Errorf("empty")
	}
	if strings.IndexFunc(h, isControlRune) >= 0 {
		return fmt.Errorf("contains a control character")
	}
	if strings.ContainsAny(h, " \t:") {
		return fmt.Errorf("contains whitespace or ':'")
	}
	return nil
}

// ValidateEffective runs the cross-layer credential checks that only make sense on the
// fully-merged config — a rule may inject a credential defined in a different layer
// (e.g. a team-shared global). Every inject must name a defined credential (a hard
// error, returned as a *ValidationError); a defined-but-never-injected credential, and
// a username with no {user} placeholder, are non-fatal warnings. The Loader calls this
// after merging global + project and stores the warnings on Config.Warnings.
func (c *Config) ValidateEffective() ([]string, error) {
	verr := &ValidationError{}
	referenced := map[string]bool{}

	checkList := func(rules []Rule, list string) {
		for i, r := range rules {
			if r.Inject == "" {
				continue
			}
			referenced[r.Inject] = true
			if _, ok := c.Credentials[r.Inject]; !ok {
				verr.add("egress %s rule %s injects credential %q, which is not defined in credentials:", list, ruleRef(i, r), r.Inject)
			}
		}
	}
	checkList(c.Network.Egress.Allow, "allow")
	checkList(c.Network.Egress.DenyAlways, "deny_always")

	var warnings []string
	for name, cred := range c.Credentials {
		if !referenced[name] {
			warnings = append(warnings, fmt.Sprintf("credential %q is defined but never injected by any rule", name))
		}
		if cred.Username != "" && !strings.Contains(cred.Template, userPlaceholder) {
			warnings = append(warnings, fmt.Sprintf("credential %q sets a username but its template %q has no %s placeholder", name, cred.Template, userPlaceholder))
		}
	}
	sort.Strings(warnings) // map iteration is random; keep output stable

	if len(verr.Issues) > 0 {
		return warnings, verr
	}
	return warnings, nil
}

// ruleRef names a rule for an error message: by host when it has one, else by its
// 1-based position in the list.
func ruleRef(i int, r Rule) string {
	if r.Host != "" {
		return fmt.Sprintf("for host %q", r.Host)
	}
	return fmt.Sprintf("#%d", i+1)
}

// parseGenerator parses one network.egress.generators entry from its raw yaml node.
// A scalar is the bare form (`- package_json`) → Generator{Type: <scalar>}. A mapping
// is the object form (`- {type:, path:}`); its keys are validated here (KnownFields
// does not reach into a captured yaml.Node) — only "type" (required, non-empty) and
// "path" (optional) are allowed, anything else is an error citing the line. The
// generator name's validity (Known) is checked later, by the compiler.
func parseGenerator(node *yaml.Node) (Generator, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return Generator{}, fmt.Errorf("egress generators line %d: empty generator name", node.Line)
		}
		return Generator{Type: node.Value}, nil
	case yaml.MappingNode:
		var g Generator
		var sawType bool
		// MappingNode content is a flat [key, value, key, value, …] list.
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			val := node.Content[i+1]
			switch key.Value {
			case "type":
				g.Type = val.Value
				sawType = true
			case "path":
				g.Path = val.Value
			default:
				return Generator{}, fmt.Errorf("egress generators line %d: unknown key %q (want %q or %q)", key.Line, key.Value, "type", "path")
			}
		}
		if !sawType || g.Type == "" {
			return Generator{}, fmt.Errorf("egress generators line %d: missing %q", node.Line, "type")
		}
		return g, nil
	default:
		return Generator{}, fmt.Errorf("egress generators line %d: a generator must be a name or a {type, path} mapping", node.Line)
	}
}

// ParseHostService parses a "label:port" entry into a typed HostService, applying the
// same validation the loader uses (non-empty, control-character-free label; port in
// 1-65535). Exported for the `service add` command to validate its argument up front
// (AC-0067).
func ParseHostService(s string) (HostService, error) { return parseHostService(s) }

// parseHostService parses a "label:port" entry into a typed HostService. The label
// is cosmetic but must be non-empty; the port must be a number in 1-65535.
func parseHostService(s string) (HostService, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return HostService{}, fmt.Errorf("host_services entry %q is not in label:port form", s)
	}
	label, portStr := s[:i], s[i+1:]
	if label == "" {
		return HostService{}, fmt.Errorf("host_services entry %q has an empty label", s)
	}
	// The label is written into the generated network.sb after a ";; " comment marker
	// (internal/profile.RenderNetworkSB). A control character — most dangerously a
	// newline — would terminate the comment line and let the rest of the label render as
	// a live SBPL form *after* the (deny network*) baseline, re-opening egress
	// (last-match-wins). Reject any control char here; the renderer also sanitizes
	// defensively (AC-0058).
	if i := strings.IndexFunc(label, isControlRune); i >= 0 {
		return HostService{}, fmt.Errorf("host_services entry %q has a control character in its label", s)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return HostService{}, fmt.Errorf("host_services entry %q has a non-numeric port %q", s, portStr)
	}
	if port < 1 || port > 65535 {
		return HostService{}, fmt.Errorf("host_services entry %q has port %d out of range 1-65535", s, port)
	}
	return HostService{Label: label, Port: port}, nil
}

// knownMethods is the set of HTTP methods an egress rule may name. The enforcer's
// method match is case-sensitive, so a lowercase or unknown verb silently never
// matches (F18); validation rejects it at load instead. Extend this set if a new verb
// is needed.
var knownMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

// isControlRune reports whether r is an ASCII control character (C0 or DEL). Used to
// keep untrusted config strings from injecting line breaks into generated artifacts.
func isControlRune(r rune) bool { return r < 0x20 || r == 0x7f }

// ValidateHost checks that an egress-rule host is a plausible hostname or glob: no
// control characters or whitespace, no scheme or path, and either `*`, a `*.suffix`
// wildcard, or a dotted hostname of label characters. It is exported so the policy
// compiler can apply the same check to generator-emitted rules, which bypass the
// config loader's validation (AC-0058 / F18).
func ValidateHost(host string) error {
	if strings.IndexFunc(host, isControlRune) >= 0 {
		return fmt.Errorf("contains a control character")
	}
	if strings.ContainsAny(host, " \t/") || strings.Contains(host, "://") {
		return fmt.Errorf("is not a bare hostname (no scheme, path, or whitespace)")
	}
	body := host
	if body == "*" {
		return nil
	}
	body = strings.TrimPrefix(body, "*.")
	if body == "" {
		return fmt.Errorf("has an empty hostname")
	}
	for _, label := range strings.Split(body, ".") {
		if label == "" {
			return fmt.Errorf("has an empty label")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return fmt.Errorf("has an invalid character %q", r)
			}
		}
	}
	return nil
}

// ValidateMethods checks that every HTTP method in an egress rule is uppercase and a
// known verb. Exported for the policy compiler to reuse on generator rules (F18).
func ValidateMethods(methods []string) error {
	for _, m := range methods {
		if m == "" {
			return fmt.Errorf("has an empty method")
		}
		if !knownMethods[m] {
			if strings.ToUpper(m) == m {
				return fmt.Errorf("has an unknown method %q", m)
			}
			return fmt.Errorf("has a non-uppercase method %q (the enforcer match is case-sensitive)", m)
		}
	}
	return nil
}

// validateRuleHostMethods records host and method problems for one rule onto verr.
func validateRuleHostMethods(r Rule, list, ref string, verr *ValidationError) {
	if err := ValidateHost(r.Host); err != nil {
		verr.add("egress %s rule %s has an invalid host %q: %v (valid form: a bare hostname like example.com, or a wildcard like *.example.com)", list, ref, r.Host, err)
	}
	if r.Methods != nil {
		if err := ValidateMethods(*r.Methods); err != nil {
			verr.add("egress %s rule %s %v", list, ref, err)
		}
	}
}
