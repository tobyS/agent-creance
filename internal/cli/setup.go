package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/claudeimport"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/setup"
	"github.com/tobyS/agent-creance/internal/setupcheck"
)

// newSetupCmd implements `agent-creance setup` — the one-time onboarding command
// that trusts the mitmproxy CA (with a live verification), installs the
// agent-creance Claude Code skill, and scaffolds the global config's Claude
// egress baseline when no global config exists yet. It is thin orchestration
// over the already-tested internal/setup.Installer plus a pure-filesystem
// scaffold; --no-skill / --no-ca-install / --no-global-config opt out of each
// part. (docs/design.md "Commands".)
func newSetupCmd(app *App) *cobra.Command {
	var noSkill, noCAInstall, noGlobalConfig bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Trust the mitmproxy CA and install the agent-creance skill",
		Long: "Prepare this machine to run caged agents — a one-time, per-machine step. setup\n" +
			"trusts the mitmproxy CA in the keychain (verified with a live curl test), installs\n" +
			"the agent-creance Claude Code skill into ~/.claude/skills/, and scaffolds the global\n" +
			"config (~/.config/agent-creance.yaml) with the Claude Code egress baseline when no\n" +
			"global config exists. An existing global config is never touched. CA trust needs one\n" +
			"keychain/sudo prompt. After setup, run `agent-creance init` in each project.",
		Example: "  # Trust the CA, install the skill, scaffold the global config\n" +
			"  agent-creance setup\n" +
			"\n" +
			"  # Skip the skill install\n" +
			"  agent-creance setup --no-skill\n" +
			"\n" +
			"  # Use the CA via env vars only, without trusting it system-wide\n" +
			"  agent-creance setup --no-ca-install",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := runSetup(cmd.Context(), app, noSkill, noCAInstall, noGlobalConfig); err != nil {
				return err
			}
			// Orient the user toward the project-once step. This lives in the
			// command closure, not runSetup, because init reuses runSetup as its
			// host-setup gate and must not print "run init" mid-init.
			fmt.Fprintln(app.Stdout, "Next: run `agent-creance init` in your project.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&noSkill, "no-skill", false,
		"skip installing the agent-creance Claude Code skill")
	cmd.Flags().BoolVar(&noCAInstall, "no-ca-install", false,
		"don't trust the CA system-wide; rely on CA-bundle env vars only (reduced tool coverage)")
	cmd.Flags().BoolVar(&noGlobalConfig, "no-global-config", false,
		"skip writing the global config's Claude Code egress baseline")
	return cmd
}

// runSetup is the testable body of the setup command: it constructs the Installer
// from the App seams and drives the CA and skill steps per the opt-out flags.
// Taking the flags as parameters (rather than reading globals) is what lets the
// unit tests exercise every combination directly against the sysdep fakes.
func runSetup(ctx context.Context, app *App, noSkill, noCAInstall, noGlobalConfig bool) error {
	inst := setup.NewInstaller(
		app.FS, app.Keychain, app.ProcessManager, app.PortAllocator,
		app.TLSProber, app.Sleeper, app.Paths,
	)

	// CA step.
	if noCAInstall {
		// env-var-only trust: ensure the PEM exists (so the cage's SSL_CERT_FILE
		// &c. resolve), but don't change system trust and don't verify.
		if _, err := inst.EnsureCA(ctx); err != nil {
			return fmt.Errorf("ensure CA: %w", err)
		}
		fmt.Fprintln(app.Stdout, caCaveat)
	} else {
		fmt.Fprintln(app.Stdout, "Checking whether the mitmproxy CA is already trusted…")
		// Bootstrap verifies trust first and only installs (popping the macOS
		// authorization dialog) when needed; the beforeInstall hook lets us explain
		// that prompt right before it appears, never on the already-trusted path.
		res, err := inst.Bootstrap(ctx, func() {
			fmt.Fprintln(app.Stdout, msgPrePrompt)
		})
		if err != nil {
			return err // carries the actionable Message; Main → exit 1
		}
		if res.AlreadyTrusted {
			fmt.Fprintln(app.Stdout, app.OutStyle.OK("✓")+" mitmproxy CA already trusted — skipped the keychain prompt.")
		} else {
			fmt.Fprintln(app.Stdout, app.OutStyle.OK("✓")+" CA installed and verified.")
		}
		fmt.Fprintln(app.Stdout, keychainNote())
	}

	// Skill step.
	if noSkill {
		fmt.Fprintln(app.Stdout, "Skipping skill install (--no-skill).")
	} else {
		if err := inst.InstallSkill(); err != nil {
			return fmt.Errorf("install skill: %w", err)
		}
		fmt.Fprintln(app.Stdout, app.OutStyle.OK("✓")+" Skill installed.")
	}

	// Global config baseline: scaffold ~/.config/agent-creance.yaml so a fresh
	// cage lets Claude Code reach its own API (AC-0043). Never touches an
	// existing file — it is the user's to edit; `allow --global` appends to it.
	if noGlobalConfig {
		fmt.Fprintln(app.Stdout, "Skipping global config baseline (--no-global-config).")
		return nil
	}
	return scaffoldGlobalConfig(app)
}

// scaffoldGlobalConfig writes the Claude Code egress baseline to the global
// config path when no global config exists. The path comes from the same
// config.Loader resolution the policy compiler reads, so writer and reader can
// never disagree. An existing file — whatever its content — is left untouched.
func scaffoldGlobalConfig(app *App) error {
	path, err := config.NewLoader(app.FS, app.Paths).GlobalPath()
	if err != nil {
		return fmt.Errorf("resolve global config path: %w", err)
	}
	switch _, err := app.FS.Stat(path); {
	case err == nil:
		fmt.Fprintf(app.Stdout, "Global config %s already exists — left untouched.\n", path)
		return nil
	case errors.Is(err, fs.ErrNotExist):
		// fresh — fall through to write
	default:
		return fmt.Errorf("stat global config: %w", err)
	}
	if err := app.FS.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create global config dir: %w", err)
	}

	// Seed the fresh baseline from the user's global Claude Code config: allowed
	// web domains (GET-only intercept) and MCP servers (remote → passthrough,
	// localhost → port). Spliced into the static baseline so its comments survive.
	content := []byte(globalConfigTemplate)
	res, warns := claudeimport.Global(app.FS, app.Paths)
	printWarnings(app, warns)
	frag := &config.Config{}
	frag.Network.Egress.Allow = append(append([]config.Rule{}, res.WebRules...), res.MCPRules...)
	frag.Network.HostServices = res.Ports
	seeded, changed, err := applyFragment(content, frag)
	if err != nil {
		return fmt.Errorf("seed global config from Claude Code config: %w", err)
	}
	content = seeded

	if err := writeFileAtomic(app.FS, path, content, configFilePerm); err != nil {
		return fmt.Errorf("write global config: %w", err)
	}
	if changed {
		fmt.Fprintf(app.Stdout, "%s Wrote %s (Claude Code egress baseline, seeded from your global Claude Code config).\n", app.OutStyle.OK("✓"), path)
	} else {
		fmt.Fprintf(app.Stdout, "%s Wrote %s (Claude Code egress baseline).\n", app.OutStyle.OK("✓"), path)
	}
	return nil
}

// globalConfigTemplate is the scaffolded global baseline: the hosts Claude Code
// officially requires (https://code.claude.com/docs/en/network-config) plus
// Anthropic's public documentation hosts so a caged agent can read the docs out
// of the box (AC-0048). Hosts whose traffic carries OAuth tokens are passthrough
// per the design's credential-privacy rationale (docs/design.md "passthrough");
// the docs hosts are GET-only intercept (public, read-only); optional telemetry
// hosts ship commented out so blocking them stays a visible choice. Must parse
// under config.Parse (strict keys; passthrough forbids paths/methods) — pinned by
// TestSetupScaffoldsGlobalConfig.
const globalConfigTemplate = `# Global agent-creance configuration. Merged beneath every project's
# .agent-creance.yaml; rules here apply to all cages on this machine.
# Scaffolded by ` + "`agent-creance setup`" + ` — edit freely, setup never overwrites
# an existing file.
network:
  egress:
    allow:
      # Claude Code essentials (https://code.claude.com/docs/en/network-config).
      # API and OAuth traffic carries tokens, so it is tunneled raw
      # (passthrough) — the proxy never sees those bytes.
      - host: api.anthropic.com
        mode: passthrough
      - host: claude.ai
        mode: passthrough
      - host: platform.claude.com
        mode: passthrough
      # Plugin and native-updater downloads.
      - host: downloads.claude.ai
      # Release-notes feed and plugin marketplace metadata.
      - host: raw.githubusercontent.com
      # Anthropic's public documentation (credential-free, read-only). Scoped to
      # GET so the cage opens these hosts for reading docs only, never writes.
      # code.claude.com serves the Claude Code + Agent SDK docs under /docs;
      # docs.anthropic.com and docs.claude.com are legacy hosts that redirect to
      # platform.claude.com (allowed above), so they are host-wide GET.
      - host: code.claude.com
        mode: intercept
        paths: ["/docs/"]
        methods: [GET]
      - host: docs.anthropic.com
        mode: intercept
        methods: [GET]
      - host: docs.claude.com
        mode: intercept
        methods: [GET]
      # Optional telemetry — Claude Code works without it (requests are
      # soft-denied and only metrics/error reporting degrade). Uncomment
      # to allow:
      # - host: statsig.anthropic.com
      # - host: statsig.com
      # - host: sentry.io
`

// caCaveat is the honest coverage notice printed under --no-ca-install. The cage
// injects SSL_CERT_FILE/NODE_EXTRA_CA_CERTS/REQUESTS_CA_BUNDLE/GIT_SSL_CAINFO at
// the mitmproxy CA (internal/cage/cage.go), which covers curl/Node/Python/git;
// Go-on-macOS trusts the CA only via the keychain, so it is the documented gap.
const caCaveat = `Skipping system trust install (--no-ca-install).

The mitmproxy CA is provided to caged tools via environment variables only
(SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, GIT_SSL_CAINFO). This
covers curl, Node / Claude Code, Python, and git.

NOT covered: Go-based tools on macOS (for example the GitHub CLI, ` + "`gh`" + `) trust the
CA only via the system keychain, so they will fail TLS inside the cage. Re-run
` + "`agent-creance setup`" + ` without --no-ca-install to trust them too.`

// msgPrePrompt is printed immediately before the keychain authorization dialog
// (the install path only), so the generic OS "security" prompt reads as expected
// rather than alarming. It mirrors the honest, concrete tone of caCaveat.
const msgPrePrompt = `agent-creance needs to trust the mitmproxy CA it uses to filter the cage's
network egress. macOS will now show an authorization dialog (titled "security")
asking you to allow a trusted-certificate change — that is agent-creance adding
its egress-proxy CA to your login keychain. Approve it with Touch ID or your
password to continue.`

// keychainNote tells the user what was installed and where — printed on both the
// install and already-trusted paths so the cert is never a mystery in Keychain
// Access. setupcheck.CACommonName keeps the cert name in sync with the
// run/setupcheck presence probe.
func keychainNote() string {
	return fmt.Sprintf(`The %q certificate is trusted in your login keychain. To inspect or remove it,
open Keychain Access (login keychain → Certificates) and search for %q, or run:
  security delete-certificate -c %q ~/Library/Keychains/login.keychain-db`,
		setupcheck.CACommonName, setupcheck.CACommonName, setupcheck.CACommonName)
}
