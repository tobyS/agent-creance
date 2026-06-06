package audit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// openStreams opens the rotated (.1) file then the current file, in that order, so
// callers read them as one logical stream (older entries first). A file that does not
// exist is skipped — neither absence is an error (the cage may not have rotated yet,
// or may never have run). The returned closeAll closes every opened handle.
func openStreams(rotatedPath, currentPath string) (readers []io.Reader, closeAll func(), err error) {
	var handles []*os.File
	closeAll = func() {
		for _, h := range handles {
			_ = h.Close()
		}
	}
	for _, p := range []string{rotatedPath, currentPath} {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			closeAll()
			return nil, func() {}, fmt.Errorf("open audit log %s: %w", p, err)
		}
		handles = append(handles, f)
		readers = append(readers, f)
	}
	return readers, closeAll, nil
}

// SummarizeFiles reads the rotated then current audit file as one stream and returns
// the decision tally. Missing files yield a zero Summary (no error).
func SummarizeFiles(rotatedPath, currentPath string) (Summary, error) {
	readers, closeAll, err := openStreams(rotatedPath, currentPath)
	if err != nil {
		return Summary{}, err
	}
	defer closeAll()
	return Summarize(readers...)
}

// Dump writes every entry in the rotated then current audit file to w, one
// human-formatted line each. Malformed lines are skipped. Missing files produce no
// output (and no error).
func Dump(w io.Writer, rotatedPath, currentPath string) error {
	readers, closeAll, err := openStreams(rotatedPath, currentPath)
	if err != nil {
		return err
	}
	defer closeAll()
	for _, r := range readers {
		br := bufio.NewReader(r)
		for {
			line, readErr := br.ReadString('\n')
			if len(strings.TrimSpace(line)) > 0 {
				if entry, perr := ParseLine([]byte(line)); perr == nil {
					if _, werr := fmt.Fprintln(w, FormatEntry(entry)); werr != nil {
						return fmt.Errorf("write audit line: %w", werr)
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return fmt.Errorf("read audit log: %w", readErr)
			}
		}
	}
	return nil
}
