package nervous

import (
	"testing"
	"time"

	"water/internal/runtime"
)

func TestReadbacksRecordBoundVoidRoundTrip(t *testing.T) {
	r := NewReadbacks()
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

	if _, ok := r.Bound(runtime.ChannelVoice, now, time.Minute); ok {
		t.Fatal("nothing recorded yet: want not bound")
	}

	r.Record(Readback{Channel: runtime.ChannelVoice, EnvelopeID: "env_1", PayloadHash: "hash1", At: now})

	rb, ok := r.Bound(runtime.ChannelVoice, now, time.Minute)
	if !ok {
		t.Fatal("want bound right after recording")
	}
	if rb.EnvelopeID != "env_1" || rb.PayloadHash != "hash1" {
		t.Fatalf("got %+v, want env_1/hash1", rb)
	}

	r.Void("env_1")
	if _, ok := r.Bound(runtime.ChannelVoice, now, time.Minute); ok {
		t.Fatal("voided: want not bound")
	}
}

func TestReadbacksOlderThanWindowIsNotBound(t *testing.T) {
	r := NewReadbacks()
	start := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	r.Record(Readback{Channel: runtime.ChannelVoice, EnvelopeID: "env_1", PayloadHash: "hash1", At: start})

	within := start.Add(59 * time.Second)
	if _, ok := r.Bound(runtime.ChannelVoice, within, 60*time.Second); !ok {
		t.Fatal("59s within a 60s window: want bound")
	}

	after := start.Add(61 * time.Second)
	if _, ok := r.Bound(runtime.ChannelVoice, after, 60*time.Second); ok {
		t.Fatal("61s past a 60s window: want not bound")
	}
}

func TestReadbacksVoidUnknownIDIsNoop(t *testing.T) {
	r := NewReadbacks()
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	r.Record(Readback{Channel: runtime.ChannelVoice, EnvelopeID: "env_1", PayloadHash: "hash1", At: now})

	r.Void("env_never_recorded")
	r.Void("")

	if _, ok := r.Bound(runtime.ChannelVoice, now, time.Minute); !ok {
		t.Fatal("voiding an unrecorded id must not disturb an existing binding")
	}
}

func TestReadbacksAreScopedPerChannel(t *testing.T) {
	r := NewReadbacks()
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	r.Record(Readback{Channel: runtime.ChannelVoice, EnvelopeID: "env_1", PayloadHash: "hash1", At: now})

	if _, ok := r.Bound(runtime.ChannelCLI, now, time.Minute); ok {
		t.Fatal("a different channel must not see another channel's binding")
	}
	if _, ok := r.Bound(runtime.ChannelVoice, now, time.Minute); !ok {
		t.Fatal("the recorded channel should still be bound")
	}
}

func TestReadbacksRecordReplacesPreviousOnSameChannel(t *testing.T) {
	r := NewReadbacks()
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	r.Record(Readback{Channel: runtime.ChannelVoice, EnvelopeID: "env_1", PayloadHash: "hash1", At: now})
	r.Record(Readback{Channel: runtime.ChannelVoice, EnvelopeID: "env_2", PayloadHash: "hash2", At: now})

	rb, ok := r.Bound(runtime.ChannelVoice, now, time.Minute)
	if !ok || rb.EnvelopeID != "env_2" {
		t.Fatalf("got %+v, ok=%v, want env_2 (the latest)", rb, ok)
	}
}

func TestRecordReadbackIgnoresNonVoiceChannel(t *testing.T) {
	n := &Nervous{readbacks: NewReadbacks()}
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	n.RecordReadback(runtime.ChannelCLI, "env_1", "hash1", now)
	if _, ok := n.readbacks.Bound(runtime.ChannelCLI, now, time.Minute); ok {
		t.Fatal("RecordReadback must ignore a non-voice channel")
	}
	n.RecordReadback(runtime.ChannelVoice, "env_1", "hash1", now)
	if _, ok := n.readbacks.Bound(runtime.ChannelVoice, now, time.Minute); !ok {
		t.Fatal("RecordReadback must record a voice channel")
	}
}
