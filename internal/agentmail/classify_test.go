package agentmail_test

import (
	"context"
	"errors"
	"testing"

	"water/internal/agentmail"
	"water/internal/backend"
	"water/internal/store"
)

func TestClassifierReadsForCEOFromTheModelReply(t *testing.T) {
	cases := []struct {
		name     string
		reply    string
		wantNeed bool
		wantType string
	}{
		{"ceo directed", `{"for_ceo": true, "confidence": 0.9}`, true, "ceo_directed"},
		{"agent directed", `{"for_ceo": false, "confidence": 0.8}`, false, "agent_directed"},
		{"wrapped in prose", "Sure, here you go: {\"for_ceo\": true, \"confidence\": 0.5} thanks", true, "ceo_directed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb := backend.NewFake("fake")
			fb.Reply = func(backend.Request) string { return tc.reply }
			c := &agentmail.Classifier{Backend: fb, Model: "fake"}
			got, err := c.Classify(context.Background(), &store.Message{From: "x@y.com", Subject: "s", Body: "b"})
			if err != nil {
				t.Fatal(err)
			}
			if got.NeedsDecision != tc.wantNeed || got.TypeID != tc.wantType || got.Fallback {
				t.Fatalf("Classify() = %+v, want NeedsDecision=%v TypeID=%q Fallback=false", got, tc.wantNeed, tc.wantType)
			}
		})
	}
}

// TestClassifierFallbackTreatsAnUnreadableReplyAsCEODirected: an unreadable
// verdict is never silently dropped as agent-only -- the conservative
// direction is to still stage it for the CEO to see, marked Fallback so a
// caller could choose to retry rather than trust it (mirroring decisions.
// ModelClassifier's own "never silently drop" rule for its own Fallback).
func TestClassifierFallbackTreatsAnUnreadableReplyAsCEODirected(t *testing.T) {
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string { return "not json at all" }
	c := &agentmail.Classifier{Backend: fb, Model: "fake"}
	got, err := c.Classify(context.Background(), &store.Message{From: "x@y.com", Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.NeedsDecision || !got.Fallback {
		t.Fatalf("Classify() on an unreadable reply = %+v, want NeedsDecision=true Fallback=true", got)
	}
}

func TestClassifierChargesOncePerCall(t *testing.T) {
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string { return `{"for_ceo": false, "confidence": 0.9}` }
	var charges int
	c := &agentmail.Classifier{Backend: fb, Model: "fake", Charge: func() error { charges++; return nil }}
	if _, err := c.Classify(context.Background(), &store.Message{From: "a", Subject: "b", Body: "c"}); err != nil {
		t.Fatal(err)
	}
	if charges != 1 {
		t.Fatalf("charges = %d, want 1", charges)
	}
}

func TestClassifierChargeRefusalStopsBeforeCallingTheBackend(t *testing.T) {
	fb := backend.NewFake("fake")
	var backendCalled bool
	fb.Reply = func(backend.Request) string { backendCalled = true; return `{"for_ceo": true, "confidence": 1}` }
	wantErr := errors.New("usage cap reached")
	c := &agentmail.Classifier{Backend: fb, Model: "fake", Charge: func() error { return wantErr }}
	_, err := c.Classify(context.Background(), &store.Message{From: "a", Subject: "b", Body: "c"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if backendCalled {
		t.Fatal("backend was called despite a charge refusal")
	}
}

func TestClassifierRequiresAMessageRecord(t *testing.T) {
	c := &agentmail.Classifier{Backend: backend.NewFake("fake"), Model: "fake"}
	if _, err := c.Classify(context.Background(), nil); err == nil {
		t.Fatal("Classify(nil) should error, not silently classify nothing")
	}
}
