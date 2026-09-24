package audit

import (
	"bufio"
	"errors"
	"fmt"
	"os"
)

// KindRepair records that Repair dropped a torn final line.
const KindRepair Kind = "repair"

// Repair drops a single torn final line from the audit log at path, if a
// torn final write is the only thing wrong with it, and appends a KindRepair
// entry recording what was dropped. It refuses to touch anything earlier
// than the last line: a break further back is a real tamper or bug, not an
// interrupted write, and must never be "fixed" by truncation.
func Repair(path string, opts ...Option) error {
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return fmt.Errorf("audit: %s is in use: %w", path, err)
	}
	defer unlock()

	lines, err := readLines(path)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return fmt.Errorf("audit: %s is empty; nothing to repair", path)
	}
	_, verr := verify(path)
	if verr == nil {
		return fmt.Errorf("audit: %s already verifies; nothing to repair", path)
	}
	var be *BreakError
	if !errors.As(verr, &be) {
		return fmt.Errorf("audit: %s: %w", path, verr)
	}
	goodLines := be.Line - 1
	if goodLines != len(lines)-1 {
		return fmt.Errorf("audit: %s is broken at line %d of %d, not only at the last line; refusing to repair more than a torn final write", path, be.Line, len(lines))
	}

	torn := lines[len(lines)-1]
	if err := truncateToLines(path, lines[:len(lines)-1]); err != nil {
		return err
	}
	unlock() // Open below takes its own lock on the now-valid file.

	log, err := Open(path, opts...)
	if err != nil {
		return err
	}
	defer log.Close()
	preview := torn
	if len(preview) > 200 {
		preview = preview[:200] + "…"
	}
	_, err = log.Append(Record{Kind: KindRepair, Reason: fmt.Sprintf("dropped a torn final line (%d bytes): %s", len(torn), preview)})
	return err
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func truncateToLines(path string, lines []string) error {
	tmp := path + ".repair-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|noFollow, 0o600)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
