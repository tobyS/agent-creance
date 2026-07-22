package cli

// credential_minted.go adds the two minted-credential registration subcommands
// (AC-0069a): `credential add-github-app` and `credential add-oauth2`. They write the
// same credentials: block as `credential add`, but with a github_app: / oauth2:
// sub-block instead of a static source, and reuse the recompiling edit pipeline so a
// running cage hot-reloads the *shape* (the token itself is minted at spawn, so a new
// minted credential needs a respawn to take effect — documented in the command help).

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// credentialAddGitHubAppOpts carries the resolved flags for `credential add-github-app`.
type credentialAddGitHubAppOpts struct {
	key      string
	clientID string
	repo     string
	perms    map[string]string
	bearer   bool
	basic    bool
	username string
	global   bool
}

func newCredentialAddGitHubAppCmd(app *App) *cobra.Command {
	var opts credentialAddGitHubAppOpts
	cmd := &cobra.Command{
		Use:   "add-github-app NAME",
		Short: "Register a GitHub App credential (host-side minted, ≤1h installation token)",
		Long: "Register a credential named NAME that mints a repo-scoped GitHub App installation\n" +
			"token host-side: the host signs a JWT with the app private key (--key, a secret\n" +
			"reference, never delivered to the cage) and exchanges it for an installation token that\n" +
			"lives ≤1h and is refreshed before expiry. --repo scopes it to one owner/name repo;\n" +
			"repeated --perm k=v cap its permissions (a down-scope, e.g. contents=read). The shape\n" +
			"defaults to Bearer; use --basic --username x-access-token for git-over-HTTPS. The policy\n" +
			"is recompiled so a running cage hot-reloads the shape, but the token is minted at cage\n" +
			"start, so a newly-added minted credential needs the cage to restart to take effect.",
		Example: "  # An installation token for one repo, Contents:read + Issues:write, as Bearer\n" +
			"  agent-creance credential add-github-app gh-app \\\n" +
			"    --key keychain://agent-creance/ghapp-key --client-id Iv1.0123456789abcdef \\\n" +
			"    --repo tobyS/agent-creance --perm contents=read --perm issues=write\n" +
			"\n" +
			"  # Same, shaped for git-over-HTTPS (Basic x-access-token:<token>)\n" +
			"  agent-creance credential add-github-app gh-git \\\n" +
			"    --key keychain://agent-creance/ghapp-key --client-id Iv1.0123456789abcdef \\\n" +
			"    --repo tobyS/agent-creance --perm contents=read --basic --username x-access-token",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCredentialAddGitHubApp(cmd.Context(), app, ".", args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.key, "key", "", "secret reference to the PKCS#1 PEM app private key (op:// / keychain:// / env://)")
	f.StringVar(&opts.clientID, "client-id", "", "the GitHub App client ID (non-secret)")
	f.StringVar(&opts.repo, "repo", "", "the single owner/name repository to scope the token to")
	f.StringToStringVar(&opts.perms, "perm", nil, "a permission cap, repeatable: --perm contents=read --perm issues=write")
	f.BoolVar(&opts.bearer, "bearer", false, "inject as 'Bearer <token>' (the default shape)")
	f.BoolVar(&opts.basic, "basic", false, "inject as HTTP Basic base64(<username>:<token>) for git-over-HTTPS (needs --username)")
	f.StringVar(&opts.username, "username", "", "username sentinel for --basic (e.g. x-access-token)")
	f.BoolVar(&opts.global, "global", false, "edit ~/.config/agent-creance.yaml instead of the project config")
	return cmd
}

func runCredentialAddGitHubApp(ctx context.Context, app *App, dir, name string, opts credentialAddGitHubAppOpts) error {
	if opts.basic && opts.bearer {
		return errors.New("pick only one header shape: --bearer or --basic")
	}
	if opts.basic && opts.username == "" {
		return errors.New("--basic needs --username (the git-over-HTTPS sentinel, e.g. x-access-token)")
	}
	if opts.key == "" {
		return errors.New("--key is required (a secret reference to the app private key)")
	}
	if err := sysdep.ValidateSecretRefSyntax(opts.key); err != nil {
		return fmt.Errorf("invalid --key %q: want an op:// , keychain:// , or env:// reference", opts.key)
	}
	if opts.clientID == "" {
		return errors.New("--client-id is required")
	}
	if opts.repo == "" {
		return errors.New("--repo is required (owner/name)")
	}

	template := "Bearer {token}"
	if opts.basic {
		template = "Basic base64({user}:{token})"
	}
	cred := config.Credential{
		Template: template,
		Username: opts.username,
		GitHubApp: &config.GitHubAppMint{
			Key:         opts.key,
			ClientID:    opts.clientID,
			Repo:        opts.repo,
			Permissions: opts.perms,
		},
	}

	path, label, err := mutationTarget(app, dir, false /*once*/, opts.global)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("credential %q", name)
	return applyAndRecompile(ctx, app, dir, path, label, subject, "added",
		func(src []byte) ([]byte, bool, error) { return config.AppendCredential(src, name, cred) })
}

// credentialAddOAuth2Opts carries the resolved flags for `credential add-oauth2`.
type credentialAddOAuth2Opts struct {
	refreshToken  string
	clientID      string
	tokenEndpoint string
	scopes        []string
	global        bool
}

func newCredentialAddOAuth2Cmd(app *App) *cobra.Command {
	var opts credentialAddOAuth2Opts
	cmd := &cobra.Command{
		Use:   "add-oauth2 NAME",
		Short: "Register an OAuth2 credential (host-side minted, refreshed access token)",
		Long: "Register a credential named NAME that mints a short-lived OAuth2 access token host-side\n" +
			"from a stored refresh token (--refresh-token, a secret reference — typically a\n" +
			"keychain:// item written by 'credential authorize NAME'). It defaults to Google Drive\n" +
			"(the drive.file scope); override with --token-endpoint and repeated --scope. Registering\n" +
			"before authorizing is allowed — the cage-start check flags an unauthorized credential\n" +
			"and requests needing it are refused (472) until you authorize.",
		Example: "  # A Google Drive credential (drive.file), authorized separately\n" +
			"  agent-creance credential add-oauth2 drive \\\n" +
			"    --refresh-token keychain://agent-creance/drive-refresh \\\n" +
			"    --client-id 1234.apps.googleusercontent.com\n" +
			"  agent-creance credential authorize drive",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCredentialAddOAuth2(cmd.Context(), app, ".", args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.refreshToken, "refresh-token", "", "secret reference to the stored refresh token (op:// / keychain:// / env://)")
	f.StringVar(&opts.clientID, "client-id", "", "the OAuth2 client ID (a desktop-app client ID is not confidential)")
	f.StringVar(&opts.tokenEndpoint, "token-endpoint", "", "the token endpoint (default: Google, "+config.DefaultOAuth2TokenEndpoint+")")
	f.StringArrayVar(&opts.scopes, "scope", nil, "a requested scope, repeatable (default: Google Drive drive.file)")
	f.BoolVar(&opts.global, "global", false, "edit ~/.config/agent-creance.yaml instead of the project config")
	return cmd
}

func runCredentialAddOAuth2(ctx context.Context, app *App, dir, name string, opts credentialAddOAuth2Opts) error {
	if opts.refreshToken == "" {
		return errors.New("--refresh-token is required (a secret reference; 'credential authorize' can populate a keychain:// item)")
	}
	if err := sysdep.ValidateSecretRefSyntax(opts.refreshToken); err != nil {
		return fmt.Errorf("invalid --refresh-token %q: want an op:// , keychain:// , or env:// reference", opts.refreshToken)
	}
	if opts.clientID == "" {
		return errors.New("--client-id is required")
	}

	cred := config.Credential{
		Template: "Bearer {token}",
		OAuth2: &config.OAuth2Mint{
			RefreshToken:  opts.refreshToken,
			ClientID:      opts.clientID,
			TokenEndpoint: opts.tokenEndpoint, // empty → Google default (applyDefaults)
			Scopes:        opts.scopes,        // empty → drive.file default (applyDefaults)
		},
	}

	path, label, err := mutationTarget(app, dir, false /*once*/, opts.global)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("credential %q", name)
	return applyAndRecompile(ctx, app, dir, path, label, subject, "added",
		func(src []byte) ([]byte, bool, error) { return config.AppendCredential(src, name, cred) })
}
