package registry

import (
	"bytes"
	"encoding/json"
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

func (npmSource) url(pkg string) string {
	return "https://registry.npmjs.org/" + npmPackagePath(pkg) + "/latest"
}

// npmPackagePath renders pkg for the registry URL path. A scoped name
// (@scope/name) must have its single slash percent-encoded; unscoped names need
// no escaping (npm names are lowercase + [-._]).
func npmPackagePath(pkg string) string {
	if strings.HasPrefix(pkg, "@") {
		return strings.Replace(pkg, "/", "%2f", 1)
	}
	return pkg
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
