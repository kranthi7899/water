package voice

// The OS provider's preferred voice for the CEO twin. The engine is the
// system TTS, so this cannot make speech expressive; it just prefers a less
// robotic installed voice when one is present. macOS Premium/Enhanced voices
// (a free download under System Settings → Accessibility → Spoken Content)
// sound far less robotic, so this prefers one of those when installed and
// falls back to a voice that ships with every Mac.

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// OSProfile is the CEO twin's OS voice: candidates in preference order and a
// speaking rate. Linux uses the espeak variant instead of the macOS names.
type OSProfile struct {
	Voices []string
	Rate   int
	Espeak string
}

// CEOOSProfile is the one OS voice profile Water speaks with.
func CEOOSProfile() OSProfile {
	return OSProfile{[]string{"Jamie (Premium)", "Jamie (Enhanced)", "Evan (Premium)", "Evan (Enhanced)", "Daniel (Enhanced)", "Daniel"}, 172, "en+m3"}
}

// NewOSVoice returns the OS provider speaking in the CEO voice. override
// (from voice.ceo_voice) wins when it names an installed voice.
func NewOSVoice(override string) *OS {
	o := NewOS()
	o.applyProfile(override, installedVoices())
	return o
}

func (o *OS) applyProfile(override string, installed []string) {
	p := CEOOSProfile()
	o.rate = p.Rate
	switch runtime.GOOS {
	case "darwin":
		o.voice = pickVoice(override, p.Voices, installed)
	case "linux":
		if strings.HasPrefix(filepath.Base(o.bin), "espeak") {
			o.voice = p.Espeak
			if override != "" {
				o.voice = override // espeak cannot list variants reliably; trust the user
			}
		}
	}
}

// Voice reports the resolved voice ("" = system default).
func (o *OS) Voice() string { return o.voice }

// Rate reports the speaking rate in words per minute (0 = engine default).
func (o *OS) Rate() int { return o.rate }

// OverrideIgnored reports whether a configured override is not installed and
// was replaced by the profile default (macOS only, where voices can be listed).
func OverrideIgnored(override string) bool {
	if override == "" || runtime.GOOS != "darwin" {
		return false
	}
	return pickVoice(override, nil, installedVoices()) == ""
}

func (o *OS) voiceArgs() []string {
	var a []string
	switch filepath.Base(o.bin) {
	case "say":
		if o.voice != "" {
			a = append(a, "-v", o.voice)
		}
		if o.rate > 0 {
			a = append(a, "-r", strconv.Itoa(o.rate))
		}
	case "espeak", "espeak-ng":
		if o.voice != "" {
			a = append(a, "-v", o.voice)
		}
		if o.rate > 0 {
			a = append(a, "-s", strconv.Itoa(o.rate))
		}
	}
	return a
}

func pickVoice(override string, prefs, installed []string) string {
	has := make(map[string]string, len(installed))
	for _, v := range installed {
		has[strings.ToLower(v)] = v
	}
	if v, ok := has[strings.ToLower(strings.TrimSpace(override))]; ok {
		return v
	}
	for _, p := range prefs {
		if v, ok := has[strings.ToLower(p)]; ok {
			return v
		}
	}
	return ""
}

var (
	voicesOnce sync.Once
	voicesList []string
	// ListVoices is overridable in tests.
	ListVoices = func() []string {
		if runtime.GOOS != "darwin" {
			return nil
		}
		out, err := exec.Command("say", "-v", "?").Output()
		if err != nil {
			return nil
		}
		return parseSayVoices(string(out))
	}
)

func installedVoices() []string {
	voicesOnce.Do(func() { voicesList = ListVoices() })
	return voicesList
}

var sayLine = regexp.MustCompile(`^(.+?)\s+[a-z]{2,3}_[A-Z0-9]{2,3}\s+#`)

func parseSayVoices(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if m := sayLine.FindStringSubmatch(line); m != nil {
			names = append(names, strings.TrimSpace(m[1]))
		}
	}
	return names
}
