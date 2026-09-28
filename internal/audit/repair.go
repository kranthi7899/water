package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// KindRepair records that Repair dropped a torn final line.
const KindRepair Kind = "repair"

// Repair drops a single torn final line from the audit log at path, if a
// torn final write is the only thing wrong with it, and appends a KindRepair
// entry recording what was dropped. It refuses to touch anything earlier
// than the last line: a break further back is a real tamper or bug, not an
// interrupted write, and must never be "fixed" by truncation. It also
// refuses, leaving the file unchanged, a final line that is a complete JSON
// entry (an edit, not a torn write) or one the anchor in opts already
// covers. The dropped bytes are kept in a 0600 "<path>.repair-dropped-<n>"
// file next to the log.
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
	// A torn write leaves a prefix of a line, never a whole JSON object. A
	// final line that parses completely but does not verify was edited
	// after it was written: that is evidence, not an interrupted write.
	var whole Entry
	if json.Unmarshal([]byte(torn), &whole) == nil {
		return fmt.Errorf("audit: %s: the final line is a complete entry that does not verify; that is an edit, not a torn write, so it is left in place", path)
	}
	// Check the anchor before touching the file: Open (below) would refuse
	// the truncated file anyway if the dropped line was already anchored,
	// but only after the line was gone for good.
	if err := checkRepairAnchor(path, lines[:len(lines)-1], opts); err != nil {
		return err
	}
	// Keep the dropped bytes, whatever happens next.
	dropped := fmt.Sprintf("%s.repair-dropped-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(dropped, []byte(torn+"\n"), 0o600); err != nil {
		return fmt.Errorf("audit: preserving the dropped line: %w", err)
	}
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
	_, err = log.Append(Record{Kind: KindRepair, Reason: fmt.Sprintf("dropped a torn final line (%d bytes, kept in %s): %s", len(torn), filepath.Base(dropped), preview)})
	return err
}

// checkRepairAnchor refuses a repair whose result Open would not accept
// against the anchor in opts (if any): the file after dropping the final
// line must end at the anchored entry, or exactly one validly chained entry
// past it (a crash between a durable append and its anchor update). An
// anchor at or past the dropped line means that line was a complete,
// anchored entry, so dropping it would destroy tamper evidence.
func checkRepairAnchor(path string, kept []string, opts []Option) error {
	var scratch Log
	for _, o := range opts {
		o(&scratch)
	}
	if scratch.anchor == nil {
		return nil
	}
	aseq, ahash, ok, err := scratch.anchor.LoadAuditAnchor(context.Background())
	if err != nil {
		return fmt.Errorf("audit: loading anchor: %w", err)
	}
	if !ok {
		return nil
	}
	var last Entry
	if len(kept) > 0 {
		if err := json.Unmarshal([]byte(kept[len(kept)-1]), &last); err != nil {
			return fmt.Errorf("audit: %s: reading the last good entry: %w", path, err)
		}
	}
	if aseq == last.Seq && ahash == last.Hash {
		return nil
	}
	if last.Seq > 0 && aseq == last.Seq-1 && ahash == last.PrevHash {
		return nil
	}
	return fmt.Errorf("audit: %s: the anchor is at seq=%d but the last good entry is seq=%d; the final line was already anchored, so it is not a torn write; refusing to drop it (the file is unchanged)", path, aseq, last.Seq)
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
