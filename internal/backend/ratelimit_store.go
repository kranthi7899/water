package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// SaveRateLimit persists the last observed window state so `water status`
// can show remaining subscription budget without spending a call.
func SaveRateLimit(home string, rl *RateLimit) error {
	if rl == nil {
		return nil
	}
	b, err := json.MarshalIndent(rl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "ratelimit.json"), b, 0o600)
}

// LoadRateLimit reads the persisted state (nil if none).
func LoadRateLimit(home string) *RateLimit {
	b, err := os.ReadFile(filepath.Join(home, "ratelimit.json"))
	if err != nil {
		return nil
	}
	var rl RateLimit
	if json.Unmarshal(b, &rl) != nil {
		return nil
	}
	return &rl
}

// Summary renders one status line.
func (rl *RateLimit) Summary() string {
	if rl == nil {
		return "no subscription budget observed yet (run any turn)"
	}
	s := rl.Backend + ": 5h window " + pct(rl.FiveHourUsed)
	if !rl.FiveHourResets.IsZero() {
		s += " (resets " + rl.FiveHourResets.Local().Format("15:04") + ")"
	}
	s += " · 7d window " + pct(rl.SevenDayUsed)
	if rl.Status != "" && rl.Status != "allowed" {
		s += " · " + rl.Status
	}
	s += " · observed " + time.Since(rl.ObservedAt).Round(time.Minute).String() + " ago"
	return s
}

func pct(f float64) string {
	return json.Number(formatPct(f)).String() + "% used"
}

func formatPct(f float64) string {
	n := int(f*100 + 0.5)
	if n < 0 {
		n = 0
	}
	if n > 100 {
		n = 100
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
