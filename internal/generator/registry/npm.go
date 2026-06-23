package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// npmSource fetches from the npm registry's per-version endpoint
// registry.npmjs.org/<pkg>/latest, which returns just the latest version's
// manifest (kilobytes). The full packument at registry.npmjs.org/<pkg> is
// deliberately avoided: for popular packages it can be enormous (vite: ~39 MB),
// blowing past the sysdep HTTP body cap. The abbreviated packument format is no
// alternative — it omits homepage and repository entirely.
type npmSource struct{}

func (npmSource) name() string { return "npm" }

// npmSegment matches one npm name segment (an unscoped name, or a scope/name
// within a scoped package): starts with an alphanumeric (so never "."/"_"), then
// alphanumerics or "._~-". It excludes URL-significant characters (?, #, @, :, %,
// /), so a name that passes cannot reshape the request URL. Legacy mixed-case
// names are tolerated.
var npmSegment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._~-]*$`)

// validate enforces the npm name charset for both unscoped ("name") and scoped
// ("@scope/name") packages.
func (npmSource) validate(pkg string) error {
	if scoped, ok := strings.CutPrefix(pkg, "@"); ok {
		scope, name, ok := strings.Cut(scoped, "/")
		if !ok || strings.Contains(name, "/") {
			return fmt.Errorf("registry: invalid npm package name %q (want @scope/name)", pkg)
		}
		if !npmSegment.MatchString(scope) || !npmSegment.MatchString(name) {
			return fmt.Errorf("registry: invalid npm package name %q", pkg)
		}
		return nil
	}
	if !npmSegment.MatchString(pkg) {
		return fmt.Errorf("registry: invalid npm package name %q", pkg)
	}
	return nil
}

func (npmSource) url(pkg string) string {
	return "https://registry.npmjs.org/" + npmPackagePath(pkg) + "/latest"
}

// npmPackagePath renders pkg for the registry URL path. A scoped name
// (@scope/name) keeps its single slash percent-encoded as the lowercase "%2f"
// the registry serves; each segment is PathEscaped as defence in depth (a no-op
// for a validated name).
func npmPackagePath(pkg string) string {
	if scoped, ok := strings.CutPrefix(pkg, "@"); ok {
		if scope, name, ok := strings.Cut(scoped, "/"); ok {
			return url.PathEscape("@"+scope) + "%2f" + url.PathEscape(name)
		}
	}
	return url.PathEscape(pkg)
}

// npmVersionDoc is the slice of the /latest version document we care about.
// Both fields are publisher-supplied and optional (e.g. old express releases
// carry a repository but no homepage). It is decoded leniently (no
// DisallowUnknownFields): this is a large third-party document, not one of our
// own strict-schema fixtures.
type npmVersionDoc struct {
	Homepage   string        `json:"homepage"`
	Repository npmRepository `json:"repository"`
}

// npmRepository normalizes npm's polymorphic "repository" field, which may be a
// bare URL string or an object {type, url, directory?}, into a single URL
// string. npm normalizes to the object form at publish time, but old or
// hand-published documents can still carry the string form.
type npmRepository struct {
	URL string
}

func (r *npmRepository) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		r.URL = s
	case '{':
		var obj struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return err
		}
		r.URL = obj.URL
	}
	// Any other shape (number, array) is treated as "no repository".
	return nil
}

func (npmSource) parse(body []byte) (Metadata, error) {
	var doc npmVersionDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return Metadata{}, err
	}
	return Metadata{Homepage: doc.Homepage, Repository: doc.Repository.URL}, nil
}
