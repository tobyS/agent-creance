// Package gitremote reads a project's configured git remotes from its .git/config,
// so init can auto-allowlist the project's own repository hosts (AC-0055). It is a
// best-effort parser of the git-config INI subset, read through a sysdep.FileSystem
// rather than by invoking git — keeping init free of external tools and new OS seams.
//
// Only the remote URLs are needed: each `[remote "<name>"]` section's `url` value is
// returned verbatim (in file order, deduped by name). Translating a URL into forge
// allow rules is the caller's job (it reuses generator.RepositoryRules). A missing or
// unreadable .git/config (including the common worktree/submodule case where .git is a
// gitdir-pointer file, not a directory) yields no remotes and no error.
package gitremote

import (
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Remote is a single configured git remote: its name (the subsection name, e.g.
// "origin") and its url, kept verbatim as written in .git/config.
type Remote struct {
	Name string
	URL  string
}

// sectionSub matches a git-config section header with a quoted subsection,
// e.g. [remote "origin"]. Section names are case-insensitive; the quoted
// subsection (the remote name) is case-sensitive and kept verbatim. A header
// without a subsection (e.g. [core]) simply doesn't match, leaving cur empty.
var sectionSub = regexp.MustCompile(`^\[\s*([A-Za-z0-9.-]+)\s+"([^"]*)"\s*\]$`)

// Detect reads <dir>/.git/config and returns the project's configured remotes in
// file order (deduped by name; a repeated url for a name keeps the last, mirroring
// git's single-valued url). An absent or unreadable config is not an error — it
// returns (nil, nil). A genuine read failure (e.g. permissions) is surfaced.
func Detect(fsys sysdep.FileSystem, dir string) ([]Remote, error) {
	data, err := fsys.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return parseConfig(string(data)), nil
}

// parseConfig walks the git-config text, capturing each remote's url. It tracks the
// current section: a `url = …` line is recorded only while inside a `[remote "name"]`
// section. Full-line comments (# or ;) and blank lines are skipped; key names are
// matched case-insensitively (git treats them so).
func parseConfig(text string) []Remote {
	var (
		order []string
		urls  = map[string]string{}
		cur   string // current remote name, "" when not in a [remote "…"] section
	)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			cur = ""
			if m := sectionSub.FindStringSubmatch(line); m != nil && strings.EqualFold(m[1], "remote") {
				cur = m[2]
			}
			continue
		}
		if cur == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		url := strings.TrimSpace(val)
		if url == "" {
			continue
		}
		if _, seen := urls[cur]; !seen {
			order = append(order, cur)
		}
		urls[cur] = url
	}

	if len(order) == 0 {
		return nil
	}
	remotes := make([]Remote, 0, len(order))
	for _, name := range order {
		remotes = append(remotes, Remote{Name: name, URL: urls[name]})
	}
	return remotes
}
