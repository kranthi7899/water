package cli

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// launchAgentPlistArgs is the pure input to renderLaunchAgentPlist, kept
// separate from any os/exec call so the rendering itself is unit-testable
// without touching the filesystem or launchd.
type launchAgentPlistArgs struct {
	Label        string
	WaterBin     string
	ExtraPathDir string // claude's directory, prepended to launchd's minimal PATH
	StdoutLog    string
	StderrLog    string
}

// renderLaunchAgentPlist renders the LaunchAgent property list that starts
// `water daemon` on login and keeps it alive. It escapes every value that
// comes from the filesystem (paths can contain XML-significant characters).
func renderLaunchAgentPlist(a launchAgentPlistArgs) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	path := a.ExtraPathDir + ":/usr/bin:/bin:/usr/sbin:/sbin"
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, esc(a.Label), esc(a.WaterBin), esc(path), esc(a.StdoutLog), esc(a.StderrLog))
}
