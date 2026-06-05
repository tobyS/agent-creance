package registry

import (
	"bytes"
	"encoding/json"
	"strings"
)

// npmSource fetches from the npm registry. The package document ("packument") at
// registry.npmjs.org/<pkg> hoists homepage + repository to its top level from the
// latest published version, so we read them there, falling back to the latest
// version object when a top-level field is absent.
type npmSource struct{}

func (npmSource) name() string { return "npm" }

func (npmSource) url(pkg string) string {
	return "https://registry.npmjs.org/" + npmPackagePath(pkg)
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

// npmPackument is the slice of the packument we care about. It is decoded
// leniently (no DisallowUnknownFields): this is a large third-party document,
// not one of our own strict-schema fixtures.
type npmPackument struct {
	DistTags   map[string]string        `json:"dist-tags"`
	Homepage   string                   `json:"homepage"`
	Repository npmRepository            `json:"repository"`
	Versions   map[string]npmVersionDoc `json:"versions"`
}

type npmVersionDoc struct {
	Homepage   string        `json:"homepage"`
	Repository npmRepository `json:"repository"`
}

// npmRepository normalizes npm's polymorphic "repository" field, which may be a
// bare URL string or an object {type, url}, into a single URL string.
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
	var doc npmPackument
	if err := json.Unmarshal(body, &doc); err != nil {
		return Metadata{}, err
	}
	md := Metadata{Homepage: doc.Homepage, Repository: doc.Repository.URL}

	// Hoisting reflects the last publish; if a field is missing at the top level,
	// fall back to the latest version's manifest.
	if md.Homepage == "" || md.Repository == "" {
		if latest, ok := doc.DistTags["latest"]; ok {
			if v, ok := doc.Versions[latest]; ok {
				if md.Homepage == "" {
					md.Homepage = v.Homepage
				}
				if md.Repository == "" {
					md.Repository = v.Repository.URL
				}
			}
		}
	}
	return md, nil
}
