package sidecar

import (
	"path/filepath"
	"testing"
	"time"
)

func passingRecord() EvalRecord {
	return EvalRecord{
		ModelSHA256:   "modelsha",
		RegistryHash:  "registryhash",
		N:             400,
		FalseAccepts:  1,
		FARate:        0.0025,
		Wilson95Upper: 0.0199,
		WarmP95Ms:     350,
		At:            time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
	}
}

func TestGateCheckPasses(t *testing.T) {
	rec := passingRecord()
	ok, reason := rec.Check("modelsha", "registryhash")
	if !ok || reason != "" {
		t.Fatalf("expected pass, got ok=%v reason=%q", ok, reason)
	}
}

func TestGateCheckStaleModelSHA(t *testing.T) {
	rec := passingRecord()
	ok, reason := rec.Check("othersha", "registryhash")
	if ok || reason != ReasonEvalStale {
		t.Fatalf("expected stale, got ok=%v reason=%q", ok, reason)
	}
}

func TestGateCheckStaleRegistryHash(t *testing.T) {
	rec := passingRecord()
	ok, reason := rec.Check("modelsha", "otherhash")
	if ok || reason != ReasonEvalStale {
		t.Fatalf("expected stale, got ok=%v reason=%q", ok, reason)
	}
}

func TestGateCheckEachThreshold(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*EvalRecord)
	}{
		{"fa_rate over", func(r *EvalRecord) { r.FARate = 0.011 }},
		{"wilson upper over", func(r *EvalRecord) { r.Wilson95Upper = 0.021 }},
		{"n under", func(r *EvalRecord) { r.N = 199 }},
		{"warm p95 over", func(r *EvalRecord) { r.WarmP95Ms = 401 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := passingRecord()
			c.mutate(&rec)
			ok, reason := rec.Check("modelsha", "registryhash")
			if ok || reason != ReasonEvalFailed {
				t.Fatalf("expected failed, got ok=%v reason=%q", ok, reason)
			}
		})
	}
}

func TestGateCheckBoundaryValuesPass(t *testing.T) {
	rec := passingRecord()
	rec.FARate = 0.01
	rec.Wilson95Upper = 0.02
	rec.N = 200
	rec.WarmP95Ms = 400
	ok, reason := rec.Check("modelsha", "registryhash")
	if !ok || reason != "" {
		t.Fatalf("expected boundary values to pass, got ok=%v reason=%q", ok, reason)
	}
}

func TestWriteReadEvalRecordRoundTrip(t *testing.T) {
	home := t.TempDir()
	rec := passingRecord()
	if err := WriteEvalRecord(home, rec); err != nil {
		t.Fatalf("WriteEvalRecord: %v", err)
	}

	got, err := ReadEvalRecord(home)
	if err != nil {
		t.Fatalf("ReadEvalRecord: %v", err)
	}
	if got.ModelSHA256 != rec.ModelSHA256 || got.N != rec.N || got.FARate != rec.FARate {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, rec)
	}
	if !got.At.Equal(rec.At) {
		t.Fatalf("timestamp mismatch: got %v want %v", got.At, rec.At)
	}

	wantPath := filepath.Join(home, "router", "tier1_eval.json")
	if EvalGatePath(home) != wantPath {
		t.Fatalf("EvalGatePath = %q, want %q", EvalGatePath(home), wantPath)
	}
}

func TestReadEvalRecordMissing(t *testing.T) {
	home := t.TempDir()
	if _, err := ReadEvalRecord(home); err == nil {
		t.Fatal("expected an error reading a missing eval record")
	}
}
