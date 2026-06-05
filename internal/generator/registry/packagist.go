package registry

import (
	"encoding/json"
	"fmt"
)

// packagistSource fetches from Packagist's p2 metadata endpoint
// repo.packagist.org/p2/<vendor>/<pkg>.json. The endpoint returns versions
// newest-first; element [0] is fully populated (later entries are minified
// deltas), so reading [0] gives the latest release's homepage + source URL
// without expanding the minified encoding.
type packagistSource struct{}

func (packagistSource) name() string { return "packagist" }

func (packagistSource) url(pkg string) string {
	return "https://repo.packagist.org/p2/" + pkg + ".json"
}

type packagistDoc struct {
	Packages map[string][]packagistVersion `json:"packages"`
}

type packagistVersion struct {
	Homepage string `json:"homepage"`
	Source   struct {
		URL string `json:"url"`
	} `json:"source"`
}

func (packagistSource) parse(body []byte) (Metadata, error) {
	var doc packagistDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return Metadata{}, err
	}
	// p2 keys the array by the package name; there is exactly one key. Take the
	// first (newest, fully-populated) version object.
	for _, versions := range doc.Packages {
		if len(versions) > 0 {
			v := versions[0]
			return Metadata{Homepage: v.Homepage, Repository: v.Source.URL}, nil
		}
	}
	return Metadata{}, fmt.Errorf("no versions in packagist response")
}
