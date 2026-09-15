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
	Schema        int                 `yaml:"schema"`
	Backend       BackendConfig       `yaml:"backend"`
	Memory        MemoryConfig        `yaml:"memory"`
	Voice         VoiceConfig         `yaml:"voice"`
	Orchestration OrchestrationConfig `yaml:"orchestration"`
	Telemetry     TelemetryConfig     `yaml:"telemetry"`
	API           APIConfig           `yaml:"api"`
	Sessions      SessionsConfig      `yaml:"sessions"`
	Tools         ToolsConfig         `yaml:"tools"`
	Skills        SkillsConfig        `yaml:"skills"`
	UI            UIConfig            `yaml:"ui"`
	Onboard       OnboardConfig       `yaml:"onboard"`
}

// SessionsConfig is transcript retention (Part 4.3): count-based with an age
// backstop; pinned sessions are exempt.
type SessionsConfig struct {
	Keep   int    `yaml:"keep"`
	MaxAge string `yaml:"max_age"`
}

// ToolsConfig enables Water's tool layer for roles whose role.yaml declares a
// tools block, and names the roots they may read. Roots are never inherited
// from the working directory (Part 5.2).
type ToolsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Roots   string `yaml:"roots"` // comma-separated absolute or ~-paths
}

// SkillsConfig picks the skill selector.
type SkillsConfig struct {
	Selector string `yaml:"selector"` // description | keyword
}

// UIConfig holds interactive-session knobs.
type UIConfig struct {
	Theme string `yaml:"theme"` // "" = per-role theme; a name forces one theme
}

// OnboardConfig records the last verified round trip.
type OnboardConfig struct {
	VerifiedAt string `yaml:"verified_at"`
}

// RootList splits tools.roots.
func (c *Config) RootList() []string {
	var out []string
	for _, r := range strings.Split(c.Tools.Roots, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, Expand(r))
		}
	}
	return out
}

// SessionMaxAge parses sessions.max_age (0 = disabled).
func (c *Config) SessionMaxAge() time.Duration {
	d, err := time.ParseDuration(c.Sessions.MaxAge)
	if err != nil {
		return 0
	}
	return d
}

type BackendConfig struct {
	Preferred    string `yaml:"preferred"` // claude-subscription | codex-subscription | api | auto
	AllowMetered bool   `yaml:"allow_metered"`
}

type MemoryConfig struct {
	Provider   string `yaml:"provider"`
	MaxEntries int    `yaml:"max_entries"`
	MaxBytes   int    `yaml:"max_bytes"`
}

type VoiceConfig struct {
	Provider     string `yaml:"provider"` // os | openai | noop
	AllowMetered bool   `yaml:"allow_metered"`
	Model        string `yaml:"model"`
	CEOVoice     string `yaml:"ceo_voice"`
	COOVoice     string `yaml:"coo_voice"`
	CTOVoice     string `yaml:"cto_voice"`
	DesignVoice  string `yaml:"design_voice"`
}

// VoiceFor returns the selected voice identifier for a role. Empty values are
// intentional: providers then use their own safe profile defaults.
func (c VoiceConfig) VoiceFor(role string) string {
	switch role {
	case "ceo":
		return c.CEOVoice
	case "coo":
		return c.COOVoice
	case "cto":
		return c.CTOVoice
	case "design":
		return c.DesignVoice
	default:
		return ""
	}
}

type OrchestrationConfig struct {
	Router        string `yaml:"router"`
	MaxParallel   int    `yaml:"max_parallel"`
	Timeout       string `yaml:"timeout"`        // whole-run ceiling
	CallTimeout   string `yaml:"call_timeout"`   // one model call
	MaxSteps      int    `yaml:"max_steps"`      // hard step budget per run
	MaxRounds     int    `yaml:"max_rounds"`     // COO assignment rounds (depth cap)
	Checkpointer  string `yaml:"checkpointer"`   // file | noop
	CheckpointDir string `yaml:"checkpoint_dir"` // where run snapshots live
}

type TelemetryConfig struct {
	TraceDir string `yaml:"trace_dir"`
}

// APIConfig holds the metered backend's settings. Key is never written to the
// environment of any subprocess.
type APIConfig struct {
	Key   string `yaml:"key,omitempty"`
	Model string `yaml:"model,omitempty"`
}

// TimeoutDuration parses orchestration.timeout (the whole-run ceiling).
func (c *Config) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Orchestration.Timeout)
	if err != nil {
		return 20 * time.Minute
	}
	return d
}

// CallTimeoutDuration parses orchestration.call_timeout (one model call).
func (c *Config) CallTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Orchestration.CallTimeout)
	if err != nil {
		return 4 * time.Minute
	}
	return d
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
		"schema":                       strconv.Itoa(CurrentSchema),
		"backend.preferred":            "auto",
		"backend.allow_metered":        "false",
		"memory.provider":              "markdown",
		"memory.max_entries":           "200",
		"memory.max_bytes":             "32768",
		"voice.provider":               "os",
		"voice.allow_metered":          "false",
		"voice.model":                  "gpt-4o-mini-tts",
		"voice.ceo_voice":              "",
		"voice.coo_voice":              "",
		"voice.cto_voice":              "",
		"voice.design_voice":           "",
		"orchestration.router":         "hierarchy",
		"orchestration.max_parallel":   "4",
		"orchestration.timeout":        "20m",
		"orchestration.call_timeout":   "4m",
		"orchestration.max_steps":      "24",
		"orchestration.max_rounds":     "2",
		"orchestration.checkpointer":   "file",
		"orchestration.checkpoint_dir": filepath.Join(Home(), "checkpoints"),
		"telemetry.trace_dir":          filepath.Join(Home(), "traces"),
		"api.key":                      "",
		"api.model":                    "",
		"sessions.keep":                "30",
		"sessions.max_age":             "2160h",
		"tools.enabled":                "false",
		"tools.roots":                  "",
		"skills.selector":              "description",
		"ui.theme":                     "",
		"onboard.verified_at":          "",
	}
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
	r.Memory.Provider = flat["memory.provider"]
	r.Memory.MaxEntries = atoi("memory.max_entries")
	r.Memory.MaxBytes = atoi("memory.max_bytes")
	r.Voice.Provider = flat["voice.provider"]
	r.Voice.AllowMetered = abool("voice.allow_metered")
	r.Voice.Model = flat["voice.model"]
	r.Voice.CEOVoice = flat["voice.ceo_voice"]
	r.Voice.COOVoice = flat["voice.coo_voice"]
	r.Voice.CTOVoice = flat["voice.cto_voice"]
	r.Voice.DesignVoice = flat["voice.design_voice"]
	r.Orchestration.Router = flat["orchestration.router"]
	r.Orchestration.MaxParallel = atoi("orchestration.max_parallel")
	r.Orchestration.Timeout = flat["orchestration.timeout"]
	r.Orchestration.CallTimeout = flat["orchestration.call_timeout"]
	r.Orchestration.MaxSteps = atoi("orchestration.max_steps")
	r.Orchestration.MaxRounds = atoi("orchestration.max_rounds")
	r.Orchestration.Checkpointer = flat["orchestration.checkpointer"]
	r.Orchestration.CheckpointDir = Expand(flat["orchestration.checkpoint_dir"])
	r.Telemetry.TraceDir = Expand(flat["telemetry.trace_dir"])
	r.API.Key = flat["api.key"]
	r.API.Model = flat["api.model"]
	r.Sessions.Keep = atoi("sessions.keep")
	r.Sessions.MaxAge = flat["sessions.max_age"]
	r.Tools.Enabled = abool("tools.enabled")
	r.Tools.Roots = flat["tools.roots"]
	r.Skills.Selector = flat["skills.selector"]
	r.UI.Theme = flat["ui.theme"]
	r.Onboard.VerifiedAt = flat["onboard.verified_at"]
	if err != nil {
		return err
	}
	if _, e := time.ParseDuration(r.Orchestration.Timeout); e != nil {
		return fmt.Errorf("orchestration.timeout: %q is not a duration", r.Orchestration.Timeout)
	}
	if _, e := time.ParseDuration(r.Orchestration.CallTimeout); e != nil {
		return fmt.Errorf("orchestration.call_timeout: %q is not a duration", r.Orchestration.CallTimeout)
	}
	if r.Sessions.MaxAge != "" && r.Sessions.MaxAge != "0" {
		if _, e := time.ParseDuration(r.Sessions.MaxAge); e != nil {
			return fmt.Errorf("sessions.max_age: %q is not a duration", r.Sessions.MaxAge)
		}
	}
	return nil
}

// Flat returns the resolved values as dotted keys (for `water config`).
func (r *Resolved) Flat() map[string]string {
	return map[string]string{
		"schema":                       strconv.Itoa(r.Schema),
		"backend.preferred":            r.Backend.Preferred,
		"backend.allow_metered":        strconv.FormatBool(r.Backend.AllowMetered),
		"memory.provider":              r.Memory.Provider,
		"memory.max_entries":           strconv.Itoa(r.Memory.MaxEntries),
		"memory.max_bytes":             strconv.Itoa(r.Memory.MaxBytes),
		"voice.provider":               r.Voice.Provider,
		"voice.allow_metered":          strconv.FormatBool(r.Voice.AllowMetered),
		"voice.model":                  r.Voice.Model,
		"voice.ceo_voice":              r.Voice.CEOVoice,
		"voice.coo_voice":              r.Voice.COOVoice,
		"voice.cto_voice":              r.Voice.CTOVoice,
		"voice.design_voice":           r.Voice.DesignVoice,
		"orchestration.router":         r.Orchestration.Router,
		"orchestration.max_parallel":   strconv.Itoa(r.Orchestration.MaxParallel),
		"orchestration.timeout":        r.Orchestration.Timeout,
		"orchestration.call_timeout":   r.Orchestration.CallTimeout,
		"orchestration.max_steps":      strconv.Itoa(r.Orchestration.MaxSteps),
		"orchestration.max_rounds":     strconv.Itoa(r.Orchestration.MaxRounds),
		"orchestration.checkpointer":   r.Orchestration.Checkpointer,
		"orchestration.checkpoint_dir": r.Orchestration.CheckpointDir,
		"telemetry.trace_dir":          r.Telemetry.TraceDir,
		"api.key":                      mask(r.API.Key),
		"api.model":                    r.API.Model,
		"sessions.keep":                strconv.Itoa(r.Sessions.Keep),
		"sessions.max_age":             r.Sessions.MaxAge,
		"tools.enabled":                strconv.FormatBool(r.Tools.Enabled),
		"tools.roots":                  r.Tools.Roots,
		"skills.selector":              r.Skills.Selector,
		"ui.theme":                     r.UI.Theme,
		"onboard.verified_at":          r.Onboard.VerifiedAt,
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
// existing file (or a fresh one). It never persists env/flag values.
func Save(set map[string]string) error {
	p := Path()
	raw := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		if err := yaml.Unmarshal(b, &raw); err != nil {
			return err
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}
	raw["schema"] = CurrentSchema
	for k, v := range set {
		if _, known := defaults()[k]; !known {
			return fmt.Errorf("unknown config key %q", k)
		}
		setNested(raw, strings.Split(k, "."), coerce(v))
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	header := "# water configuration (schema 1). Layers: defaults → this file → WATER_* env → flags.\n"
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

func coerce(s string) any {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	if b, err := strconv.ParseBool(s); err == nil {
		return b
	}
	return s
}
