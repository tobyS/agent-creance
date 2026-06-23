package generator

import "strings"

// sharedApexHosts is the set of hosts where many independent tenants share one
// apex, distinguished only by URL path (e.g. sourceforge.net/projects/<x>).
// A bare-host homepage on such a host must NOT become a host-wide allow — that
// would allowlist every other tenant's content, the exact tenant-isolation the
// path-scoping in homepageRule exists to provide. Because a bare host carries no
// path to scope to, the rule is dropped instead (see homepageRule).
//
// This is data, not per-call code: vetting and extending it is a table edit. The
// membership criterion is "many unrelated projects live behind this exact host,
// keyed by path." Subdomain-isolated platforms (each tenant gets its own host,
// e.g. *.vercel.app, *.github.io, *.gitlab.io) are deliberately absent — a
// whole-host allow there already covers exactly one tenant, so they are safe.
var sharedApexHosts = map[string]bool{
	"sourceforge.net":  true, // projects at /projects/<name>/
	"pear.php.net":     true, // PEAR packages at /package/<name>
	"pythonhosted.org": true, // shared package host keyed by path
}

// isSharedApex reports whether host is a known shared-apex host. host is matched
// case-insensitively (callers typically pass an already-lowercased hostname).
func isSharedApex(host string) bool {
	return sharedApexHosts[strings.ToLower(host)]
}
