package cli

// init_gitremotes.go turns the project's own configured git remotes into static
// allow entries for the generated config (AC-0055): the repo host (clone/fetch/push),
// the repo-scoped forge API host, and the forge content/CDN companion hosts — reusing
// generator.RepositoryRules. Whether push is permitted is the caller's decision
// (allowPush); when it is not, a deny_always on the git-receive-pack push endpoint is
// emitted so read-only is actually enforced (the broad repo allow alone would permit
// push, and method scoping can't separate push from fetch — both are POST).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/gitremote"
)

// gitRemoteResult bundles the rules and operator-facing notes derived from the
// project's git remotes.
type gitRemoteResult struct {
	Allow []config.Rule // repo/API/content allow entries
	Deny  []config.Rule // read-only push blocks (empty when push is granted)
	Notes []string      // caveats to print (SSH transport, uninferable companions, …)
}

// gatherGitRemoteRules detects the project's git remotes and expands each into allow
// (and, unless allowPush, deny) rules. No remotes yields a zero result and no error.
// Rules are deduplicated by host+path so multiple remotes sharing a host (origin +
// upstream, fork + upstream) coexist without collapsing distinct repos.
func gatherGitRemoteRules(app *App, dir string, allowPush bool) (gitRemoteResult, error) {
	remotes, err := gitremote.Detect(app.FS, dir)
	if err != nil {
		return gitRemoteResult{}, fmt.Errorf("read git remotes: %w", err)
	}
	if len(remotes) == 0 {
		return gitRemoteResult{}, nil
	}

	var (
		res       gitRemoteResult
		allowSeen = map[string]bool{}
		denySeen  = map[string]bool{}
		sshNoted  bool
	)
	addAllow := func(r config.Rule) {
		if key := ruleKey(r); !allowSeen[key] {
			allowSeen[key] = true
			res.Allow = append(res.Allow, r)
		}
	}
	addDeny := func(r config.Rule) {
		if key := ruleKey(r); !denySeen[key] {
			denySeen[key] = true
			res.Deny = append(res.Deny, r)
		}
	}

	for _, rm := range remotes {
		host, org, repo, ok := generator.NormalizeRepoURL(rm.URL)
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf("remote %q (%s): couldn't parse host/org/repo — skipped.", rm.Name, rm.URL))
			continue
		}
		if !isHTTPSRemote(rm.URL) && !sshNoted {
			sshNoted = true
			res.Notes = append(res.Notes, "a remote uses a non-HTTPS transport (e.g. SSH); added its HTTPS forge hosts, but git-over-SSH transport itself is unsupported — switch the remote to HTTPS to use it in the cage.")
		}
		if !generator.IsKnownForge(host) {
			res.Notes = append(res.Notes, fmt.Sprintf("remote %q host %q is not a known forge — added a bare repo-host allow; API/content companion hosts couldn't be inferred.", rm.Name, host))
		}

		reason := fmt.Sprintf("project git remote (%s)", rm.Name)
		for _, g := range generator.RepositoryRules(rm.URL, "") {
			r := config.Rule{Host: g.Rule.Host, Reason: reason}
			switch {
			case g.Rule.Host == host:
				// The repo web host serves git smart-HTTP. Segment matching is
				// literal, so cover both the bare and ".git" path forms (a remote
				// stored with .git requests /<org>/<repo>.git/…).
				r.Paths = pathsPtr(repoPath(org, repo), repoGitPath(org, repo))
			case len(g.Rule.Paths) > 0:
				p := append([]string(nil), g.Rule.Paths...)
				r.Paths = &p
			}
			addAllow(r)
		}

		if !allowPush {
			addDeny(config.Rule{
				Host:   host,
				Paths:  pathsPtr(repoPath(org, repo)+"git-receive-pack", repoGitPath(org, repo)+"git-receive-pack"),
				Reason: fmt.Sprintf("read-only: push (git-receive-pack) to %s blocked; re-run init with --git-push to allow", rm.Name),
			})
		}
	}
	return res, nil
}

// repoPath is the bare repo path prefix, e.g. "/facebook/react/".
func repoPath(org, repo string) string { return "/" + org + "/" + repo + "/" }

// repoGitPath is the ".git" repo path prefix, e.g. "/facebook/react.git/", the form
// git uses when the remote URL carries a .git suffix.
func repoGitPath(org, repo string) string { return "/" + org + "/" + repo + ".git/" }

// pathsPtr returns a *[]string for a config.Rule, omitting empty entries.
func pathsPtr(paths ...string) *[]string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

// ruleKey is a host+sorted-paths identity for deduping git-remote rules (mode/reason
// are intentionally excluded).
func ruleKey(r config.Rule) string {
	var paths []string
	if r.Paths != nil {
		paths = append(paths, *r.Paths...)
		sort.Strings(paths)
	}
	return r.Host + "\x00" + strings.Join(paths, "\x00")
}

// isHTTPSRemote reports whether url is an HTTPS remote (the only transport the cage
// supports). scp-like (git@host:…), ssh://, git:// and http:// are not.
func isHTTPSRemote(url string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "https://")
}
