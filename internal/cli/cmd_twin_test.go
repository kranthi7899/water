package cli

import (
	"strings"
	"testing"

	"water"
	"water/internal/twinlink"
	"water/internal/twins"
	"water/internal/vault"
)

// TestTwinFlagSelectsAnyTwinAndCounterpartyLoads is Slice E's second twin:
// --twin (or WATER_TWIN) names any twins/<id> directory, taking precedence
// over --demo, and the shipped counterparty manifest loads through the same
// validation path the daemon uses, granting only the twin link — sending
// at level A, reading at level R — and nothing else.
func TestTwinFlagSelectsAnyTwinAndCounterpartyLoads(t *testing.T) {
	a := NewApp()
	a.flags.demo = true
	a.flags.twin = "counterparty"
	if got := a.twinID(); got != "counterparty" {
		t.Fatalf("--twin counterparty --demo resolved %q", got)
	}
	t.Setenv(twinEnvVar, "counterparty")
	if got := NewApp().twinID(); got != "counterparty" {
		t.Fatalf("WATER_TWIN=counterparty resolved %q", got)
	}

	m, err := loadTwinManifest(water.TwinsFS(), "counterparty", "", "", "")
	if err != nil {
		t.Fatalf("counterparty manifest must load with no code of its own: %v", err)
	}
	ids := m.FunctionIDs()
	if len(ids) != 2 {
		t.Fatalf("counterparty grants %v, want only the twin link", ids)
	}
	if f, _ := m.Function("twinlink.send_message"); f.Level != twins.A {
		t.Fatalf("twinlink.send_message level = %q, want A", f.Level)
	}
	if f, _ := m.Function("twininbox.list_messages"); f.Level != twins.R {
		t.Fatalf("twininbox.list_messages level = %q, want R", f.Level)
	}
	real, err := twins.Load(water.TwinsFS(), realTwinID)
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := real.Function("twinlink.send_message"); !ok || f.Level != twins.A || real.AutoAllowed("twinlink.send_message") {
		t.Fatalf("real manifest twinlink.send_message = %+v ok=%v; want level A and never auto", f, ok)
	}
	if !strings.Contains(loadRoleMD("counterparty"), "counterparty twin") || !strings.Contains(loadRoleMD(demoTwinID), "The CEO twin") {
		t.Fatal("role.md: a twin's own, else the CEO's")
	}
}

// TestPeerTableRoundTripsThroughTheVault: `water twin peer add/remove`'s
// storage, keyed by the twin's own id so two daemons on one machine (one
// Keychain) keep separate tables.
func TestPeerTableRoundTripsThroughTheVault(t *testing.T) {
	v := vault.NewMemory()
	p, err := loadPeers(v, "ceo")
	if err != nil || len(p) != 0 {
		t.Fatalf("empty table: %v %v", p, err)
	}
	p["counterparty"] = twinlink.Peer{Socket: "/tmp/cp.sock", Token: "tok"}
	if err := savePeers(v, "ceo", p); err != nil {
		t.Fatal(err)
	}
	if other, _ := loadPeers(v, "counterparty"); len(other) != 0 {
		t.Fatal("another twin's table must be separate")
	}
	got, err := loadPeers(v, "ceo")
	if err != nil || got["counterparty"].Token != "tok" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	delete(got, "counterparty")
	if err := savePeers(v, "ceo", got); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(twinlink.VaultService, "ceo"); err == nil {
		t.Fatal("an empty table should delete the vault entry")
	}
}
