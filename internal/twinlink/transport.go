package twinlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// VaultService is the vault service holding a twin's peer table; the
// account is the twin's own id, so two daemons on one machine (one
// Keychain) never read each other's.
const VaultService = "water.twinlink"

// PeerClientPrefix marks a daemon client token as belonging to another
// twin: the receiving daemon's clients.json names such a token
// "twin:<peer id>". A peer token is accepted only on the inbound twin-message
// endpoint, and the id after the prefix is the only from_twin it may claim.
const PeerClientPrefix = "twin:"

// ReceivePath is the receiving daemon's inbound endpoint.
const ReceivePath = "/v1/twinlink/messages"

// Peer is how to reach one other twin's daemon: its Unix socket and the
// bearer token that daemon minted for this twin (`water daemon token new
// twin:<this twin's id>`, run against the peer's home).
type Peer struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
}

// Peers is a twin's whole peer table, keyed by peer twin id. It is stored as
// one single-line JSON vault secret, like the Google credential: the tokens
// in it are secrets and never belong in a file or a model's context.
type Peers map[string]Peer

// ParsePeers decodes a peer table secret.
func ParsePeers(secret string) (Peers, error) {
	var p Peers
	if err := json.Unmarshal([]byte(secret), &p); err != nil {
		return nil, errors.New("twinlink: peer table is not valid JSON")
	}
	for id, peer := range p {
		if !ValidTwinID(id) {
			return nil, fmt.Errorf("twinlink: peer table has a malformed twin id %q", id)
		}
		if peer.Socket == "" || peer.Token == "" {
			return nil, fmt.Errorf("twinlink: peer %s needs both a socket and a token", id)
		}
	}
	return p, nil
}

// Encode renders the table as the single-line JSON the vault stores.
func (p Peers) Encode() (string, error) {
	b, err := json.Marshal(p)
	return string(b), err
}

// IDs lists the peer ids, sorted.
func (p Peers) IDs() []string {
	out := make([]string, 0, len(p))
	for id := range p {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ErrOutcomeUnknown means the message may or may not have reached the other
// twin (the connection broke after the request went out, or the peer failed
// with a 5xx). Never retry blindly: because a message's id is derived from
// its content, re-sending the identical message from a fresh approval is
// safe — the receiver recognizes it as a duplicate — but a caller must not
// assume the first attempt failed.
var ErrOutcomeUnknown = errors.New("twinlink: delivery outcome unknown; the other twin may have received it")

// Ack is the receiving daemon's reply to a delivered message.
type Ack struct {
	Accepted  bool   `json:"accepted"`
	ID        string `json:"id"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// deliverTimeout bounds one delivery attempt.
const deliverTimeout = 15 * time.Second

// Deliver POSTs m to the peer's daemon over its Unix socket, exactly once:
// no retry of any kind, the same single-attempt contract as a Gmail send.
// Three outcomes: success (the peer accepted it); a definite failure (the
// socket could not be reached, so nothing was sent, or the peer refused the
// message with a 4xx); or ErrOutcomeUnknown.
func Deliver(ctx context.Context, peer Peer, m Message) (Ack, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return Ack{}, err
	}
	var connected atomic.Bool
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, "unix", peer.Socket)
			if err == nil {
				connected.Store(true)
			}
			return c, err
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: deliverTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://twin"+ReceivePath, bytes.NewReader(body))
	if err != nil {
		return Ack{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+peer.Token)
	resp, err := client.Do(req)
	if err != nil {
		if !connected.Load() {
			return Ack{}, fmt.Errorf("twinlink: could not reach %s's daemon (nothing was sent)", m.ToTwin)
		}
		return Ack{}, fmt.Errorf("%w (%s)", ErrOutcomeUnknown, scrubToken(err.Error(), peer.Token))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode >= 500:
		return Ack{}, fmt.Errorf("%w (peer answered %d)", ErrOutcomeUnknown, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Ack{}, fmt.Errorf("twinlink: %s refused the message (%d): %s", m.ToTwin, resp.StatusCode, firstLine(scrubToken(string(raw), peer.Token)))
	}
	var ack Ack
	if err := json.Unmarshal(raw, &ack); err != nil || !ack.Accepted || ack.ID != m.ID {
		// A 200 that does not read as an acceptance of this exact message:
		// it may well have been recorded, so this is not a definite failure.
		return Ack{}, fmt.Errorf("%w (unreadable acknowledgement)", ErrOutcomeUnknown)
	}
	return ack, nil
}

func scrubToken(s, tok string) string {
	if tok == "" {
		return s
	}
	return strings.ReplaceAll(s, tok, "[redacted]")
}

// firstLine keeps a refusal's reason short and single-line: it is text
// another twin wrote, so it is never allowed to run on.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.ToValidUTF8(s, "")
}
