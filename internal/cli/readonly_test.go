package cli

import (
	"os"
	"testing"

	"water/internal/audit"
)

// holdAuditLock stands in for a running daemon: it opens the audit log's
// single writer under a fresh WATER_HOME and keeps it open for the test.
func holdAuditLock(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	log, err := audit.Open(audit.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = devnull
	t.Cleanup(func() { os.Stdout = old; devnull.Close() })
}

// TestStatusWorksWhileTheDaemonHoldsTheAuditLog: `water status` only reads
// the manifest, so it must not need (or fight the daemon for) the audit
// log's exclusive writer lock.
func TestStatusWorksWhileTheDaemonHoldsTheAuditLog(t *testing.T) {
	holdAuditLock(t)
	cmd := NewApp().statusCmd()
	cmd.SetArgs([]string{"--no-probe"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status failed while the daemon holds the audit log: %v", err)
	}
}

// TestDoctorTwinCheckWorksWhileTheDaemonHoldsTheAuditLog: likewise for
// doctor's twin check, which must not report a failure just because the
// daemon is running.
func TestDoctorTwinCheckWorksWhileTheDaemonHoldsTheAuditLog(t *testing.T) {
	holdAuditLock(t)
	if c := doctorTwinCheck(realTwinID, "", "", ""); c.Status != "ok" {
		t.Fatalf("doctor twin check = %+v while the daemon holds the audit log, want ok", c)
	}
}
