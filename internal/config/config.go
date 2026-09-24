// Package config is a hand-rolled layered configuration:
// built-in defaults → ~/.water/config.yaml → env (WATER_*) → command flags.
// Every resolved value remembers which layer set it (`water config`).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CurrentSchema is the config file schema this binary writes.
const CurrentSchema = 1

// Config is the typed, fully-resolved configuration.
type Config struct {
	Schema   int            `yaml:"schema"`
	Backend  BackendConfig  `yaml:"backend"`
	Voice    VoiceConfig    `yaml:"voice"`
	API      APIConfig      `yaml:"api"`
	Onboard  OnboardConfig  `yaml:"onboard"`
	Sync     SyncConfig     `yaml:"sync"`
	Brief    BriefConfig    `yaml:"brief"`
	Meetings MeetingsConfig `yaml:"meetings"`
	Agent    AgentConfig    `yaml:"agent"`
}

// SyncConfig configures the daemon's background Google refresh
// (internal/sync): calendar and mail refresh on independent intervals, mail
// far more often since it is cheap to poll and staleness matters more.
type SyncConfig struct {
	IntervalMinutes     int `yaml:"interval_minutes"`
	MailIntervalSeconds int `yaml:"mail_interval_seconds"`
}

// Interval is IntervalMinutes as a time.Duration (the calendar cadence).
func (c SyncConfig) Interval() time.Duration { return time.Duration(c.IntervalMinutes) * time.Minute }

// MailInterval is MailIntervalSeconds as a time.Duration.
func (c SyncConfig) MailInterval() time.Duration {
	return time.Duration(c.MailIntervalSeconds) * time.Second
}

// BriefConfig configures the morning brief's background precompute
// (internal/runtime/brief.go via internal/sync's events tick).
type BriefConfig struct {
	// ReadyAfter is "HH:MM" local time; the precompute only fires once local
	// time is past this, so it doesn't try before the CEO's day realistically
	// starts.
	ReadyAfter string `yaml:"ready_after"`
}

// MeetingsConfig configures Slice M's live-meeting features.
type MeetingsConfig struct {
	// ProactiveCues gates GET /v1/meetings/{id}/cues (docs/slices/M.md
	// section 6): quiet, rate-limited related-item suggestions surfaced
	// from a live session's recent transcript. Off by default, following
	// this package's existing plain-bool-flag convention.
	ProactiveCues bool `yaml:"proactive_cues"`
}

// AgentConfig configures how outward actions identify themselves as the
// agent, never the CEO.
type AgentConfig struct {
	// MailAddress is the "Send mail as" alias (e.g. water.twin@gmail.com)
	// the CEO verifies on their real Gmail account through Gmail's own web
	// UI. gmail.send_message/draft_message set the MIME From: header to
	// this address; sending still uses the real account's existing OAuth
	// grant. Empty until the CEO sets it, after creating the alias account.
	MailAddress string `yaml:"mail_address"`
	// ForwardTo is the CEO's own real address: internal/agentmail's
	// inbound-triage watcher forwards a message it judges is meant for the
	// CEO (not the agent) here, as a level-A gmail.send_message approval.
	// Empty means such mail is logged but never staged, since there is
	// nowhere configured to send it.
	ForwardTo string `yaml:"forward_to"`
}

// OnboardConfig records the last verified round trip.
type OnboardConfig struct {
	VerifiedAt string `yaml:"verified_at"`
}

type BackendConfig struct {
	Preferred    string `yaml:"preferred"` // claude-subscription | codex-subscription | api | auto
	AllowMetered bool   `yaml:"allow_metered"`
}

// VoiceConfig configures the single CEO twin's optional speech output.
type VoiceConfig struct {
	Provider     string `yaml:"provider"` // os | openai | noop
	AllowMetered bool   `yaml:"allow_metered"`
	Model        string `yaml:"model"`
	CEOVoice     string `yaml:"ceo_voice"`
}

// APIConfig holds the metered backend's settings. Key is never written to the
// environment of any subprocess.
type APIConfig struct {
	Key   string `yaml:"key,omitempty"`
	Model string `yaml:"model,omitempty"`
}

// Layer names, in precedence order.
const (
	LayerDefault = "default"
	LayerFile    = "file"
	LayerEnv     = "env"
	LayerFlag    = "flag"
)

// Resolved is a Config plus, for every dotted key, the layer that set it.
type Resolved struct {
	Config
	Provenance map[string]string
	FilePath   string
	FileExists bool
}

// Keys lists all known dotted keys.
func Keys() []string {
	ks := make([]string, 0, len(defaults()))
	for k := range defaults() {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func defaults() map[string]string {
	return map[string]string{
		"schema":                     strconv.Itoa(CurrentSchema),
		"backend.preferred":          "auto",
		"backend.allow_metered":      "false",
		"voice.provider":             "os",
		"voice.allow_metered":        "false",
		"voice.model":                "gpt-4o-mini-tts",
		"voice.ceo_voice":            "",
		"api.key":                    "",
		"api.model":                  "",
		"onboard.verified_at":        "",
		"sync.interval_minutes":      "10",
		"sync.mail_interval_seconds": "60",
		"brief.ready_after":          "07:00",
		"meetings.proactive_cues":    "false",
		"agent.mail_address":         "",
		"agent.forward_to":           "",
	}
}

// retiredPrefixes and retiredKeys name config sections and keys removed after
// the council-to-single-twin rebuild (orchestration, per-role sessions/tools/
// skills/ui/telemetry/memory knobs, and the COO/CTO/design voices). An old
// config.yaml that still sets them must keep loading, so Load ignores them
// instead of failing with "unknown key"; it still rejects anything else it
// doesn't recognize. There is no schema bump: nothing about the resolved
// shape of a *current* key changed, only which keys still exist.
var retiredPrefixes = []string{
	"orchestration.", "sessions.", "tools.", "skills.", "ui.", "telemetry.", "memory.",
}

var retiredKeys = map[string]bool{
	"voice.coo_voice":    true,
	"voice.cto_voice":    true,
	"voice.design_voice": true,
}

func retired(key string) bool {
	if retiredKeys[key] {
		return true
	}
	for _, p := range retiredPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// Load resolves configuration. flags are dotted-key overrides supplied by the
// CLI (only keys the user actually set).
func Load(flags map[string]string) (*Resolved, error) {
	flat := map[string]string{}
	prov := map[string]string{}
	for k, v := range defaults() {
		flat[k], prov[k] = v, LayerDefault
	}
	res := &Resolved{Provenance: prov, FilePath: Path()}

	if b, err := os.ReadFile(res.FilePath); err == nil {
		res.FileExists = true
		var raw map[string]any
		if err := yaml.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", res.FilePath, err)
		}
		raw, err = migrate(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", res.FilePath, err)
		}
		for k, v := range flatten("", raw) {
			if _, known := flat[k]; !known {
				if retired(k) {
					continue
				}
				return nil, fmt.Errorf("%s: unknown key %q", res.FilePath, k)
			}
			flat[k], prov[k] = v, LayerFile
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	for k := range flat {
		if k == "schema" {
			continue
		}
		env := "WATER_" + strings.ToUpper(strings.NewReplacer(".", "_").Replace(k))
		if v, ok := os.LookupEnv(env); ok {
			flat[k], prov[k] = v, LayerEnv
		}
	}
	for k, v := range flags {
		if _, known := flat[k]; !known {
			return nil, fmt.Errorf("unknown config key %q", k)
		}
		flat[k], prov[k] = v, LayerFlag
	}
	if err := res.apply(flat); err != nil {
		return nil, err
	}
	return res, nil
}

func (r *Resolved) apply(flat map[string]string) error {
	var err error
	atoi := func(k string) int {
		n, e := strconv.Atoi(flat[k])
		if e != nil && err == nil {
			err = fmt.Errorf("%s: %q is not an integer (set by %s)", k, flat[k], r.Provenance[k])
		}
		return n
	}
	abool := func(k string) bool {
		b, e := strconv.ParseBool(flat[k])
		if e != nil && err == nil {
			err = fmt.Errorf("%s: %q is not a boolean (set by %s)", k, flat[k], r.Provenance[k])
		}
		return b
	}
	r.Schema = atoi("schema")
	r.Backend.Preferred = flat["backend.preferred"]
	r.Backend.AllowMetered = abool("backend.allow_metered")
	r.Voice.Provider = flat["voice.provider"]
	r.Voice.AllowMetered = abool("voice.allow_metered")
	r.Voice.Model = flat["voice.model"]
	r.Voice.CEOVoice = flat["voice.ceo_voice"]
	r.API.Key = flat["api.key"]
	r.API.Model = flat["api.model"]
	r.Onboard.VerifiedAt = flat["onboard.verified_at"]
	r.Sync.IntervalMinutes = atoi("sync.interval_minutes")
	r.Sync.MailIntervalSeconds = atoi("sync.mail_interval_seconds")
	r.Brief.ReadyAfter = flat["brief.ready_after"]
	r.Meetings.ProactiveCues = abool("meetings.proactive_cues")
	r.Agent.MailAddress = flat["agent.mail_address"]
	r.Agent.ForwardTo = flat["agent.forward_to"]
	if v := r.Brief.ReadyAfter; v != "" && err == nil {
		// Parsed the same way internal/sync's readyTime does; a bad value
		// there only logs on every tick and never precomputes the brief.
		if _, e := time.Parse("15:04", v); e != nil {
			err = fmt.Errorf("brief.ready_after: %q is not HH:MM (set by %s)", v, r.Provenance["brief.ready_after"])
		}
	}
	return err
}

// intKeys and boolKeys are the non-string keys (see apply). Save stores these
// as YAML ints/bools and every other key as a string, so a string value that
// merely looks numeric or boolean ("0123", "t") is kept verbatim.
var (
	intKeys  = map[string]bool{"schema": true, "sync.interval_minutes": true, "sync.mail_interval_seconds": true}
	boolKeys = map[string]bool{"backend.allow_metered": true, "voice.allow_metered": true, "meetings.proactive_cues": true}
)

// Flat returns the resolved values as dotted keys (for `water config`).
func (r *Resolved) Flat() map[string]string {
	return map[string]string{
		"schema":                     strconv.Itoa(r.Schema),
		"backend.preferred":          r.Backend.Preferred,
		"backend.allow_metered":      strconv.FormatBool(r.Backend.AllowMetered),
		"voice.provider":             r.Voice.Provider,
		"voice.allow_metered":        strconv.FormatBool(r.Voice.AllowMetered),
		"voice.model":                r.Voice.Model,
		"voice.ceo_voice":            r.Voice.CEOVoice,
		"api.key":                    mask(r.API.Key),
		"api.model":                  r.API.Model,
		"onboard.verified_at":        r.Onboard.VerifiedAt,
		"sync.interval_minutes":      strconv.Itoa(r.Sync.IntervalMinutes),
		"sync.mail_interval_seconds": strconv.Itoa(r.Sync.MailIntervalSeconds),
		"brief.ready_after":          r.Brief.ReadyAfter,
		"meetings.proactive_cues":    strconv.FormatBool(r.Meetings.ProactiveCues),
		"agent.mail_address":         r.Agent.MailAddress,
		"agent.forward_to":           r.Agent.ForwardTo,
	}
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "********"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func flatten(prefix string, m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch t := v.(type) {
		case map[string]any:
			for kk, vv := range flatten(key, t) {
				out[kk] = vv
			}
		case nil:
			out[key] = ""
		default:
			out[key] = fmt.Sprint(t)
		}
	}
	return out
}

// migrate upgrades an on-disk config map to CurrentSchema. Each schema bump
// adds a case here; the path exists from day one so migrations are additive.
func migrate(raw map[string]any) (map[string]any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	schema := 1
	if v, ok := raw["schema"]; ok {
		switch t := v.(type) {
		case int:
			schema = t
		case float64:
			schema = int(t)
		case string:
			n, err := strconv.Atoi(t)
			if err != nil {
				return nil, fmt.Errorf("schema: %q is not an integer", t)
			}
			schema = n
		}
	}
	for schema < CurrentSchema {
		switch schema {
		// case 1: ... upgrade 1→2 here
		default:
			return nil, fmt.Errorf("no migration from schema %d", schema)
		}
	}
	if schema > CurrentSchema {
		return nil, fmt.Errorf("config schema %d is newer than this binary supports (%d); upgrade water", schema, CurrentSchema)
	}
	raw["schema"] = CurrentSchema
	return raw, nil
}

// Save writes only the file layer: the given key/values merged into the
// existing file (or a fresh one). It never persists env/flag values. Values
// are validated exactly as Load would parse them, and the existing file is
// read and migrated exactly as Load does, before anything is written; on any
// error the file is left untouched.
func Save(set map[string]string) error {
	flat := defaults()
	prov := map[string]string{}
	for k, v := range set {
		if k == "schema" {
			return errors.New("schema is managed by water, not settable")
		}
		if _, known := flat[k]; !known {
			return fmt.Errorf("unknown config key %q", k)
		}
		flat[k], prov[k] = v, LayerFile
	}
	if err := (&Resolved{Provenance: prov}).apply(flat); err != nil {
		return err
	}

	p := Path()
	var raw map[string]any
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(b, &raw); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	// migrate stamps CurrentSchema, and refuses a file newer than this binary.
	if raw, err = migrate(raw); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	for k, v := range set {
		setNested(raw, strings.Split(k, "."), typed(k, v))
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("# water configuration (schema %d). Layers: defaults → this file → WATER_* env → flags.\n", CurrentSchema)
	return os.WriteFile(p, append([]byte(header), out...), 0o600)
}

func setNested(m map[string]any, path []string, v any) {
	if len(path) == 1 {
		m[path[0]] = v
		return
	}
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[path[0]] = child
	}
	setNested(child, path[1:], v)
}

// typed returns the YAML value for key k: an int or bool for the typed keys
// (already validated by Save), the raw string for everything else.
func typed(k, s string) any {
	switch {
	case intKeys[k]:
		n, _ := strconv.Atoi(s)
		return n
	case boolKeys[k]:
		b, _ := strconv.ParseBool(s)
		return b
	}
	return s
}
