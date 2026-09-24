package twinlink

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validMsg(t *testing.T) Message {
	t.Helper()
	m, err := MessageFromArgs("ceo", map[string]any{"to_twin": "counterparty", "type": "request", "subject": "budget", "payload": "how much is left?"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestValidate(t *testing.T) {
	rb := time.Now()
	for name, mut := range map[string]func(*Message){
		"bad id":                   func(m *Message) { m.ID = "x" },
		"bad twin id":              func(m *Message) { m.ToTwin = "Counter/Party" },
		"to itself":                func(m *Message) { m.ToTwin = m.FromTwin },
		"unknown type":             func(m *Message) { m.Type = "command" },
		"response without reply":   func(m *Message) { m.Type = Response },
		"request with in_reply_to": func(m *Message) { m.InReplyTo = m.ID },
		"notice with reply_by":     func(m *Message) { m.Type = Notice; m.ReplyBy = &rb },
		"empty subject":            func(m *Message) { m.Subject = " " },
		"long subject":             func(m *Message) { m.Subject = strings.Repeat("s", MaxSubject+1) },
		"empty payload":            func(m *Message) { m.Payload = "" },
		"big payload":              func(m *Message) { m.Payload = strings.Repeat("p", MaxPayload+1) },
		"invalid utf8":             func(m *Message) { m.Payload = "\xff" },
		"too many refs":            func(m *Message) { m.EvidenceRefs = make([]string, MaxEvidenceRefs+1) },
		"empty ref":                func(m *Message) { m.EvidenceRefs = []string{""} },
		"no sent_at":               func(m *Message) { m.SentAt = time.Time{} },
	} {
		m := validMsg(t)
		mut(&m)
		if err := m.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
	if err := validMsg(t).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveIDBindsContentNotSendTime(t *testing.T) {
	a := validMsg(t)
	b := a
	b.SentAt = a.SentAt.Add(time.Hour)
	ida, _ := DeriveID(a)
	idb, _ := DeriveID(b)
	if ida != idb || ida != a.ID || !ValidMessageID(ida) {
		t.Fatalf("ids %s %s %s: a retry of the same message must keep its id", ida, idb, a.ID)
	}
	for _, mut := range []func(*Message){
		func(m *Message) { m.Payload += "!" },
		func(m *Message) { m.FromTwin = "other" },
		func(m *Message) { m.ToTwin = "other" },
		func(m *Message) { m.NeedsHumanApproval = true },
		func(m *Message) { m.EvidenceRefs = []string{"x"} },
	} {
		c := a
		mut(&c)
		if id, _ := DeriveID(c); id == ida {
			t.Fatalf("changed content kept the id: %+v", c)
		}
	}
}

func TestMessageFromArgsStampsSenderFromConfigNotArgs(t *testing.T) {
	m, err := MessageFromArgs("ceo", map[string]any{"to_twin": "counterparty", "type": "request", "subject": "s", "payload": "p",
		"reply_by": "2026-09-30T17:00:00-04:00", "evidence_refs": []any{"a", "b"}, "needs_human_approval": true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if m.FromTwin != "ceo" || m.ReplyBy == nil || !m.ReplyBy.Equal(time.Date(2026, 9, 30, 21, 0, 0, 0, time.UTC)) || len(m.EvidenceRefs) != 2 || !m.NeedsHumanApproval {
		t.Fatalf("message = %+v", m)
	}
	if _, err := MessageFromArgs("ceo", map[string]any{"to_twin": "counterparty", "type": "request", "subject": "s", "payload": "p", "reply_by": "friday"}, time.Now()); err == nil {
		t.Fatal("a non-RFC 3339 reply_by was accepted")
	}
}

func TestParsePeers(t *testing.T) {
	p, err := ParsePeers(`{"counterparty":{"socket":"/tmp/x.sock","token":"t"}}`)
	if err != nil || p["counterparty"].Socket != "/tmp/x.sock" {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{`nope`, `{"Bad Id":{"socket":"s","token":"t"}}`, `{"ok":{"socket":"","token":"t"}}`, `{"ok":{"socket":"s","token":""}}`} {
		if _, err := ParsePeers(bad); err == nil {
			t.Errorf("ParsePeers(%s) accepted", bad)
		}
	}
	s, _ := p.Encode()
	if strings.Contains(s, "\n") {
		t.Fatal("the vault secret must be single-line")
	}
}

// serve runs h on a fresh Unix socket and returns its path.
func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestDeliverOutcomes(t *testing.T) {
	ctx := context.Background()
	m := validMsg(t)
	const tok = "sekrit-peer-token"
	var calls atomic.Int32
	ok := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+tok || r.URL.Path != ReceivePath {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var got Message
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got.ID != m.ID {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(Ack{Accepted: true, ID: got.ID})
	})
	if ack, err := Deliver(ctx, Peer{Socket: ok, Token: tok}, m); err != nil || !ack.Accepted {
		t.Fatalf("delivery: %+v %v", ack, err)
	}

	// 4xx: a definite refusal, not an unknown outcome, and never the token.
	refuse := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad "+tok+"\nsecond line", http.StatusBadRequest)
	})
	_, err := Deliver(ctx, Peer{Socket: refuse, Token: tok}, m)
	if err == nil || errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), "second line") {
		t.Fatalf("4xx: %v", err)
	}

	// 5xx and an unreadable 200: the peer may have recorded it.
	for name, h := range map[string]http.HandlerFunc{
		"5xx":         func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"garbage 200": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) },
		"wrong id": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(Ack{Accepted: true, ID: "tl_" + strings.Repeat("0", 32)})
		},
		"dropped": func(w http.ResponseWriter, r *http.Request) {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
		},
	} {
		if _, err := Deliver(ctx, Peer{Socket: serve(t, h), Token: tok}, m); !errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("%s: %v, want ErrOutcomeUnknown", name, err)
		}
	}

	// Unreachable socket: nothing was sent.
	_, err = Deliver(ctx, Peer{Socket: filepath.Join(os.TempDir(), "no-such-water.sock"), Token: tok}, m)
	if err == nil || errors.Is(err, ErrOutcomeUnknown) || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("unreachable: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("the accepting peer saw %d calls, want exactly 1 (no retry)", calls.Load())
	}
}
