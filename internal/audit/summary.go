package audit

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/tobyS/agent-creance/internal/style"
)

// Summary is the aggregate of an audit-log stream: decision counts plus totals and an
// intercepted/passthrough split. Unknown counts any decision outside the known three
// (forward-compat with a future log version); Malformed counts lines that failed to
// parse and were skipped.
type Summary struct {
	Total       int
	Allow       int
	SoftDeny    int
	HardDeny    int
	Intercepted int
	Passthrough int
	Unknown     int
	Malformed   int
}

// Summarize tallies decisions across the given readers in order. Callers pass the
// rotated (.1) reader first, then the current reader, so the two files are summed as
// one logical stream. A malformed line is counted and skipped — a corrupt entry never
// aborts the summary.
func Summarize(readers ...io.Reader) (Summary, error) {
	var s Summary
	for _, r := range readers {
		if err := s.consume(r); err != nil {
			return Summary{}, err
		}
	}
	return s, nil
}

// consume scans one reader line-by-line. It uses bufio.Reader.ReadString rather than
// bufio.Scanner so a very long line (a long URL) past Scanner's 64 KB token cap is
// still read whole rather than erroring.
func (s *Summary) consume(r io.Reader) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(strings.TrimSpace(line)) > 0 {
			s.tally(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read audit stream: %w", err)
		}
	}
}

func (s *Summary) tally(line string) {
	entry, err := ParseLine([]byte(line))
	if err != nil {
		s.Malformed++
		return
	}
	s.Total++
	if entry.IsPassthrough() {
		s.Passthrough++
	} else {
		s.Intercepted++
	}
	switch entry.Decision {
	case DecisionAllow:
		s.Allow++
	case DecisionSoftDeny:
		s.SoftDeny++
	case DecisionHardDeny:
		s.HardDeny++
	default:
		s.Unknown++
	}
}

// Render formats the summary for the operator. The layout is golden-stable. sty
// bolds the header, colors the decision labels by verdict, and dims the
// secondary detail; a disabled styler reproduces the original bytes.
func (s Summary) Render(sty *style.Styler) string {
	if s.Total == 0 && s.Malformed == 0 {
		return "No audit entries yet.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d entries %s\n",
		sty.Header("Audit summary:"), s.Total,
		sty.Dim(fmt.Sprintf("(%d intercepted, %d passthrough)", s.Intercepted, s.Passthrough)))
	fmt.Fprintf(&b, "  %s      %d\n", sty.OK("allow"), s.Allow)
	fmt.Fprintf(&b, "  %s  %d\n", sty.Warn("soft-deny"), s.SoftDeny)
	fmt.Fprintf(&b, "  %s  %d\n", sty.Bad("hard-deny"), s.HardDeny)
	if s.Unknown > 0 {
		fmt.Fprintf(&b, "  %s    %d\n", sty.Dim("unknown"), s.Unknown)
	}
	if s.Malformed > 0 {
		fmt.Fprintf(&b, "%s\n", sty.Dim(fmt.Sprintf("(%d malformed line(s) skipped)", s.Malformed)))
	}
	return b.String()
}
