package registry

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// packagistSource fetches from Packagist's p2 metadata endpoint
// repo.packagist.org/p2/<vendor>/<pkg>.json. The endpoint returns versions
// newest-first; element [0] is fully populated (later entries are minified
// deltas), so reading [0] gives the latest release's homepage + source URL
// without expanding the minified encoding.
type packagistSource struct{}

func (packagistSource) name() string { return "packagist" }

// packagistSegment matches one Packagist name segment (vendor or package):
// alphanumerics separated by single ".", "-", or "_" runs, per Packagist's
// documented charset. It deliberately excludes URL-significant characters
// (?, #, @, :, %, /), so a name that passes cannot reshape the request URL.
var packagistSegment = regexp.MustCompile(`^[a-zA-Z0-9]([._-]?[a-zA-Z0-9]+)*$`)

// validate enforces the Packagist "vendor/package" shape: exactly two segments,
// each matching the registry name charset.
func (packagistSource) validate(pkg string) error {
	vendor, name, ok := strings.Cut(pkg, "/")
	if !ok || strings.Contains(name, "/") {
		return fmt.Errorf("registry: invalid packagist package name %q (want vendor/package)", pkg)
	}
	if !packagistSegment.MatchString(vendor) || !packagistSegment.MatchString(name) {
		return fmt.Errorf("registry: invalid packagist package name %q", pkg)
	}
	return nil
}

func (packagistSource) url(pkg string) string {
	// PathEscape each segment as defence in depth; for a validated name this is a
	// no-op, but it ensures even a future loosening of validate cannot inject.
	vendor, name, _ := strings.Cut(pkg, "/")
	return "https://repo.packagist.org/p2/" + url.PathEscape(vendor) + "/" + url.PathEscape(name) + ".json"
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
