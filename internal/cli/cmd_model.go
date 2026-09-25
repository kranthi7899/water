package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"water/internal/config"
	"water/internal/nervous/sidecar"
)

// modelDownloadURL and modelExpectedSHA256 are overridden by this package's
// own tests to point `water model pull` at an httptest.Server fixture
// instead of the real, pinned Hugging Face download.
var (
	modelDownloadURL    = sidecar.ModelDownloadURL
	modelExpectedSHA256 = sidecar.ModelSHA256
)

const gemmaTermsNotice = `FunctionGemma is distributed under Google's Gemma Terms of Use, not an
OSI-approved open-source license. Review and accept before downloading:

  Terms of Use:            https://ai.google.dev/gemma/terms
  Prohibited Use Policy:   https://ai.google.dev/gemma/prohibited_use_policy

Re-run with --accept-gemma-terms once you agree.`

// modelCmd is the top-level `water model` command group.
func (a *App) modelCmd() *cobra.Command {
	c := &cobra.Command{Use: "model", Short: "Manage local sidecar models"}
	c.AddCommand(a.modelPullCmd())
	return c
}

// modelPullCmd implements `water model pull functiongemma [--accept-gemma-terms]`.
// Nothing downloads implicitly: this command is the only network call this
// slice adds, and only when the owner types it and accepts the Gemma terms.
func (a *App) modelPullCmd() *cobra.Command {
	var accept bool
	c := &cobra.Command{
		Use:   "pull <name>",
		Short: "Download a pinned sidecar model (functiongemma)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "functiongemma" {
				return exitWith(ExitUsage, fmt.Errorf("unknown model %q; only \"functiongemma\" is supported", args[0]))
			}
			if !accept {
				fmt.Fprintln(cmd.ErrOrStderr(), gemmaTermsNotice)
				return exitWith(ExitUsage, errors.New("refusing to download functiongemma without --accept-gemma-terms"))
			}
			home := config.Home()
			if err := pullModel(cmd.Context(), modelDownloadURL(), modelExpectedSHA256, home, os.Getenv("HF_TOKEN"), cmd.OutOrStdout()); err != nil {
				return exitWith(ExitError, err)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&accept, "accept-gemma-terms", false, "accept Google's Gemma Terms of Use before downloading")
	return c
}

// pullModel downloads url to $home/models/functiongemma.gguf.part, verifies
// its sha256 against expectedSHA256, and only then renames it into place as
// $home/models/functiongemma.gguf. A sha256 mismatch deletes the partial
// file and returns a clear error naming both hashes. token, when non-empty,
// is sent as a Hugging Face bearer token (HF_TOKEN from the environment); a
// 401/403 response is reported plainly, naming what to do, rather than as an
// opaque HTTP error.
func pullModel(ctx context.Context, url, expectedSHA256, home, token string, out io.Writer) error {
	dir := filepath.Join(home, "models")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	partPath := filepath.Join(dir, sidecar.ModelPartFileName)
	finalPath := filepath.Join(dir, sidecar.ModelFileName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", url, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// proceed to stream the body below.
	case http.StatusUnauthorized:
		if token == "" {
			return fmt.Errorf("Hugging Face requires authentication for this download (401 Unauthorized): set HF_TOKEN to an access token with access to %s, then retry", sidecar.ModelRepo)
		}
		return fmt.Errorf("Hugging Face rejected HF_TOKEN (401 Unauthorized): confirm the token is valid and has access to %s", sidecar.ModelRepo)
	case http.StatusForbidden:
		return fmt.Errorf("Hugging Face refused access (403 Forbidden): visit https://huggingface.co/%s, accept the license, and if the repo is gated set HF_TOKEN to an access token with access to it", sidecar.ModelRepo)
	default:
		return fmt.Errorf("download %s: unexpected HTTP status %s", url, resp.Status)
	}

	f, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", partPath, err)
	}

	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, hasher), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(partPath)
		return fmt.Errorf("download %s: %w", url, copyErr)
	}
	if closeErr != nil {
		os.Remove(partPath)
		return fmt.Errorf("close %s: %w", partPath, closeErr)
	}

	gotSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if gotSHA256 != expectedSHA256 {
		os.Remove(partPath)
		return fmt.Errorf("sha256 mismatch after downloading %d bytes to %s: got %s, want %s (partial file removed)", written, partPath, gotSHA256, expectedSHA256)
	}

	if err := os.Rename(partPath, finalPath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", partPath, finalPath, err)
	}

	fmt.Fprintf(out, "functiongemma: downloaded and verified %d bytes -> %s\n", written, finalPath)
	return nil
}
