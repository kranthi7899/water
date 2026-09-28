package gate_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// headroomManifest gives list_messages a cap of 4 (background may use 3),
// read_doc a cap of 2 (background may use 1) and list_events a cap of 1
// (background still gets 1: the floor never reaches zero). All three are
// on the auto allowlist so P2 exercises the same rule as P1.
const headroomManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 3, auto_model_calls: 1}
connectors:
  - name: fake_calendar
    functions:
      - {name: list_events, level: R, rate: {max: 1, per: 1h}}
  - name: fake_mail
    functions:
      - {name: list_messages, level: R, rate: {max: 4, per: 1h}}
  - name: fake_docs
    functions:
      - {name: read_doc, level: R, rate: {max: 2, per: 1h}}
auto_allowlist: [fake_mail.list_messages, fake_docs.read_doc, fake_calendar.list_events]
`

// headroomGates returns one store-backed gate and one in-memory gate over
// headroomManifest, sharing a clock the test advances, so every assertion
// runs against both rate-window paths (takeStoreLocked and live).
func headroomGates(t *testing.T) (map[string]*gate.Gate, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	out := map[string]*gate.Gate{}
	for _, name := range []string{"store", "memory"} {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "water.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		log, err := audit.Open(filepath.Join(dir, "audit", "audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { log.Close() })
		m, err := twins.Parse([]byte(headroomManifest))
		if err != nil {
			t.Fatal(err)
		}
		reg, err := connectors.NewRegistry(fake.NewCalendar(), fake.NewMail(fake.Message{ID: "m1", From: "a@x.com", Subject: "s", Body: "b"}), fake.NewDocs(fake.Doc{ID: "d1", Title: "Plan", Body: "x"}))
		if err != nil {
			t.Fatal(err)
		}
		v := vault.NewMemory()
		v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-123"))
		cfg := gate.Config{Manifest: m, Registry: reg, Approvals: approvals.NewQueue(st, log), Audit: log, Vault: v, Now: clock}
		if name == "store" {
			cfg.Store = st
		}
		g, err := gate.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = g
	}
	return out, &now
}

func invoke(g *gate.Gate, fn string, o gate.Origin) error {
	args := map[string]any{}
	if fn == "fake_docs.read_doc" {
		args["id"] = "d1"
	}
	_, err := g.Invoke(context.Background(), gate.Call{Function: fn, Args: args, Origin: o, Taint: gate.Clean})
	return err
}

// TestBackgroundOriginsLeaveRateHeadroomForP0 is the P0 reserve: a
// non-P0 origin may use at most 75% of a function's rate cap (rounded down,
// at least 1), so background work can never lock the CEO's own questions
// out, while P0 may still use the whole cap.
func TestBackgroundOriginsLeaveRateHeadroomForP0(t *testing.T) {
	gates, _ := headroomGates(t)
	for name, g := range gates {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				if err := invoke(g, "fake_mail.list_messages", gate.P1); err != nil {
					t.Fatalf("background call %d of 3 refused: %v", i+1, err)
				}
			}
			for _, o := range []gate.Origin{gate.P1, gate.P2} {
				err := invoke(g, "fake_mail.list_messages", o)
				denied(t, err, "reserved for P0")
				if !strings.Contains(err.Error(), "background origin "+string(o)) || !strings.Contains(err.Error(), "3 of 4") {
					t.Fatalf("%s denial must name the origin and the reserve: %v", o, err)
				}
			}
			// The CEO still has the reserved call...
			if err := invoke(g, "fake_mail.list_messages", gate.P0); err != nil {
				t.Fatalf("P0 locked out by background use: %v", err)
			}
			// ...and then the ordinary full cap holds for P0 too.
			err := invoke(g, "fake_mail.list_messages", gate.P0)
			denied(t, err, "rate cap for fake_mail.list_messages reached (4 per")
			if strings.Contains(err.Error(), "reserved") {
				t.Fatalf("a P0 denial at the full cap is not a reserve denial: %v", err)
			}
		})
	}
}

// TestP0HitsCountTowardTheBackgroundShare: the window is one shared
// count, so P0's own use also shrinks what background may still take (the
// reserve is headroom above background's share, not a separate bucket).
func TestP0HitsCountTowardTheBackgroundShare(t *testing.T) {
	gates, _ := headroomGates(t)
	for name, g := range gates {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				if err := invoke(g, "fake_mail.list_messages", gate.P0); err != nil {
					t.Fatal(err)
				}
			}
			denied(t, invoke(g, "fake_mail.list_messages", gate.P1), "reserved for P0")
			if err := invoke(g, "fake_mail.list_messages", gate.P0); err != nil {
				t.Fatalf("P0's fourth call refused: %v", err)
			}
		})
	}
}

// TestBackgroundRateShareRoundsDownWithAFloorOfOne covers the rounding: a
// cap of 2 leaves background 1 (75% of 2 rounds down), and a cap of 1
// leaves background 1 (never zero, which would silently disable a
// background-only function). The window still expires as before.
func TestBackgroundRateShareRoundsDownWithAFloorOfOne(t *testing.T) {
	gates, now := headroomGates(t)
	for name, g := range gates {
		t.Run(name, func(t *testing.T) {
			if err := invoke(g, "fake_docs.read_doc", gate.P2); err != nil {
				t.Fatal(err)
			}
			denied(t, invoke(g, "fake_docs.read_doc", gate.P2), "1 of 2")
			if err := invoke(g, "fake_docs.read_doc", gate.P0); err != nil {
				t.Fatalf("P0 refused its reserved read_doc: %v", err)
			}

			if err := invoke(g, "fake_calendar.list_events", gate.P1); err != nil {
				t.Fatalf("a cap of 1 must still allow background one call: %v", err)
			}
			denied(t, invoke(g, "fake_calendar.list_events", gate.P0), "rate cap for fake_calendar.list_events reached (1 per")
		})
	}
	*now = now.Add(time.Hour)
	for name, g := range gates {
		if err := invoke(g, "fake_docs.read_doc", gate.P2); err != nil {
			t.Fatalf("%s: after the window background must be allowed again: %v", name, err)
		}
	}
}
