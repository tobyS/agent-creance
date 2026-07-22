package sysdeptest

import "github.com/tobyS/agent-creance/internal/sysdep"

// FakeBrowser is a scripted Browser that records the URLs it was asked to open
// instead of launching a real browser.
type FakeBrowser struct {
	// Opened records each URL passed to Open, in order.
	Opened []string
	// OpenErr, if set, is returned by Open.
	OpenErr error
}

var _ sysdep.Browser = (*FakeBrowser)(nil)

func (f *FakeBrowser) Open(url string) error {
	f.Opened = append(f.Opened, url)
	return f.OpenErr
}
