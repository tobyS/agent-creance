package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
)

// pollInterval is the stat-poll backstop. fsnotify events on macOS/kqueue are
// coalesced and best-effort, so following must not depend on them for correctness: a
// periodic stat catches any append or rotation a dropped event would have missed.
// fsnotify only reduces latency below this interval.
const pollInterval = 1 * time.Second

// Follow streams new audit entries to w as they are appended, formatted one per line,
// and keeps streaming correctly across a rename-based rotation (egress.jsonl ->
// egress.jsonl.1, fresh egress.jsonl). It follows from the current end of the file
// (historical entries are not re-emitted) and returns nil when ctx is cancelled.
//
// It watches the parent directory rather than the file: on macOS/kqueue a watch on
// the file itself is destroyed by the rename, whereas a directory watch survives and
// delivers the Create for the new file — which, together with inode-identity checks
// and the poll backstop, is how rotation is detected. dirPath is the directory holding
// currentPath (typically filepath.Dir(layout.EgressJSONL())).
func Follow(ctx context.Context, w io.Writer, dirPath, currentPath string) error {
	if _, err := os.Stat(dirPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no audit log directory yet (%s) — has the cage run?", dirPath)
		}
		return fmt.Errorf("stat audit log dir: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create file watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()
	if err := watcher.Add(dirPath); err != nil {
		return fmt.Errorf("watch %s: %w", dirPath, err)
	}

	f := &follower{w: w, currentPath: currentPath}
	defer f.closeFile()

	// Open the current file at its end so only entries appended after we start are
	// streamed; if it does not exist yet, openTail leaves the handle nil and the loop
	// picks it up when it appears.
	if err := f.openTail(); err != nil {
		return err
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Name != currentPath {
				continue // ignore the .1 backup and unrelated dir entries
			}
			if ev.Op == fsnotify.Chmod {
				continue // kqueue fires Chmod on truncate; never a rotation signal
			}
			if err := f.sync(); err != nil {
				return err
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return fmt.Errorf("watch audit log: %w", err)
		case <-ticker.C:
			if err := f.sync(); err != nil {
				return err
			}
		}
	}
}

// follower holds the streaming state for one Follow call.
type follower struct {
	w           io.Writer
	currentPath string
	file        *os.File    // nil until the current file exists
	info        os.FileInfo // identity of file, for rotation detection via os.SameFile
	leftover    []byte      // bytes of an as-yet-incomplete trailing line
}

// sync reconciles state with the filesystem: it handles a rotation if the file at
// currentPath is a different inode than the one we hold, then reads any newly appended
// bytes. It is idempotent — safe to call from both fsnotify events and the poll tick.
func (f *follower) sync() error {
	if err := f.checkRotation(); err != nil {
		return err
	}
	return f.drain()
}

// checkRotation switches to a new file when the inode at currentPath differs from the
// one we hold (a rotation, or the file's first appearance). It drains the old handle
// to EOF *before* closing it — the renamed file keeps its inode, so the still-open
// handle reads the tail that landed before the flip — then opens the new file from the
// start (drain, called by sync, then emits its contents).
func (f *follower) checkRotation() error {
	info, err := os.Stat(f.currentPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // gone mid-rotation; wait for the new file's Create
	}
	if err != nil {
		return fmt.Errorf("stat audit log: %w", err)
	}
	if f.file != nil && os.SameFile(f.info, info) {
		return nil // same file, no rotation
	}
	if f.file != nil {
		if err := f.drain(); err != nil {
			return err
		}
		f.closeFile()
	}
	return f.openFromStart()
}

// drain reads from the current handle to EOF, emitting every complete line and
// buffering any partial trailing line until its newline arrives.
func (f *follower) drain() error {
	if f.file == nil {
		return nil
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := f.file.Read(buf)
		if n > 0 {
			f.leftover = append(f.leftover, buf[:n]...)
			if emitErr := f.emitLines(); emitErr != nil {
				return emitErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read audit log: %w", err)
		}
	}
}

// emitLines writes out every complete (newline-terminated) line currently buffered,
// skipping blank and malformed lines, and keeps the remainder buffered.
func (f *follower) emitLines() error {
	for {
		i := bytes.IndexByte(f.leftover, '\n')
		if i < 0 {
			return nil
		}
		line := f.leftover[:i]
		f.leftover = f.leftover[i+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := ParseLine(line)
		if err != nil {
			continue // skip malformed lines, like Dump/Summarize
		}
		if _, err := fmt.Fprintln(f.w, FormatEntry(entry)); err != nil {
			return fmt.Errorf("write audit line: %w", err)
		}
	}
}

// openTail opens the current file (if it exists) positioned at its end, so only future
// appends are streamed.
func (f *follower) openTail() error { return f.open(true) }

// openFromStart opens the current file (if it exists) positioned at its start, so its
// whole content is streamed — used after a rotation, when the new file is fresh.
func (f *follower) openFromStart() error { return f.open(false) }

func (f *follower) open(atEnd bool) error {
	file, err := os.Open(f.currentPath)
	if errors.Is(err, fs.ErrNotExist) {
		f.file = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat audit log: %w", err)
	}
	if atEnd {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			_ = file.Close()
			return fmt.Errorf("seek audit log: %w", err)
		}
	}
	f.file = file
	f.info = info
	f.leftover = nil
	return nil
}

func (f *follower) closeFile() {
	if f.file != nil {
		_ = f.file.Close()
		f.file = nil
	}
}
