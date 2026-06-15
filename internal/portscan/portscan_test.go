package portscan

import (
	"io/fs"
	"reflect"
	"testing"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const dir = "/proj"

func detect(files map[string]string) ([]config.HostService, []string) {
	fsys := sysdeptest.NewFakeFileSystem()
	for name, body := range files {
		fsys.Files[dir+"/"+name] = []byte(body)
	}
	return Detect(fsys, dir)
}

func TestDetectCompose(t *testing.T) {
	got, warns := detect(map[string]string{
		"docker-compose.yml": `
services:
  web:
    ports:
      - "8080:80"
  db:
    ports:
      - "127.0.0.1:5432:5432"
  cache:
    ports:
      - target: 6379
        published: 6380
  ephemeral:
    ports:
      - "3000"
`,
	})
	if len(warns) != 0 {
		t.Fatalf("warns: %v", warns)
	}
	want := []config.HostService{
		{Label: "web", Port: 8080},
		{Label: "cache", Port: 6380},
		{Label: "db", Port: 5432},
	}
	if !reflect.DeepEqual(sortByPort(got), sortByPort(want)) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDetectPackageJSON(t *testing.T) {
	tests := []struct {
		name string
		pkg  string
		want []config.HostService
	}{
		{
			name: "explicit port flag wins",
			pkg:  `{"scripts":{"dev":"vite --port 4000"}}`,
			want: []config.HostService{{Label: "dev", Port: 4000}},
		},
		{
			name: "framework default",
			pkg:  `{"scripts":{"dev":"vite"}}`,
			want: []config.HostService{{Label: "vite", Port: 5173}},
		},
		{
			name: "next default",
			pkg:  `{"scripts":{"start":"next start"}}`,
			want: []config.HostService{{Label: "next", Port: 3000}},
		},
		{
			name: "unknown tool yields nothing",
			pkg:  `{"scripts":{"dev":"node server.js"}}`,
			want: []config.HostService{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := detect(map[string]string{"package.json": tc.pkg})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectProcfile(t *testing.T) {
	got, _ := detect(map[string]string{"Procfile": "web: bundle exec puma -p 9292\nworker: rake jobs:work\n"})
	want := []config.HostService{{Label: "web", Port: 9292}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDetectEnv(t *testing.T) {
	got, _ := detect(map[string]string{".env": "PORT=8000\nDB_PORT=5433\nNAME=app\n# COMMENT_PORT=1\n"})
	want := []config.HostService{
		{Label: "db_port", Port: 5433},
		{Label: "port", Port: 8000},
	}
	if !reflect.DeepEqual(sortByPort(got), sortByPort(want)) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDetectDedupePrecedence(t *testing.T) {
	// Same port 3000 from compose (highest) and package.json — compose label wins.
	got, _ := detect(map[string]string{
		"docker-compose.yml": "services:\n  app:\n    ports:\n      - \"3000:3000\"\n",
		"package.json":       `{"scripts":{"dev":"next dev"}}`,
	})
	want := []config.HostService{{Label: "app", Port: 3000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (compose must win)", got, want)
	}
}

func TestDetectNoFiles(t *testing.T) {
	got, warns := detect(nil)
	if len(got) != 0 || len(warns) != 0 {
		t.Fatalf("expected empty, got services=%v warns=%v", got, warns)
	}
}

func TestDetectMalformedComposeWarns(t *testing.T) {
	_, warns := detect(map[string]string{"docker-compose.yml": ":\n  - not: valid: yaml: ["})
	if len(warns) == 0 {
		t.Fatalf("expected a warning for malformed compose")
	}
}

func TestDetectUnreadablePackageWarns(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Errs[dir+"/package.json"] = fs.ErrPermission
	_, warns := Detect(fsys, dir)
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning, got %v", warns)
	}
}

func sortByPort(s []config.HostService) []config.HostService {
	out := append([]config.HostService(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Port > out[j].Port; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
