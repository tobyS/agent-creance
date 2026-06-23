package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const importProjDir = "/proj"

type importFixture struct {
	app  *App
	fs   *sysdeptest.FakeFileSystem
	term *sysdeptest.FakeTerminal
	out  *bytes.Buffer
}

func newImportFixture(existing string) *importFixture {
	fs := sysdeptest.NewFakeFileSystem()
	if existing != "" {
		fs.Files[importProjDir+"/"+configFile] = []byte(existing)
	}
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = "/home/user"

	out := &bytes.Buffer{}
	term := &sysdeptest.FakeTerminal{}
	app := &App{
		Stdout:   out,
		Stderr:   &bytes.Buffer{},
		Stdin:    strings.NewReader(""),
		FS:       fs,
		Paths:    paths,
		Clock:    sysdeptest.NewFakeClock(time.Unix(0, 0)),
		HTTP:     sysdeptest.NewFakeHTTPGetter(),
		Terminal: term,
		Flock:    sysdeptest.NewFakeFlock(),
	}
	return &importFixture{app: app, fs: fs, term: term, out: out}
}

func (f *importFixture) writeFrag(body string) string {
	path := "/frag.yaml"
	f.fs.Files[path] = []byte(body)
	return path
}

func (f *importFixture) projectConfig(t *testing.T) string {
	t.Helper()
	b, ok := f.fs.Files[importProjDir+"/"+configFile]
	require.True(t, ok, "%s not written", configFile)
	return string(b)
}

const importBase = `network:
  host_services:
    - web:3000  # existing comment
  egress:
    allow:
      - host: seed.example
`

func TestImportMergesAllowAndPorts(t *testing.T) {
	f := newImportFixture(importBase)
	frag := f.writeFrag(`network:
  host_services:
    - api:8080
  egress:
    allow:
      - host: docs.example.com
        methods: [GET]
`)
	require.NoError(t, runImport(context.Background(), f.app, importProjDir, frag, true))

	cfg, err := config.Parse([]byte(f.projectConfig(t)))
	require.NoError(t, err)
	require.Equal(t, []config.HostService{{Label: "web", Port: 3000}, {Label: "api", Port: 8080}}, cfg.Network.HostServices)
	require.Len(t, cfg.Network.Egress.Allow, 2)
	require.Contains(t, f.projectConfig(t), "- web:3000  # existing comment", "comments preserved")
	require.Contains(t, f.out.String(), "policy recompiled")
}

func TestImportRejectsUnknownKey(t *testing.T) {
	f := newImportFixture(importBase)
	frag := f.writeFrag("network:\n  egress:\n    allwo:\n      - host: typo.example\n")
	err := runImport(context.Background(), f.app, importProjDir, frag, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid config fragment")
	// Existing config untouched.
	require.Equal(t, importBase, f.projectConfig(t))
}

func TestImportIdempotent(t *testing.T) {
	f := newImportFixture(importBase)
	frag := f.writeFrag("network:\n  egress:\n    allow:\n      - host: seed.example\n")
	require.NoError(t, runImport(context.Background(), f.app, importProjDir, frag, true))
	require.Contains(t, f.out.String(), "Nothing to import")
	require.Equal(t, importBase, f.projectConfig(t), "no-op must not rewrite")
}

func TestImportInteractiveDeclineAborts(t *testing.T) {
	f := newImportFixture(importBase)
	f.term.Interactive = true
	f.app.Stdin = strings.NewReader("n\n")
	frag := f.writeFrag("network:\n  egress:\n    allow:\n      - host: new.example\n")

	require.NoError(t, runImport(context.Background(), f.app, importProjDir, frag, false))
	require.Contains(t, f.out.String(), "Import cancelled")
	require.Equal(t, importBase, f.projectConfig(t), "declined import must not write")
}

func TestImportInteractiveConfirmWrites(t *testing.T) {
	f := newImportFixture(importBase)
	f.term.Interactive = true
	f.app.Stdin = strings.NewReader("y\n")
	frag := f.writeFrag("network:\n  egress:\n    allow:\n      - host: new.example\n")

	require.NoError(t, runImport(context.Background(), f.app, importProjDir, frag, false))
	cfg, err := config.Parse([]byte(f.projectConfig(t)))
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 2)
}

func TestImportNonInteractiveWithoutYesDoesNotWrite(t *testing.T) {
	f := newImportFixture(importBase)
	frag := f.writeFrag("network:\n  egress:\n    allow:\n      - host: new.example\n")

	require.NoError(t, runImport(context.Background(), f.app, importProjDir, frag, false))
	require.Contains(t, f.out.String(), "re-run with --yes")
	require.Equal(t, importBase, f.projectConfig(t))
}

func TestImportIgnoredSectionsNote(t *testing.T) {
	f := newImportFixture(importBase)
	frag := f.writeFrag(`agent:
  command: [claude]
network:
  egress:
    allow:
      - host: new.example
`)
	require.NoError(t, runImport(context.Background(), f.app, importProjDir, frag, true))
	require.Contains(t, f.out.String(), "ignored sections: agent")
	cfg, err := config.Parse([]byte(f.projectConfig(t)))
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 2, "allow still merged despite ignored agent section")
}

func TestImportMissingFragmentErrors(t *testing.T) {
	f := newImportFixture(importBase)
	err := runImport(context.Background(), f.app, importProjDir, "/nope.yaml", true)
	require.Error(t, err)
}
