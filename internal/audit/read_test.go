package audit_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/audit"
	"github.com/tobyS/agent-creance/internal/style"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestSummarizeFiles(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl")
	rot := cur + ".1"
	writeFile(t, rot, rotatedFixture)
	writeFile(t, cur, currentFixture)

	s, err := audit.SummarizeFiles(rot, cur)
	require.NoError(t, err)
	require.Equal(t, audit.Summary{
		Total: 6, Allow: 3, SoftDeny: 1, HardDeny: 2,
		Intercepted: 4, Passthrough: 2, Malformed: 1,
	}, s)
}

func TestSummarizeFilesNoRotated(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl")
	rot := cur + ".1"
	writeFile(t, cur, currentFixture) // only current; no .1

	s, err := audit.SummarizeFiles(rot, cur)
	require.NoError(t, err)
	// currentFixture has 3 valid entries (t4 soft-deny, t5 allow, t6 passthrough) + 1 malformed.
	require.Equal(t, 3, s.Total)
	require.Equal(t, 1, s.Malformed)
}

func TestSummarizeFilesMissingBoth(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl")
	s, err := audit.SummarizeFiles(cur+".1", cur)
	require.NoError(t, err)
	require.Equal(t, audit.Summary{}, s)
}

func TestDumpReadsRotatedThenCurrent(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl")
	rot := cur + ".1"
	writeFile(t, rot, `{"ts":"t1","method":"GET","url":"https://a/","decision":"allow","rule":{"list":"allow_always","index":0},"status":200}`+"\n")
	writeFile(t, cur, `{"ts":"t2","host":"api.anthropic.com","decision":"allow"}`+"\n")

	var buf bytes.Buffer
	require.NoError(t, audit.Dump(&buf, rot, cur, style.Plain()))

	want := "t1  allow      GET https://a/ -> 200\n" +
		"t2  allow      api.anthropic.com (passthrough)\n"
	require.Equal(t, want, buf.String())
}

func TestDumpMissingFilesIsEmpty(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "egress.jsonl")
	var buf bytes.Buffer
	require.NoError(t, audit.Dump(&buf, cur+".1", cur, style.Plain()))
	require.Empty(t, buf.String())
}
