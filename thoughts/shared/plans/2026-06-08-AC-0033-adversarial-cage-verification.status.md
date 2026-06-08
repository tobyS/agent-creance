# AC-0033 — implementation status

Plan: `thoughts/shared/plans/2026-06-08-AC-0033-adversarial-cage-verification.md`

- [x] Phase 1 — Matrix table + evaluator + coverage/drift guard (hermetic)
- [x] Phase 2 — Fake-agent payload script
- [x] Phase 3 — Live battery harness + negative control (integration)
- [ ] Phase 4 — Manual red-team checklist
- [ ] Phase 5 — Close out

## Log
- 2026-06-08: research + plan committed (ebdf5df, edb1915). Starting Phase 1.
- 2026-06-08: Phase 1 committed (1be0c83). Hermetic matrix/evaluator/drift guard green.
- 2026-06-08: Phase 2+3 committed (bf64b11). Live battery: all 16 vectors green from
  inside the real cage; negative control detects the raw-egress escape; stable -count=2.

## Findings surfaced by the harness (candidate follow-up tickets)
1. The injected CA env vars (SSL_CERT_FILE / NODE_EXTRA_CA_CERTS / REQUESTS_CA_BUNDLE /
   GIT_SSL_CAINFO) point at ~/.mitmproxy/mitmproxy-ca-cert.pem, which is NOT readable
   inside the cage (safehouse denies ~/.mitmproxy). So those belt-and-suspenders CA
   files are non-functional in-cage; CA trust must come via the keychain (`setup`). The
   battery works around it with an in-mount --cacert copy.
2. CLAUDE_CONFIG_DIR resolves under ~/.cache/agent-creance, but safehouse's base policy
   grants RW only to /tmp, $TMPDIR, and specific toolchain dirs — NOT a generic ~/.cache.
   The battery passes because XDG_CACHE_HOME=t.TempDir() lands under $TMPDIR (writable);
   with the real ~/.cache location the redirected config dir may not be writable in-cage.
   Worth verifying that the real run mounts it (or relies on $TMPDIR).
