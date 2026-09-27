package research

import (
	"context"
	"os/exec"
	"time"
)

// SetTimeout shortens a connector's call timeout (tests only).
func (c *Connector) SetTimeout(d time.Duration) { c.timeout = d }

// SetNow fixes a connector's clock (tests only).
func (c *Connector) SetNow(now func() time.Time) { c.now = now }

// Command exposes the subprocess builder (tests only; nothing is started).
func Command(ctx context.Context, path, dir string, args []string) *exec.Cmd {
	return command(ctx, path, dir, args)
}
