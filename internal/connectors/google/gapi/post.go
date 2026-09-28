package gapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ErrSendOutcomeUnknown means a POST failed in a way that leaves the actual
// outcome unknown to the caller: a network error or timeout may have hit
// after Google already received and started acting on the request, or
// Google answered with a server error (5xx) after processing began, or
// Google answered 2xx (so it did act) but the response was lost. Unlike
// GetJSON's read retries, PostJSON never retries this automatically — a
// blind retry on a send_message/create_event/move_event risks doing it
// twice. A caller must check whether the effect actually happened (e.g.
// list sent mail, list events for the expected one) before deciding whether
// it is safe to retry. Use errors.Is(err, ErrSendOutcomeUnknown) to detect
// this case specifically.
var ErrSendOutcomeUnknown = errors.New("google: send outcome unknown; check before retrying")

// PostJSON POSTs body as JSON to rawURL (with query added, as GetJSON) and
// decodes the JSON response into out (out may be nil to discard it).
//
// Idempotency contract: PostJSON makes exactly one send attempt, plus (as
// GetJSON does) one refresh-and-retry when the first attempt's token was
// merely expired (a 401 before Google processes anything is safe to retry).
// Beyond that it never retries. If the outcome of the send attempt is
// ambiguous — the HTTP round trip itself failed (network error, timeout),
// or Google answered 5xx after receiving the request — PostJSON returns an
// error wrapping ErrSendOutcomeUnknown and does not retry. A clean 4xx
// (400, 403, 404, 409, 429, ...) is a definite, safe-to-report failure:
// Google rejected the call before doing anything, returned as a plain
// *APIError. A definite failure or a definite success are the only two
// outcomes that are ever safe to act on without checking further; anything
// wrapping ErrSendOutcomeUnknown is neither.
func (c *Client) PostJSON(ctx context.Context, rawURL string, query url.Values, body, out any) error {
	return c.write(ctx, http.MethodPost, rawURL, query, body, out)
}

// PatchJSON is PostJSON with an HTTP PATCH (e.g. Calendar events.patch),
// under the same single-attempt contract.
func (c *Client) PatchJSON(ctx context.Context, rawURL string, query url.Values, body, out any) error {
	return c.write(ctx, http.MethodPatch, rawURL, query, body, out)
}

func (c *Client) write(ctx context.Context, method, rawURL string, query url.Values, body, out any) error {
	target, err := c.requestURL(rawURL, query)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("google: encoding request: %s", c.scrub(err.Error()))
	}
	respBody, err := c.post(ctx, method, target, payload)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			// Google answered 2xx: the action happened even though its
			// answer is unreadable, so this must never read as a failure.
			return fmt.Errorf("%w: Google accepted the request but its response could not be decoded: %s", ErrSendOutcomeUnknown, c.scrub(err.Error()))
		}
	}
	return nil
}

// post makes one write attempt, refreshing and retrying exactly once on a
// 401 (see PostJSON's contract). It never loops on 5xx/429 or network
// errors the way get's read-retry loop does.
func (c *Client) post(ctx context.Context, method, target string, payload []byte) ([]byte, error) {
	refreshed := false
	var forceStale string
	force := false
	for {
		tok, err := c.token(ctx, force, forceStale)
		c.remember(tok)
		if err != nil {
			return nil, err
		}
		force = false
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("google: %s", c.scrub(err.Error()))
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("Accept", "application/json")
		resp, err := c.opts.HTTPClient.Do(req)
		if err != nil {
			// The request may have already reached Google (a dropped
			// connection or timeout can happen after the body was fully
			// written and even after Google acted on it) before this
			// error surfaced locally: the outcome is unknown.
			return nil, fmt.Errorf("%w: request failed: %s", ErrSendOutcomeUnknown, c.scrub(err.Error()))
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			b, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxBytes+1))
			resp.Body.Close()
			// Past a 2xx status the action happened; a lost or oversized
			// answer must not read as a failure someone would retry.
			if err != nil {
				return nil, fmt.Errorf("%w: Google accepted the request but reading its response failed: %s", ErrSendOutcomeUnknown, c.scrub(err.Error()))
			}
			if int64(len(b)) > c.opts.MaxBytes {
				return nil, fmt.Errorf("%w: Google accepted the request but its response is larger than %d bytes", ErrSendOutcomeUnknown, c.opts.MaxBytes)
			}
			return b, nil
		}
		apiErr := c.readError(resp)
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			// A pre-send auth failure: Google never processed the call, so
			// one refresh-and-retry is as safe here as it is for GET.
			refreshed, force, forceStale = true, true, tok
			continue
		}
		if resp.StatusCode >= 500 {
			// Google received and started on the request; a 5xx answer
			// doesn't say whether the send/create/move happened.
			return nil, fmt.Errorf("%w: %w", ErrSendOutcomeUnknown, apiErr)
		}
		// Any other non-2xx (400, 403, 404, 409, 429, ...) is Google
		// definitely refusing the call before doing anything: safe to
		// report as a plain failure.
		return nil, apiErr
	}
}
