package sysdep

import (
	"context"
	"fmt"
	"time"
)

// Browser abstracts opening a URL in the user's default web browser — the one
// interactive step of the OAuth2 consent flow (`credential authorize`, AC-0069a).
// Routing it through a seam keeps the authorize command unit-testable: the fake
// records the URL instead of launching a browser.
type Browser interface {
	// Open launches url in the default browser (macOS `open`). A non-nil error means
	// the launcher could not be run; it does not report whether the user completed
	// anything in the browser.
	Open(url string) error
}

// browserOpenTimeout bounds the `open` invocation. `open` returns immediately once
// it has handed the URL to LaunchServices, so this only guards against a wedged
// launcher.
const browserOpenTimeout = 10 * time.Second

// OSBrowser is the production Browser: it shells out to macOS `open` via the
// Commander seam.
type OSBrowser struct {
	Commander Commander
}

var _ Browser = OSBrowser{}

func (b OSBrowser) Open(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), browserOpenTimeout)
	defer cancel()
	if _, err := b.Commander.Output(ctx, "open", url); err != nil {
		return fmt.Errorf("sysdep: open %q in browser: %w", url, err)
	}
	return nil
}
