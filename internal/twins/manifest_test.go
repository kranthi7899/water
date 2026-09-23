package twins_test

import (
	"strings"
	"testing"
	"time"

	"water"
	"water/internal/twins"
)

func TestEmbeddedCEOManifestLoads(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	f, ok := m.Function("fake_mail.send_email")
	if !ok || f.Level != twins.A || f.Rate == nil || time.Duration(f.Rate.Per) != time.Hour {
		t.Fatalf("send_email: %+v %v", f, ok)
	}
	if !m.AutoAllowed("fake_mail.draft_reply") || m.AutoAllowed("fake_mail.send_email") {
		t.Fatal("auto allowlist wrong")
	}
	if _, ok := m.Function("fake_mail.delete_everything"); ok {
		t.Fatal("unlisted function resolved")
	}
}

const base = "id: t\nname: T\nusage: {window: 1h, model_calls: 5}\n"

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown level":        base + "connectors:\n  - name: c\n    functions:\n      - {name: f, level: X}\n",
		"lowercase level":      base + "connectors:\n  - name: c\n    functions:\n      - {name: f, level: r}\n",
		"missing level":        base + "connectors:\n  - name: c\n    functions:\n      - {name: f}\n",
		"duplicate function":   base + "connectors:\n  - name: c\n    functions:\n      - {name: f, level: R}\n      - {name: f, level: A}\n",
		"duplicate connector":  base + "connectors:\n  - name: c\n    functions: []\n  - name: c\n    functions: []\n",
		"unknown key":          base + "conectors: []\n",
		"allowlist A function": base + "connectors:\n  - name: c\n    functions:\n      - {name: f, level: A}\nauto_allowlist: [c.f]\n",
		"allowlist unlisted":   base + "auto_allowlist: [c.f]\n",
		"bad rate":             base + "connectors:\n  - name: c\n    functions:\n      - {name: f, level: R, rate: {max: 0, per: 1h}}\n",
		"no usage":             "id: t\nname: T\n",
		"auto over total":      "id: t\nname: T\nusage: {window: 1h, model_calls: 5, auto_model_calls: 6}\n",
	}
	for name, doc := range cases {
		if _, err := twins.Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := twins.Parse([]byte(base + "connectors:\n  - name: c\n    functions:\n      - {name: f, level: B}\n")); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if _, err := twins.Parse([]byte(strings.Replace(base, "id: t", "id: ''", 1))); err == nil {
		t.Error("empty id accepted")
	}
}
