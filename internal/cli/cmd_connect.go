package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"water/internal/connectors/github"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/hubspot"
	"water/internal/connectors/linear"
	"water/internal/connectors/tokenapi"
	"water/internal/vault"
)

// connectCmd is the top-level `water connect` command group: one subcommand
// per external account water can read from.
func (a *App) connectCmd() *cobra.Command {
	c := &cobra.Command{Use: "connect", Short: "Connect an external account"}
	c.AddCommand(a.connectGoogleCmd())
	c.AddCommand(connectTokenCmd("github", "Connect the GitHub account water lists pull requests and issues from (see docs/real-connectors-setup.md)", github.Service, github.Account, github.CheckStatus))
	c.AddCommand(connectTokenCmd("linear", "Connect the Linear account water lists issues from (see docs/real-connectors-setup.md)", linear.Service, linear.Account, linear.CheckStatus))
	c.AddCommand(connectTokenCmd("hubspot", "Connect the HubSpot account water lists deals and contacts from (see docs/real-connectors-setup.md)", hubspot.Service, hubspot.Account, hubspot.CheckStatus))
	return c
}

// connectTokenCmd builds `water connect <name> --token/--status/--revoke`
// for a simple bearer/raw-token service (github, linear, hubspot): the same
// shape as connectGoogleCmd, minus the OAuth flow — there is nothing to
// authorize or refresh, only a token to store, check or delete. checkStatus
// is the connector's own minimal live call (each service's --status check
// differs: GitHub GETs /rate_limit, Linear POSTs a tiny GraphQL query,
// HubSpot GETs one contact page).
func connectTokenCmd(name, short, service, account string, checkStatus func(ctx context.Context, s vault.Secret) error) *cobra.Command {
	var token string
	var status, revoke bool
	c := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := vault.Default()
			switch {
			case status && revoke:
				return exitWith(ExitUsage, errors.New("--status and --revoke are mutually exclusive"))
			case revoke:
				return revokeToken(v, name, service, account)
			case status:
				return tokenStatus(cmd.Context(), v, name, service, account, checkStatus)
			default:
				if token == "" {
					return exitWith(ExitUsage, fmt.Errorf("--token is required the first time you connect %s; see docs/real-connectors-setup.md", name))
				}
				return connectToken(v, name, service, account, token)
			}
		},
	}
	c.Flags().StringVar(&token, "token", "", "the "+name+" token to store")
	c.Flags().BoolVar(&status, "status", false, "check the stored token with a live call")
	c.Flags().BoolVar(&revoke, "revoke", false, "delete the stored token locally (does not revoke it at "+name+"; do that in "+name+"'s own settings)")
	return c
}

// connectToken stores a bearer/raw token in the vault. It never prints the
// token back.
func connectToken(v vault.Vault, name, service, account, token string) error {
	cred := tokenapi.Credential{Token: token}
	secret, err := cred.Secret()
	if err != nil {
		return exitWith(ExitUsage, err)
	}
	if err := v.Set(service, account, secret); err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Printf("connected: %s/%s\n", service, account)
	return nil
}

// tokenStatus runs the connector's own minimal live call to confirm the
// stored token actually authenticates.
func tokenStatus(ctx context.Context, v vault.Vault, name, service, account string, checkStatus func(context.Context, vault.Secret) error) error {
	s, err := v.Get(service, account)
	if err != nil {
		fmt.Printf("%s: not connected (%s/%s)\n", name, service, account)
		return exitWith(ExitUsage, tokenapi.ErrNoToken)
	}
	if err := checkStatus(ctx, s); err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Printf("%s: connected and verified OK (%s/%s)\n", name, service, account)
	return nil
}

// revokeToken deletes the local copy of a token. Unlike Google's OAuth
// tokens, there is no API call that can revoke a GitHub PAT, a Linear API
// key or a HubSpot private-app token from here — the owner does that in the
// service's own settings if they want the token itself invalidated, not
// just removed from water.
func revokeToken(v vault.Vault, name, service, account string) error {
	if err := v.Delete(service, account); err != nil {
		fmt.Printf("%s: nothing to revoke (%s/%s)\n", name, service, account)
		return nil
	}
	fmt.Printf("disconnected: %s/%s (this only removes the local copy; revoke the token itself in %s's own settings if you no longer want it valid)\n", service, account, name)
	return nil
}

func (a *App) connectGoogleCmd() *cobra.Command {
	var clientFile, account string
	var status, revoke bool
	c := &cobra.Command{
		Use:   "google",
		Short: "Connect the Google account water reads Calendar, Gmail and Drive from (see docs/google-setup.md)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if account == "" {
				account = gapi.DefaultAccount
			}
			v := vault.Default()
			switch {
			case status && revoke:
				return exitWith(ExitUsage, errors.New("--status and --revoke are mutually exclusive"))
			case revoke:
				return revokeGoogle(cmd.Context(), v, account)
			case status:
				return googleStatus(cmd.Context(), v, account)
			default:
				if clientFile == "" {
					return exitWith(ExitUsage, errors.New("--client-file is required the first time you connect; see docs/google-setup.md"))
				}
				return connectGoogle(cmd.Context(), v, account, clientFile)
			}
		},
	}
	c.Flags().StringVar(&clientFile, "client-file", "", "path to the downloaded OAuth client JSON (Desktop app type)")
	c.Flags().StringVar(&account, "account", gapi.DefaultAccount, "vault account name")
	c.Flags().BoolVar(&status, "status", false, "check the stored credential with a live token refresh")
	c.Flags().BoolVar(&revoke, "revoke", false, "revoke the stored credential at Google and delete it")
	return c
}

// connectGoogle runs the loopback OAuth flow and stores the resulting
// credential. It never prints the client secret, the code or any token.
func connectGoogle(ctx context.Context, v vault.Vault, account, clientFile string) error {
	data, err := os.ReadFile(clientFile)
	if err != nil {
		return exitWith(ExitUsage, err)
	}
	cfg, err := gapi.ParseClientFile(data)
	if err != nil {
		return exitWith(ExitUsage, err)
	}
	cred, err := gapi.Authorize(ctx, cfg, openConsentPage, nil)
	if err != nil {
		return exitWith(ExitError, err)
	}
	secret, err := cred.Secret()
	if err != nil {
		return exitWith(ExitError, err)
	}
	if err := v.Set(gapi.Service, account, secret); err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Printf("connected: %s/%s\n", gapi.Service, account)
	return nil
}

// openConsentPage prints the consent URL and, on macOS, also opens it. It
// always returns nil: whether or not a browser actually launched, the
// printed URL still works and the loopback flow keeps waiting regardless
// (see gapi.Authorize's doc comment on open).
func openConsentPage(authURL string) error {
	fmt.Fprintln(os.Stderr, "Open this URL to connect water to Google:")
	fmt.Fprintln(os.Stderr, authURL)
	if goruntime.GOOS == "darwin" {
		_ = exec.Command("/usr/bin/open", authURL).Start()
	}
	return nil
}

// googleStatus reports the stored credential's state with a live token
// refresh, per `water connect google --status`.
func googleStatus(ctx context.Context, v vault.Vault, account string) error {
	s, err := v.Get(gapi.Service, account)
	if err != nil {
		fmt.Printf("google: not connected (%s/%s)\n", gapi.Service, account)
		return exitWith(ExitUsage, gapi.ErrNotConnected)
	}
	cred, err := gapi.CredentialFromSecret(s)
	if err != nil {
		return exitWith(ExitError, err)
	}
	cl, err := gapi.New(cred, nil)
	if err != nil {
		return exitWith(ExitError, err)
	}
	if err := cl.Refresh(ctx); err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Printf("google: connected and refreshed OK (%s/%s)\n", gapi.Service, account)
	return nil
}

// revokeGoogle revokes the stored refresh token at Google, then deletes it
// from the vault regardless of whether the revoke call itself succeeded (a
// token already revoked or expired still shouldn't linger locally).
func revokeGoogle(ctx context.Context, v vault.Vault, account string) error {
	s, err := v.Get(gapi.Service, account)
	if err != nil {
		fmt.Printf("google: nothing to revoke (%s/%s)\n", gapi.Service, account)
		return nil
	}
	if cred, err := gapi.CredentialFromSecret(s); err != nil {
		fmt.Fprintln(os.Stderr, "warning: stored credential is unreadable, deleting it without revoking at Google:", err)
	} else if err := gapi.Revoke(ctx, cred.RefreshToken, nil); err != nil {
		fmt.Fprintln(os.Stderr, "warning: revoke at Google failed:", err)
	}
	if err := v.Delete(gapi.Service, account); err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Printf("revoked and disconnected: %s/%s\n", gapi.Service, account)
	return nil
}
