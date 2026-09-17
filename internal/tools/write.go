package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Write via an opened root and rename a sibling temporary file. Root rejects
// symlink escapes even if a directory changes after validation; rename also
// avoids modifying an outside file through a hard link in the workspace.
func (s *Service) writeFile(ctx context.Context, target, content string) (string, error) {
	resolved, root, err := ResolveWithinRoots(s.Policy.Filesystem.Roots, target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDenied, err)
	}
	if prot, hit := s.Policy.protectedHit(resolved); hit {
		return "", fmt.Errorf("%w: protected path %s", ErrDenied, prot)
	}
	root, err = realPath(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := r.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		return "", err
	}
	mode := os.FileMode(0644)
	if info, err := r.Stat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("write target is not a regular file: %s", target)
		}
		mode = info.Mode().Perm()
	}
	tmp := filepath.Join(filepath.Dir(rel), ".water-write-"+NewCallID(s.Policy.Role))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", err
	}
	defer r.Remove(tmp)
	_, err = f.WriteString(content)
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := r.Rename(tmp, rel); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), resolved), nil
}
