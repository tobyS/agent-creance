package profile

import (
	"fmt"
	"path/filepath"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

const (
	dirPerm   = 0o755
	filePerm  = 0o644
	tmpSuffix = ".tmp"
)

// projectConfigName is the per-project config file at the project root.
const projectConfigName = ".agent-creance.yaml"

// Compiler resolves a project's effective config and writes its network.sb append
// fragment to the out-of-tree state directory. It holds only injected sysdep seams, so
// it is unit-tested hermetically against the fakes in sysdeptest. Unlike the policy
// compiler it keeps no cache: network.sb is a few lines of text, regenerated on every
// launch (the ephemeral proxy port is appended separately at launch time).
type Compiler struct {
	fs     sysdep.FileSystem
	loader *config.Loader
	state  *state.Resolver
}

// New wires a Compiler with the filesystem and path-resolution seams.
func New(fsys sysdep.FileSystem, paths sysdep.PathResolver) *Compiler {
	return &Compiler{
		fs:     fsys,
		loader: config.NewLoader(fsys, paths),
		state:  state.New(paths),
	}
}

// Result reports what Compile wrote.
type Result struct {
	// ProfilePath is the absolute out-of-tree path written (== layout.NetworkSB()).
	ProfilePath string
	// AllowCount is the number of host-service allow rules emitted (deduped by port).
	AllowCount int
}

// Compile resolves the effective config for projectDir and writes its network.sb
// fragment (deny-all baseline + per-host-service allows) atomically under the project's
// out-of-tree state root. It rewrites unconditionally — there is no input-hash cache.
func (c *Compiler) Compile(projectDir string) (Result, error) {
	layout, err := c.state.Resolve(projectDir)
	if err != nil {
		return Result{}, fmt.Errorf("profile: %w", err)
	}
	cfg, err := c.loader.Load(filepath.Join(projectDir, projectConfigName))
	if err != nil {
		return Result{}, fmt.Errorf("profile: %w", err)
	}

	body := RenderNetworkSB(cfg.Network.HostServices)
	if err := c.write(layout, body); err != nil {
		return Result{}, err
	}
	return Result{
		ProfilePath: layout.NetworkSB(),
		AllowCount:  len(dedupeByPort(cfg.Network.HostServices)),
	}, nil
}

// write writes the fragment atomically (temp file then rename) under the out-of-tree
// state directory, mirroring the policy compiler's write idiom. The only directory it
// creates is the state root — never anything inside the project tree (C4).
func (c *Compiler) write(layout state.Layout, body string) error {
	if err := c.fs.MkdirAll(layout.Root, dirPerm); err != nil {
		return fmt.Errorf("profile: create state dir: %w", err)
	}
	dest := layout.NetworkSB()
	tmp := dest + tmpSuffix
	if err := c.fs.WriteFile(tmp, []byte(body), filePerm); err != nil {
		return fmt.Errorf("profile: write network.sb: %w", err)
	}
	if err := c.fs.Rename(tmp, dest); err != nil {
		_ = c.fs.Remove(tmp)
		return fmt.Errorf("profile: finalize network.sb: %w", err)
	}
	return nil
}
