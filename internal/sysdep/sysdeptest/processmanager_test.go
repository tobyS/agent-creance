package sysdeptest

import (
	"context"
	"strings"
	"testing"
)

func TestFakeProcessManagerSpawnWithSecretRecordsPayloadAndArgs(t *testing.T) {
	pm := NewFakeProcessManager()
	pm.SpawnPID = 4242
	const secret = `{"gh":"tok3n"}`
	pid, err := pm.SpawnWithSecret(context.Background(), []byte(secret), "mitmdump", "--set", "creance_secret_fd=3")
	if err != nil {
		t.Fatalf("SpawnWithSecret: %v", err)
	}
	if pid != 4242 {
		t.Errorf("pid = %d, want 4242", pid)
	}
	if len(pm.Spawned) != 1 || pm.Spawned[0].Name != "mitmdump" {
		t.Fatalf("Spawned = %+v, want one mitmdump entry", pm.Spawned)
	}
	if len(pm.Secrets) != 1 || string(pm.Secrets[0]) != secret {
		t.Fatalf("Secrets = %v, want one entry %q", pm.Secrets, secret)
	}
	// Hygiene: the secret must never ride argv.
	for _, a := range pm.Spawned[0].Args {
		if strings.Contains(a, "tok3n") {
			t.Errorf("secret leaked into argv: %q", a)
		}
	}
}

func TestFakeProcessManagerSpawnWithSecretDefensiveCopy(t *testing.T) {
	pm := NewFakeProcessManager()
	buf := []byte("secret-bytes")
	if _, err := pm.SpawnWithSecret(context.Background(), buf, "mitmdump"); err != nil {
		t.Fatalf("SpawnWithSecret: %v", err)
	}
	// Mutating the caller's slice must not change what the fake recorded.
	buf[0] = 'X'
	if string(pm.Secrets[0]) != "secret-bytes" {
		t.Errorf("recorded secret was not defensively copied: %q", string(pm.Secrets[0]))
	}
}

func TestFakeProcessManagerSpawnWithSecretHonorsSpawnErr(t *testing.T) {
	pm := NewFakeProcessManager()
	pm.SpawnErr = context.Canceled
	if _, err := pm.SpawnWithSecret(context.Background(), []byte("x"), "mitmdump"); err == nil {
		t.Error("SpawnWithSecret ignored SpawnErr, want error")
	}
}
