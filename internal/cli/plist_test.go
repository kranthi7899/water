package cli

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestRenderLaunchAgentPlistIsValidXMLAndCarriesTheRightFields(t *testing.T) {
	out := renderLaunchAgentPlist(launchAgentPlistArgs{
		Label:        "com.water.daemon",
		WaterBin:     "/opt/homebrew/bin/water",
		ExtraPathDir: "/opt/homebrew/bin",
		StdoutLog:    "/Users/ceo/.water/logs/daemon.log",
		StderrLog:    "/Users/ceo/.water/logs/daemon.err.log",
	})
	var v any
	if err := xml.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not well-formed XML: %v\n%s", err, out)
	}
	for _, want := range []string{
		"<string>com.water.daemon</string>",
		"<string>/opt/homebrew/bin/water</string>",
		"<string>daemon</string>",
		"<string>/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>",
		"<string>/Users/ceo/.water/logs/daemon.log</string>",
		"<string>/Users/ceo/.water/logs/daemon.err.log</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q\n%s", want, out)
		}
	}
}

func TestRenderLaunchAgentPlistEscapesPaths(t *testing.T) {
	out := renderLaunchAgentPlist(launchAgentPlistArgs{
		Label: "l", WaterBin: `/tmp/a & b/water`, ExtraPathDir: "/x", StdoutLog: "/o", StderrLog: "/e",
	})
	if strings.Contains(out, "a & b") {
		t.Fatalf("unescaped ampersand would produce malformed XML:\n%s", out)
	}
	var v any
	if err := xml.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not well-formed XML: %v\n%s", err, out)
	}
}
