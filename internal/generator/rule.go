// Package generator turns a project's dependency manifest (package.json /
// composer.json) into a deterministic, source-annotated set of egress allow rules.
// For each direct dependency it looks up the package's homepage + repository
// metadata through the registry client (internal/generator/registry) and emits:
//
//   - a homepage rule, host-wide for a bare-host homepage and path-scoped when the
//     homepage URL carries a path (so a package on a shared, path-multiplexed host
//     like <user>.github.io does not allowlist every other tenant's content);
//   - a repository rule scoped to <org>/<repo>; and
//   - for a repository on a known forge (GitHub, GitLab), the forge's companion
//     content hosts (raw/codeload/pages/release-CDN), scoped to the same <org>/<repo>
//     wherever the host's URL layout permits.
//
// The emitted rule set is cached on disk keyed by the manifest's hash, so an
// unchanged manifest reuses the previous run's rules without any registry call.
// All side effects go through sysdep seams (FileSystem) and the registry lookup
// seam, injected at construction, so unit tests are fully hermetic.
//
// Turning these rules into the compiled policy.json (union with explicit/global
// rules, source annotations in the artifact) is a separate concern (AC-0013); this
// package only produces the annotated rules.
package generator

import "github.com/tobyS/agent-creance/internal/policy"

// Rule is an allow rule emitted by a generator, annotated with the source that
// produced it. The embedded policy.Rule is the matcher-facing rule; Source records
// provenance for `policy show` (e.g. "generated:package_json:react"); LowerTrust
// marks a host-wide companion host (objects.githubusercontent.com) that a stricter
// threat model can drop.
type Rule struct {
	Rule       policy.Rule `json:"rule"`
	Source     string      `json:"source"`
	LowerTrust bool        `json:"lower_trust,omitempty"`
}
