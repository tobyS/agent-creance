// Package portscan statically infers the local development ports a project's
// services listen on, so init can pre-fill network.host_services. It is a
// best-effort, conservative heuristic over a project's own files — read through a
// sysdep.FileSystem, never the OS directly — and the engineer reviews the result
// before it is written.
//
// Sources, in descending confidence (a port found by an earlier source wins):
//   - docker-compose.yml / .yaml: explicitly published host ports.
//   - package.json scripts: an explicit --port/-p/PORT= in a dev/start/serve
//     script, else the well-known default of the dev tool the script invokes.
//   - Procfile: an explicit port flag on a process command.
//   - .env / .env.local: PORT / *_PORT numeric values.
//
// These are third-party / hand-authored files, so parsing is lenient and a single
// malformed file yields a warning rather than aborting the scan.
package portscan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// frameworkDefaults maps a dev-tool name (as it appears in a package.json script)
// to the port it serves on by default. Conservative and explicit on purpose.
var frameworkDefaults = []struct {
	tool string
	port int
}{
	{"vite", 5173},
	{"next", 3000},
	{"react-scripts", 3000},
	{"vue-cli-service", 8080},
	{"ng", 4200},
	{"nuxt", 3000},
	{"astro", 4321},
	{"gatsby", 8000},
	{"remix", 3000},
	{"parcel", 1234},
	{"webpack-dev-server", 8080},
}

var (
	// portFlag matches an explicit port on a command line: --port 3000,
	// --port=3000, -p 3000, -p=3000, or PORT=3000.
	portFlag = regexp.MustCompile(`(?:--port[= ]|-p[= ]|PORT=)(\d{1,5})\b`)
	// envPort matches a PORT or *_PORT assignment in a .env line.
	envPort = regexp.MustCompile(`^([A-Za-z0-9_]*PORT)\s*=\s*"?(\d{1,5})"?\s*$`)
)

// Detect scans projectDir for likely development ports. It returns the detected
// host services (deduplicated by port, sorted by port) and any warnings from
// malformed source files. Absent files are not warnings.
func Detect(fsys sysdep.FileSystem, projectDir string) ([]config.HostService, []string) {
	var (
		warns []string
		acc   = newPortAcc()
	)
	scanCompose(fsys, projectDir, acc, &warns)
	scanPackageJSON(fsys, projectDir, acc, &warns)
	scanProcfile(fsys, projectDir, acc)
	scanEnv(fsys, projectDir, acc)
	return acc.services(), warns
}

// --- docker-compose ---

type composeFile struct {
	Services map[string]struct {
		Ports []yaml.Node `yaml:"ports"`
	} `yaml:"services"`
}

func scanCompose(fsys sysdep.FileSystem, dir string, acc *portAcc, warns *[]string) {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		data, ok := readFile(fsys, filepath.Join(dir, name), warns)
		if !ok {
			continue
		}
		var cf composeFile
		if err := yaml.Unmarshal(data, &cf); err != nil {
			*warns = append(*warns, fmt.Sprintf("parse %s: %v", name, err))
			continue
		}
		for _, svc := range sortedServiceNames(cf) {
			for _, node := range cf.Services[svc].Ports {
				if port, ok := composeHostPort(node); ok {
					acc.add(port, svc)
				}
			}
		}
	}
}

// composeHostPort extracts the published host port from one compose ports entry,
// in either short ("8080:80", "127.0.0.1:8080:80") or long ({published: 8080})
// form. A bare container port ("3000") publishes to a random host port and is
// skipped, as are ranges.
func composeHostPort(node yaml.Node) (int, bool) {
	var v any
	if err := node.Decode(&v); err != nil {
		return 0, false
	}
	switch t := v.(type) {
	case string:
		return shortHostPort(t)
	case map[string]any:
		if pub, ok := t["published"]; ok {
			return parsePort(fmt.Sprint(pub))
		}
	}
	return 0, false
}

func shortHostPort(s string) (int, bool) {
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 2: // HOST:CONTAINER
		return parsePort(parts[0])
	case 3: // IP:HOST:CONTAINER
		return parsePort(parts[1])
	default: // bare container port or unsupported form
		return 0, false
	}
}

func sortedServiceNames(cf composeFile) []string {
	out := make([]string, 0, len(cf.Services))
	for name := range cf.Services {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// --- package.json ---

func scanPackageJSON(fsys sysdep.FileSystem, dir string, acc *portAcc, warns *[]string) {
	data, ok := readFile(fsys, filepath.Join(dir, "package.json"), warns)
	if !ok {
		return
	}
	var doc struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		*warns = append(*warns, fmt.Sprintf("parse package.json: %v", err))
		return
	}
	for _, key := range []string{"dev", "start", "serve"} {
		script, ok := doc.Scripts[key]
		if !ok {
			continue
		}
		if m := portFlag.FindStringSubmatch(script); m != nil {
			if port, ok := parsePort(m[1]); ok {
				acc.add(port, key)
				continue
			}
		}
		for _, fw := range frameworkDefaults {
			if scriptUsesTool(script, fw.tool) {
				acc.add(fw.port, fw.tool)
				break
			}
		}
	}
}

// scriptUsesTool reports whether an npm script invokes the named tool as a
// whitespace-delimited word (so "next" does not match "nextfoo").
func scriptUsesTool(script, tool string) bool {
	for _, f := range strings.Fields(script) {
		if f == tool {
			return true
		}
	}
	return false
}

// --- Procfile ---

func scanProcfile(fsys sysdep.FileSystem, dir string, acc *portAcc) {
	data, ok := readFile(fsys, filepath.Join(dir, "Procfile"), nil)
	if !ok {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, cmd, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if m := portFlag.FindStringSubmatch(cmd); m != nil {
			if port, ok := parsePort(m[1]); ok {
				acc.add(port, strings.TrimSpace(name))
			}
		}
	}
}

// --- .env ---

func scanEnv(fsys sysdep.FileSystem, dir string, acc *portAcc) {
	for _, name := range []string{".env", ".env.local"} {
		data, ok := readFile(fsys, filepath.Join(dir, name), nil)
		if !ok {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if m := envPort.FindStringSubmatch(line); m != nil {
				if port, ok := parsePort(m[2]); ok {
					acc.add(port, strings.ToLower(m[1]))
				}
			}
		}
	}
}

// --- shared ---

// readFile reads a file, treating absence as "not present" (false, no warning).
// A genuine read error is recorded as a warning when warns is non-nil.
func readFile(fsys sysdep.FileSystem, path string, warns *[]string) ([]byte, bool) {
	data, err := fsys.ReadFile(path)
	if err != nil {
		if warns != nil && !errors.Is(err, fs.ErrNotExist) {
			*warns = append(*warns, fmt.Sprintf("read %s: %v", filepath.Base(path), err))
		}
		return nil, false
	}
	return data, true
}

func parsePort(s string) (int, bool) {
	s = strings.TrimSpace(s)
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// portAcc accumulates ports keyed on the port number (the meaningful identity;
// the label is cosmetic), keeping the first label seen so earlier, higher-
// confidence sources win.
type portAcc struct {
	labels map[int]string
	order  []int
}

func newPortAcc() *portAcc { return &portAcc{labels: map[int]string{}} }

func (a *portAcc) add(port int, label string) {
	if _, seen := a.labels[port]; seen {
		return
	}
	a.labels[port] = label
	a.order = append(a.order, port)
}

func (a *portAcc) services() []config.HostService {
	ports := append([]int(nil), a.order...)
	sort.Ints(ports)
	out := make([]config.HostService, 0, len(ports))
	for _, p := range ports {
		out = append(out, config.HostService{Label: a.labels[p], Port: p})
	}
	return out
}
