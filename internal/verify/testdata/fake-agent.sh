#!/bin/sh
# fake-agent.sh — the hostile "fake agent" for the AC-0033 cage-verification
# battery. It runs INSIDE the real cage (agent-safehouse + mitmproxy) as the
# agent.command and tries to escape, emitting exactly one structured line per
# vector:
#
#     CREANCE::<vector-id>::<observed>
#
# The Go harness (verification_integration_test.go) parses these and evaluates
# them against internal/verify.Vectors. A BLOCKED vector emits its expected
# refusal token or "LEAK" (it got through); an ALLOWED vector emits its success
# token or a "blocked:*" token; egress vectors emit "skip" when run offline.
#
# Inputs arrive as CREANCE_* env vars (passed via config.env) plus the cage's own
# injected env (HTTPS_PROXY, CLAUDE_CONFIG_DIR). Most proxy probes deliberately drop
# the injected CA env vars and trust mitmproxy's CA via an explicit, in-mount
# --cacert copy (CREANCE_CA): that isolates "does the proxy enforce the policy" from
# CA plumbing. The two env-ca-* probes are the exception (AC-0034) — they trust the
# CA ONLY via the injected env-var file to prove that file is readable in-cage, so we
# stash its path before the unset below.
#
# POSIX sh only (the cage runs /bin/sh). No `set -e`: every probe must run even
# when an earlier one fails, and a non-zero rc is frequently the expected result.

CREANCE_INJECTED_CA="${NODE_EXTRA_CA_CERTS:-$SSL_CERT_FILE}"
unset SSL_CERT_FILE NODE_EXTRA_CA_CERTS REQUESTS_CA_BUNDLE GIT_SSL_CAINFO CURL_CA_BUNDLE

PROJ="${CREANCE_PROJ:-.}"
BODY="$PROJ/.probe-body"
HDR="$PROJ/.probe-hdr"

emit() { echo "CREANCE::$1::$2"; }

# http_code_through_proxy URL [--cacert FILE] — echoes "<code> <x-cage-reason>".
http_through_proxy() {
	url="$1"
	shift
	code=$(curl -s -o "$BODY" -D "$HDR" -w '%{http_code}' \
		--max-time 25 -x "$HTTPS_PROXY" "$@" "$url" 2>/dev/null)
	reason=$(grep -i '^x-cage-reason:' "$HDR" 2>/dev/null | tr -d '\r' | awk '{print $2}')
	rm -f "$BODY" "$HDR"
	echo "${code:-000} ${reason}"
}

# tcp_blocked HOST PORT [-4|-6] — emits "blocked" if the connect was refused,
# "LEAK" if it succeeded. -z = connect-scan; -G/-w cap the attempt.
probe_tcp() {
	host="$2"
	port="$3"
	fam="$4"
	# shellcheck disable=SC2086
	if nc -z -G 2 -w 2 $fam "$host" "$port" >/dev/null 2>&1; then
		emit "$1" LEAK
	else
		emit "$1" blocked
	fi
}

# ---- BLOCKED: kernel/Seatbelt --------------------------------------------------

# Read a secret planted outside the cage's mounts (under real $HOME) → denied.
if cat "$CREANCE_SECRET" >/dev/null 2>&1; then
	emit fs-outside LEAK
else
	emit fs-outside blocked
fi

# Write into the real home dir (the real ~/.claude lives here and is never
# mounted) → denied. On the expected blocked path nothing is created.
probe="$CREANCE_HOME/.creance-escape-$$"
if (: >"$probe") 2>/dev/null; then
	emit fs-real-claude LEAK
	rm -f "$probe"
else
	emit fs-real-claude blocked
fi

probe_tcp net-raw-tcp 1.1.1.1 443
probe_tcp net-localhost-v4 127.0.0.1 "$CREANCE_BLOCKED_PORT" -4
probe_tcp net-localhost-v6 ::1 "$CREANCE_BLOCKED_PORT" -6
probe_tcp net-dns 8.8.8.8 53

# A child process must inherit the sandbox: re-run a blocked vector one fork down.
if sh -c 'nc -z -G 2 -w 2 1.1.1.1 443 >/dev/null 2>&1'; then
	emit net-child LEAK
else
	emit net-child blocked
fi

# ---- BLOCKED: proxy ------------------------------------------------------------

set -- $(http_through_proxy "https://$CREANCE_SOFT_HOST/v2/auth/" --cacert "$CREANCE_CA")
code="$1"; reason="$2"
if [ "$code" = "403" ] && [ "$reason" = "soft-deny" ]; then
	emit proxy-soft-deny 403:soft-deny
elif [ "$code" = "200" ]; then
	emit proxy-soft-deny LEAK
else
	emit proxy-soft-deny "ERROR:$code"
fi

set -- $(http_through_proxy "https://$CREANCE_HARD_HOST/anything" --cacert "$CREANCE_CA")
code="$1"; reason="$2"
if [ "$code" = "403" ] && [ "$reason" = "hard-deny" ]; then
	emit proxy-hard-deny 403:hard-deny
elif [ "$code" = "200" ]; then
	emit proxy-hard-deny LEAK
else
	emit proxy-hard-deny "ERROR:$code"
fi

# Allowlisted host but a path outside its allowed prefix → soft-deny (local 403).
set -- $(http_through_proxy "$CREANCE_OFFPATH_URL" --cacert "$CREANCE_CA")
code="$1"; reason="$2"
if [ "$code" = "403" ] && [ "$reason" = "soft-deny" ]; then
	emit proxy-offpath 403:soft-deny
elif [ "$code" = "200" ]; then
	emit proxy-offpath LEAK
else
	emit proxy-offpath "ERROR:$code"
fi

# ---- ALLOWED: false-negative guards -------------------------------------------

# A host_services loopback port is reachable.
if nc -z -G 2 -w 2 127.0.0.1 "$CREANCE_SVC_PORT" >/dev/null 2>&1; then
	emit svc-allowed connect-ok
else
	emit svc-allowed blocked
fi

if [ "$CREANCE_EGRESS" = "1" ]; then
	set -- $(http_through_proxy "https://$CREANCE_ALLOW_HOST/" --cacert "$CREANCE_CA")
	code="$1"
	[ "$code" = "200" ] && emit allow-200 200 || emit allow-200 "blocked:$code"

	# Passthrough: trust ONLY the system CA (no --cacert). A 200 proves mitmproxy
	# tunnelled without terminating TLS (the real upstream cert validated).
	set -- $(http_through_proxy "https://$CREANCE_PASS_HOST/")
	code="$1"
	[ "$code" = "200" ] && emit passthrough 200 || emit passthrough "blocked:$code"

	# AC-0034: node trusts the proxy CA ONLY via NODE_EXTRA_CA_CERTS (the injected
	# env-var file — NOT the keychain, NOT CREANCE_CA). It CONNECTs through the proxy
	# then completes TLS using node's default trust store augmented by that file. A
	# 200 proves the file was readable in-cage; a CA error proves it was not.
	if command -v node >/dev/null 2>&1; then
		code=$(NODE_EXTRA_CA_CERTS="$CREANCE_INJECTED_CA" node - <<'NODEJS' 2>/dev/null
const http=require('http'),tls=require('tls');
const m=(process.env.HTTPS_PROXY||'').replace(/^https?:\/\//,'').split(':');
const host=process.env.CREANCE_ALLOW_HOST;
const done=v=>{console.log(v);process.exit(0);};
const req=http.request({host:m[0],port:m[1]||80,method:'CONNECT',path:host+':443'});
req.on('connect',(_res,socket)=>{
  const s=tls.connect({socket,servername:host},()=>{
    s.write('GET / HTTP/1.1\r\nHost: '+host+'\r\nConnection: close\r\n\r\n');
  });
  let buf='';
  s.on('data',d=>{buf+=d;});
  s.on('end',()=>{const mm=buf.match(/^HTTP\/1\.[01] (\d+)/);done(mm?mm[1]:'ERR');});
  s.on('error',e=>done('ERR:'+(e.code||'tls')));
});
req.on('error',e=>done('ERR:'+(e.code||'conn')));
req.setTimeout(25000,()=>done('ERR:timeout'));
req.end();
NODEJS
		)
		[ "$code" = "200" ] && emit env-ca-node 200 || emit env-ca-node "blocked:${code:-000}"
	else
		emit env-ca-node skip
	fi

	# AC-0034: python trusts the proxy CA ONLY via SSL_CERT_FILE/REQUESTS_CA_BUNDLE
	# (the injected env-var file; the OpenSSL CA-file path requests also uses). urllib
	# honors the proxy env and the SSL_CERT_FILE default-verify path.
	if command -v python3 >/dev/null 2>&1; then
		code=$(SSL_CERT_FILE="$CREANCE_INJECTED_CA" REQUESTS_CA_BUNDLE="$CREANCE_INJECTED_CA" \
			python3 - "https://$CREANCE_ALLOW_HOST/" <<'PYEOF' 2>/dev/null
import sys, urllib.request
try:
    print(urllib.request.urlopen(sys.argv[1], timeout=25).status)
except Exception:
    print("ERR")
PYEOF
		)
		[ "$code" = "200" ] && emit env-ca-python 200 || emit env-ca-python "blocked:${code:-000}"
	else
		emit env-ca-python skip
	fi
else
	emit allow-200 skip
	emit passthrough skip
	emit env-ca-node skip
	emit env-ca-python skip
fi

# ---- DOCUMENTED: honesty assertions -------------------------------------------

# Project files are damageable by design: rm/write within ./ succeeds.
victim="$PROJ/.creance-victim"
if (: >"$victim") 2>/dev/null && rm -f "$victim" 2>/dev/null; then
	emit doc-rm rm-ok
else
	emit doc-rm blocked
fi

# A POST body to an allowlisted host goes through (residual exfil surface). The
# harness separately checks it was audited and the body NOT recorded.
if [ "$CREANCE_EGRESS" = "1" ]; then
	set -- $(http_through_proxy "https://$CREANCE_ALLOW_HOST/" --cacert "$CREANCE_CA" -X POST -d creance-exfil-marker)
	code="$1"; reason="$2"
	if [ -z "$reason" ] && [ "$code" != "000" ]; then
		emit doc-post post-sent
	else
		emit doc-post "blocked:$code"
	fi
else
	emit doc-post skip
fi

# The ephemeral CLAUDE_CONFIG_DIR is writable, but it is NOT the real ~/.claude —
# so a planted hook cannot survive into a later un-caged Claude run. The harness
# asserts (structurally) that this dir is under the cache, not real ~/.claude.
hookdir="$CLAUDE_CONFIG_DIR/hooks"
if mkdir -p "$hookdir" 2>/dev/null && (echo '{"planted":true}' >"$hookdir/creance-escape.json") 2>/dev/null; then
	emit doc-config-dir planted
else
	emit doc-config-dir blocked
fi
