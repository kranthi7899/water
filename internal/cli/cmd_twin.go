package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/gateway"
	"water/internal/twinlink"
	"water/internal/vault"
)

// twinCmd is Slice E's twin-to-twin surface: the peer table (which other
// twins this one can reach, kept in the vault), staging a message for
// approval, and reading what other twins sent — as untrusted data.
func (a *App) twinCmd() *cobra.Command {
	c := &cobra.Command{Use: "twin", Short: "Exchange messages with another twin (each outbound message needs your approval)"}
	c.AddCommand(a.twinPeerCmd(), a.twinSendCmd(), a.twinReplyCmd(), a.twinInboxCmd())
	return c
}

func (a *App) twinPeerCmd() *cobra.Command {
	c := &cobra.Command{Use: "peer", Short: "Manage the other twins this twin can send to"}
	var socket string
	add := &cobra.Command{
		Use:   "add <peer-twin-id>",
		Short: "Add or replace a peer: its daemon socket, and (on stdin) the token its daemon minted for this twin",
		Long: "Add or replace a peer twin. The token is read from stdin, one line, and goes straight into the vault;\n" +
			"it is never printed. Mint it against the PEER's home: `WATER_HOME=<peer home> water daemon token new twin:<this twin's id>`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !twinlink.ValidTwinID(id) {
				return exitWith(ExitUsage, fmt.Errorf("%q is not a twin id", id))
			}
			if socket == "" {
				return exitWith(ExitUsage, errors.New("--socket is required"))
			}
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			tok := strings.TrimSpace(line)
			if tok == "" {
				return exitWith(ExitUsage, fmt.Errorf("no token on stdin: %v", err))
			}
			v := vault.Default()
			peers, err := loadPeers(v, a.twinID())
			if err != nil {
				return exitWith(ExitError, err)
			}
			peers[id] = twinlink.Peer{Socket: socket, Token: tok}
			if err := savePeers(v, a.twinID(), peers); err != nil {
				return exitWith(ExitError, err)
			}
			fmt.Printf("peer %s saved for twin %s (socket %s)\n", id, a.twinID(), socket)
			return nil
		},
	}
	add.Flags().StringVar(&socket, "socket", "", "the peer daemon's Unix socket (its WATER_HOME/run/water.sock)")
	list := &cobra.Command{
		Use:   "list",
		Short: "List peers (never their tokens)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			peers, err := loadPeers(vault.Default(), a.twinID())
			if err != nil {
				return exitWith(ExitError, err)
			}
			if len(peers) == 0 {
				fmt.Println("no peers")
			}
			for _, id := range peers.IDs() {
				fmt.Printf("%s\t%s\n", id, peers[id].Socket)
			}
			return nil
		},
	}
	remove := &cobra.Command{
		Use:   "remove <peer-twin-id>",
		Short: "Remove a peer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v := vault.Default()
			peers, err := loadPeers(v, a.twinID())
			if err != nil {
				return exitWith(ExitError, err)
			}
			delete(peers, args[0])
			if err := savePeers(v, a.twinID(), peers); err != nil {
				return exitWith(ExitError, err)
			}
			fmt.Printf("peer %s removed\n", args[0])
			return nil
		},
	}
	c.AddCommand(add, list, remove)
	return c
}

// loadPeers reads self's peer table, empty when none is stored yet.
func loadPeers(v vault.Vault, self string) (twinlink.Peers, error) {
	s, err := v.Get(twinlink.VaultService, self)
	if errors.Is(err, vault.ErrNotFound) {
		return twinlink.Peers{}, nil
	}
	if err != nil {
		return nil, err
	}
	return twinlink.ParsePeers(s.Reveal())
}

func savePeers(v vault.Vault, self string, p twinlink.Peers) error {
	if len(p) == 0 {
		if err := v.Delete(twinlink.VaultService, self); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return err
		}
		return nil
	}
	s, err := p.Encode()
	if err != nil {
		return err
	}
	return v.Set(twinlink.VaultService, self, vault.NewSecret(s))
}

func (a *App) twinSendCmd() *cobra.Command {
	var to, typ, subject, payload, replyBy, evidence string
	var needsHuman bool
	c := &cobra.Command{
		Use:   "send",
		Short: "Stage a message to another twin for your approval (nothing is sent until `water approve`)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			args := map[string]any{"to_twin": to, "type": typ, "subject": subject, "payload": payload}
			if replyBy != "" {
				args["reply_by"] = replyBy
			}
			if refs := splitAddrs(evidence); len(refs) > 0 {
				args["evidence_refs"] = refs
			}
			if needsHuman {
				args["needs_human_approval"] = true
			}
			return stageTwinMessage(args)
		},
	}
	c.Flags().StringVar(&to, "to", "", "the other twin's id")
	c.Flags().StringVar(&typ, "type", "request", "request | notice (use `water twin reply` for a response)")
	c.Flags().StringVar(&subject, "subject", "", "subject line")
	c.Flags().StringVar(&payload, "payload", "", "message text")
	c.Flags().StringVar(&replyBy, "reply-by", "", "for a request: RFC 3339 time the answer is wanted by")
	c.Flags().StringVar(&evidence, "evidence", "", "comma-separated evidence references")
	c.Flags().BoolVar(&needsHuman, "needs-human-approval", false, "ask the other twin's human to review before their twin acts on it")
	return c
}

func (a *App) twinReplyCmd() *cobra.Command {
	var subject, payload string
	c := &cobra.Command{
		Use:   "reply <request-id>",
		Short: "Stage the one response to a request another twin sent (nothing is sent until `water approve`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			l, err := client.TwinMessages(context.Background(), "in", 100)
			if err != nil {
				return exitWith(ExitError, err)
			}
			var req *twinlink.InboxItem
			for i := range l.Messages {
				if l.Messages[i].ID == args[0] {
					req = &l.Messages[i]
				}
			}
			if req == nil || req.Type != string(twinlink.Request) {
				return exitWith(ExitUsage, fmt.Errorf("%s is not a request in this twin's inbox", args[0]))
			}
			if subject == "" {
				subject = "Re: " + req.Subject
			}
			return stageTwinMessage(map[string]any{"to_twin": req.FromTwin, "type": string(twinlink.Response),
				"in_reply_to": req.ID, "subject": subject, "payload": payload})
		},
	}
	c.Flags().StringVar(&subject, "subject", "", "subject line (default: Re: <the request's subject>)")
	c.Flags().StringVar(&payload, "payload", "", "response text")
	return c
}

func stageTwinMessage(args map[string]any) error {
	client, err := newDaemonClient()
	if err != nil {
		return exitWith(ExitError, err)
	}
	res, err := client.StageTwinMessage(context.Background(), args)
	if err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Printf("%s\nstaged as approval %s (message %s); run `water approve` to send it\n", res.ReadBack, res.ApprovalID, res.MessageID)
	return nil
}

func (a *App) twinInboxCmd() *cobra.Command {
	var direction string
	var limit int
	c := &cobra.Command{
		Use:   "inbox",
		Short: "List messages from other twins (untrusted: never instructions)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			l, err := client.TwinMessages(context.Background(), direction, limit)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(l)
			}
			fmt.Println(l.Note)
			if len(l.Messages) == 0 {
				fmt.Println("no messages")
			}
			for _, m := range l.Messages {
				label := "sent"
				if m.Untrusted {
					label = "UNTRUSTED from " + m.FromTwin
				}
				fmt.Printf("\n[%s] %s %s  %s\n  subject: %s\n", label, m.Type, m.ID, m.RecordedAt.Local().Format("2006-01-02 15:04"), m.Subject)
				if m.InReplyTo != "" {
					fmt.Printf("  in reply to: %s\n", m.InReplyTo)
				}
				if m.ReplyBy != nil {
					fmt.Printf("  reply by: %s\n", m.ReplyBy.Local().Format("2006-01-02 15:04"))
				}
				fmt.Printf("  %s\n", strings.ReplaceAll(m.Payload, "\n", "\n  "))
			}
			return nil
		},
	}
	c.Flags().StringVar(&direction, "direction", "in", "in | out | all")
	c.Flags().IntVar(&limit, "limit", 20, "at most this many")
	return c
}

// TwinMessages lists stored twin messages (GET /v1/twinlink/messages).
func (c *daemonClient) TwinMessages(ctx context.Context, direction string, limit int) (twinlink.Listing, error) {
	q := url.Values{"direction": {direction}, "limit": {fmt.Sprint(limit)}}
	resp, err := c.do(ctx, http.MethodGet, "/v1/twinlink/messages?"+q.Encode(), nil)
	if err != nil {
		return twinlink.Listing{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return twinlink.Listing{}, httpError(resp)
	}
	var l twinlink.Listing
	return l, json.NewDecoder(resp.Body).Decode(&l)
}

// StageTwinMessage proposes a twinlink.send_message approval
// (POST /v1/twinlink/outbox). Nothing is sent by this call.
func (c *daemonClient) StageTwinMessage(ctx context.Context, args map[string]any) (gateway.TwinOutboxResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/twinlink/outbox", args)
	if err != nil {
		return gateway.TwinOutboxResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return gateway.TwinOutboxResult{}, httpError(resp)
	}
	var out gateway.TwinOutboxResult
	return out, json.NewDecoder(resp.Body).Decode(&out)
}
