package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"water/internal/connectors/google/gapi"
	"water/internal/vault"
)

// connectCmd is the top-level `water connect` command group: one subcommand
// per external account water can read from.
func (a *App) connectCmd() *cobra.Command {
	c := &cobra.Command{Use: "connect", Short: "Connect an external account"}
	c.AddCommand(a.connectGoogleCmd())
	return c
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
	cred, err := gapi.CredentialFromSecret(s)
	if err != nil {
		return exitWith(ExitError, err)
	}
	if err := gapi.Revoke(ctx, cred.RefreshToken, nil); err != nil {
		fmt.Fprintln(os.Stderr, "warning: revoke at Google failed:", err)
	}
	if err := v.Delete(gapi.Service, account); err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Printf("revoked and disconnected: %s/%s\n", gapi.Service, account)
	return nil
}
