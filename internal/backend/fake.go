package backend

import (
	"context"
	"sync"
	"sync/atomic"
)

// Fake is an in-memory Backend for tests and the guard suite. It records
// every request it receives.
type Fake struct {
	FakeName string
	Avail    Availability
	Reply    func(req Request) string
	FailWith error
	calls    atomic.Int64
	mu       sync.Mutex
	requests []Request
}

// NewFake returns a usable, non-metered fake that echoes the prompt.
func NewFake(name string) *Fake {
	return &Fake{
		FakeName: name,
		Avail:    Availability{Installed: true, Authed: true, Metered: false, Detail: "fake"},
	}
}

func (f *Fake) Name() string                           { return f.FakeName }
func (f *Fake) Available(context.Context) Availability { return f.Avail }
func (f *Fake) Calls() int64                           { return f.calls.Load() }
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

func (f *Fake) Run(ctx context.Context, req Request) (Response, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.FailWith != nil {
		return Response{}, f.FailWith
	}
	text := "[" + f.FakeName + ":" + req.Role + "] " + req.Prompt
	if f.Reply != nil {
		text = f.Reply(req)
	}
	return Response{Text: text, Raw: text, Metered: f.Avail.Metered, Backend: f.FakeName}, nil
}
