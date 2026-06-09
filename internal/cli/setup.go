package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/setup"
	"github.com/tobyS/agent-creance/internal/setupcheck"
)

// newSetupCmd implements `agent-creance setup` — the one-time onboarding command
// that trusts the mitmproxy CA (with a live verification) and installs the
// agent-creance Claude Code skill. It is thin orchestration over the already-
// tested internal/setup.Installer; --no-skill / --no-ca-install opt out of each
// half. (docs/design.md "Commands".)
func newSetupCmd(app *App) *cobra.Command {
	var noSkill, noCAInstall bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Trust the mitmproxy CA and install the agent-creance skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd.Context(), app, noSkill, noCAInstall)
		},
	}
	cmd.Flags().BoolVar(&noSkill, "no-skill", false,
		"skip installing the agent-creance Claude Code skill")
	cmd.Flags().BoolVar(&noCAInstall, "no-ca-install", false,
		"don't trust the CA system-wide; rely on CA-bundle env vars only (reduced tool coverage)")
	return cmd
}

// runSetup is the testable body of the setup command: it constructs the Installer
// from the App seams and drives the CA and skill steps per the opt-out flags.
// Taking the flags as parameters (rather than reading globals) is what lets the
// unit tests exercise every combination directly against the sysdep fakes.
func runSetup(ctx context.Context, app *App, noSkill, noCAInstall bool) error {
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
			fmt.Fprintln(app.Stdout, "✓ mitmproxy CA already trusted — skipped the keychain prompt.")
		} else {
			fmt.Fprintln(app.Stdout, "✓ CA installed and verified.")
		}
		fmt.Fprintln(app.Stdout, keychainNote())
	}

	// Skill step.
	if noSkill {
		fmt.Fprintln(app.Stdout, "Skipping skill install (--no-skill).")
		return nil
	}
	if err := inst.InstallSkill(); err != nil {
		return fmt.Errorf("install skill: %w", err)
	}
	fmt.Fprintln(app.Stdout, "✓ Skill installed.")
	return nil
}

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
