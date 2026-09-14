package guards_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/tools"
)

// TestToolRootsCannotReachWaterHome — invariant #2 through the tool layer: a
// declared root that contains water's home (for example "~") must not let one
// role read another role's memory, transcripts, traces or the keyring.
func TestToolRootsCannotReachWaterHome(t *testing.T) {
	user := t.TempDir()
	home := filepath.Join(user, ".water")
	os.MkdirAll(filepath.Join(home, "memory", "design"), 0o755)
	os.WriteFile(filepath.Join(home, "memory", "design", "session.md"), []byte("DESIGN-PRIVATE"), 0o644)
	os.WriteFile(filepath.Join(home, "keyring"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(user, "notes.txt"), []byte("fine"), 0o644)
	os.Symlink(filepath.Join(home, "memory"), filepath.Join(user, "mem-link"))

	p := &tools.Policy{Role: "cto", Filesystem: tools.FSPolicy{Mode: "read-write", Roots: []string{user}}, Protected: []string{home}}
	svc := tools.NewService(p, nil)
	ctx := context.Background()
	if out, err := svc.Call(ctx, tools.ToolReadFile, map[string]any{"path": "notes.txt"}); err != nil || !strings.HasSuffix(out, "fine") {
		t.Fatalf("ordinary read under the root: %q %v", out, err)
	}
	for _, path := range []string{".water/memory/design/session.md", ".water/keyring", "mem-link/design/session.md", filepath.Join(home, "memory")} {
		if out, err := svc.Call(ctx, tools.ToolReadFile, map[string]any{"path": path}); err == nil {
			t.Fatalf("read of %s succeeded: %q", path, out)
		}
	}
	if _, err := svc.Call(ctx, tools.ToolWriteFile, map[string]any{"path": ".water/memory/design/session.md", "content": "x"}); err == nil {
		t.Fatal("write into water home succeeded")
	}
	if _, err := svc.Call(ctx, tools.ToolListDir, map[string]any{"path": ".water"}); err == nil {
		t.Fatal("listing water home succeeded")
	}
}
