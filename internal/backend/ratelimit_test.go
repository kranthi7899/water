package backend

import (
	"errors"
	"testing"
)

const streamFixture = `{"type":"system","subtype":"init","session_id":"x"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1789395600,"rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"utilization":0.27,"resetsAt":1789395600},"seven_day":{"utilization":0.31,"resetsAt":1789632000}}}}
{"type":"result","subtype":"success","is_error":false,"result":"hi","usage":{"input_tokens":10,"output_tokens":2}}
`

func TestParseRateLimitAndResult(t *testing.T) {
	rl := parseRateLimit(streamFixture)
	if rl == nil || rl.Status != "allowed" || rl.FiveHourUsed != 0.27 || rl.SevenDayUsed != 0.31 || rl.FiveHourResets.IsZero() {
		t.Fatalf("%+v", rl)
	}
	res, ok := parseClaudeResult(streamFixture)
	if !ok || res.Result != "hi" || res.Usage.InputTokens != 10 {
		t.Fatalf("%+v %v", res, ok)
	}
	if s := rl.Summary(); s == "" || !contains(s, "27% used") {
		t.Fatalf("summary %q", s)
	}
	if (*RateLimit)(nil).Summary() == "" {
		t.Fatal("nil summary")
	}
}

func TestRateLimitErrorClass(t *testing.T) {
	if !IsRateLimitText("You've hit your session limit · resets 7:20am (America/Los_Angeles)") {
		t.Fatal("session limit text not recognised")
	}
	if IsRateLimitText("permission denied") {
		t.Fatal("false positive")
	}
	err := errorf(ErrRateLimited, "x")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatal("wrap")
	}
}

func errorf(base error, s string) error { return &wrapped{base, s} }

type wrapped struct {
	base error
	s    string
}

func (w *wrapped) Error() string { return w.s }
func (w *wrapped) Unwrap() error { return w.base }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
