package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water"
	"water/internal/agentmail"
	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/google/gcal"
	"water/internal/connectors/google/gdrive"
	"water/internal/connectors/google/gmail"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twinlink"
	"water/internal/twins"
	"water/internal/vault"
)

// twinNode is one complete daemon instance for Slice E's loopback: its own
// home directory (store, audit log, approvals, clients.json), its own vault,
// its own real Unix socket, loaded from its own real, embedded twins/<id>
// manifest. Two of these in one test are two daemons with separate data
// directories talking over their sockets — the same transport two `water
// daemon` processes use.
type twinNode struct {
	id    string
	home  string
	d     *Daemon
	st    *store.Store
	log   *audit.Log
	q     *approvals.Queue
	g     *gate.Gate
	v     *vault.MemoryVault
	fake  *backend.Fake
	cli   string // this node's own CEO-side client token
	sock  string
	http  *http.Client
	close func()
}

func newTwinNode(t *testing.T, id string) *twinNode {
	t.Helper()
	// A short home under the system temp dir: a Unix socket path must fit
	// in 104 bytes on macOS, which t.TempDir()'s long paths can exceed.
	home, err := os.MkdirTemp("", "wtl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	st, err := store.Open(filepath.Join(home, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(filepath.Join(home, "audit", "audit.jsonl"), audit.WithAnchor(st))
	if err != nil {
		t.Fatal(err)
	}
	q := approvals.NewQueue(st, log)
	m, err := twins.Load(water.TwinsFS(), id)
	if err != nil {
		t.Fatal(err)
	}
	// The same connector set the CLI builds for every twin; each manifest
	// decides which of them its twin may actually use.
	reg, err := connectors.NewRegistry(gcal.New(), gmail.New("agent@example.com"), gdrive.New(), agentmail.New("agent@example.com"),
		twinlink.NewSender(id, st), twinlink.NewInbox(st))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	paths := Paths{Home: home}
	clients, err := LoadClients(paths.ClientsPath())
	if err != nil {
		t.Fatal(err)
	}
	cli, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	d := New(Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fb, Clients: clients, SocketPath: paths.SocketPath()})
	l, unlock, err := Listen(paths)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: d.Mux()}
	go srv.Serve(l)
	sock := paths.SocketPath()
	n := &twinNode{id: id, home: home, d: d, st: st, log: log, q: q, g: g, v: v, fake: fb, cli: cli, sock: sock,
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}}
	n.close = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		unlock()
		log.Close()
		st.Close()
	}
	t.Cleanup(n.close)
	return n
}

// peerWith makes n and other known to each other: each daemon mints a
// "twin:<other>" token (what `water daemon token new twin:<id>` does), and
// each twin's vault peer table gets the other's socket and the token the
// other minted for it (what `water twin peer add` does).
func peerWith(t *testing.T, a, b *twinNode) {
	t.Helper()
	tokBForA, err := b.d.cfg.Clients.New(twinlink.PeerClientPrefix + a.id)
	if err != nil {
		t.Fatal(err)
	}
	tokAForB, err := a.d.cfg.Clients.New(twinlink.PeerClientPrefix + b.id)
	if err != nil {
		t.Fatal(err)
	}
	set := func(n *twinNode, peer string, p twinlink.Peer) {
		s, err := twinlink.Peers{peer: p}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := n.v.Set(twinlink.VaultService, n.id, vault.NewSecret(s)); err != nil {
			t.Fatal(err)
		}
	}
	set(a, b.id, twinlink.Peer{Socket: b.sock, Token: tokBForA})
	set(b, a.id, twinlink.Peer{Socket: a.sock, Token: tokAForB})
}

func (n *twinNode) do(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			r = strings.NewReader(b)
		default:
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			r = strings.NewReader(string(raw))
		}
	}
	req, err := http.NewRequest(method, "http://twin"+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// stage proposes a twin message through the node's own outbox endpoint.
func (n *twinNode) stage(t *testing.T, args map[string]any) TwinOutboxResult {
	t.Helper()
	code, body := n.do(t, http.MethodPost, "/v1/twinlink/outbox", n.cli, args)
	if code != http.StatusOK {
		t.Fatalf("%s outbox: %d %s", n.id, code, body)
	}
	var res TwinOutboxResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// approve answers a pending approval over the node's own approvals endpoint,
// the path `water approve` takes.
func (n *twinNode) approve(t *testing.T, id, hash, reply string) DecisionResult {
	t.Helper()
	code, body := n.do(t, http.MethodPost, "/v1/approvals/"+id+"/decision", n.cli, map[string]string{"payload_hash": hash, "reply": reply})
	if code != http.StatusOK {
		t.Fatalf("%s decide: %d %s", n.id, code, body)
	}
	var res DecisionResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func (n *twinNode) inbox(t *testing.T, direction string) twinlink.Listing {
	t.Helper()
	code, body := n.do(t, http.MethodGet, "/v1/twinlink/messages?direction="+direction, n.cli, nil)
	if code != http.StatusOK {
		t.Fatalf("%s inbox: %d %s", n.id, code, body)
	}
	var l twinlink.Listing
	if err := json.Unmarshal(body, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func (n *twinNode) pending(t *testing.T) []approvals.Envelope {
	t.Helper()
	envs, err := n.q.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return envs
}

func (n *twinNode) taint(t *testing.T) gate.Taint {
	t.Helper()
	ta, ok := n.d.lookupTurnToken(n.d.stableSessionToken())
	if !ok {
		t.Fatal("no session token")
	}
	return ta.Taint
}
