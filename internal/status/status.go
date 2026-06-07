// Package status orchestrates `agent-creance status`: it enumerates every
// project's out-of-tree state dir, reads each project's proxy.lock (via the same
// read-only proxy.Manager.Inspect doctor uses), and renders a deterministic table
// of the cages that are running, orphaned, or stranded. The Scanner is the
// side-effecting half; Render (report.go) is pure and golden-tested.
package status

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// Scanner reads the proxy state of every project. cli/status.go builds it from the
// App seams.
type Scanner struct {
	Manager  *proxy.Manager
	Resolver *state.Resolver
	FS       sysdep.FileSystem
}

// Scan enumerates <cache>/agent-creance/projects/*, inspects each project's
// proxy.lock, and returns one ProjectStatus per project that has recorded proxy
// state (LockPresent). Projects whose lock is absent, empty, or cleared are
// skipped — they are not running cages. A missing projects/ root (no project has
// ever run) yields an empty report, not an error. Each project is read under its
// own flock, so Scan never corrupts another project's state.
func (s *Scanner) Scan() (Report, error) {
	root, err := s.Resolver.ProjectsRoot()
	if err != nil {
		return Report{}, err
	}
	entries, err := s.FS.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Report{}, nil
		}
		return Report{}, err
	}

	var projects []ProjectStatus
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		layout := state.LayoutForRoot(filepath.Join(root, e.Name()))
		diag, err := s.Manager.Inspect(layout)
		if err != nil {
			return Report{}, err
		}
		if !diag.LockPresent {
			continue
		}
		projects = append(projects, ProjectStatus{Hash: layout.Hash, Diag: diag})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Hash < projects[j].Hash })
	return Report{Projects: projects}, nil
}
