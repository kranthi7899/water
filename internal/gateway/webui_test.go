package gateway

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"water/internal/webui"
)

// TestUIAssetsAreServedBehindAuthWithTheCSP serves every embedded UI file
// through the daemon's real mux: each needs the client token, and every
// response under the prefix (the 401s included) carries the strict CSP and
// nosniff.
func TestUIAssetsAreServedBehindAuthWithTheCSP(t *testing.T) {
	h := newHarness(t)
	files, err := webui.Files()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{UIPrefix}
	for _, f := range files {
		paths = append(paths, UIPrefix+f)
	}
	check := func(p string, resp *http.Response) {
		t.Helper()
		if got := resp.Header.Get("Content-Security-Policy"); got != webui.CSP {
			t.Errorf("%s: Content-Security-Policy = %q, want webui.CSP", p, got)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", p, got)
		}
	}
	for _, p := range paths {
		for _, tok := range []string{"", "bogus"} {
			resp := do(t, h.srv.URL, "GET", p, "", tok)
			check(p, resp)
			if got := statusOf(resp); got != http.StatusUnauthorized {
				t.Errorf("GET %s with token %q: status %d, want 401", p, tok, got)
			}
		}
		resp := do(t, h.srv.URL, "GET", p, "", h.token)
		check(p, resp)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(b) == 0 {
			t.Errorf("GET %s: status %d, %d bytes; want 200 with a body", p, resp.StatusCode, len(b))
		}
	}
	// A miss under the prefix is a 404 that still carries the headers.
	resp := do(t, h.srv.URL, "GET", UIPrefix+"missing.js", "", h.token)
	check("missing.js", resp)
	if got := statusOf(resp); got != http.StatusNotFound {
		t.Errorf("missing asset: status %d, want 404", got)
	}
	// Only GET/HEAD: the UI prefix accepts no writes.
	if got := statusOf(do(t, h.srv.URL, "POST", UIPrefix+"index.html", "{}", h.token)); got != http.StatusMethodNotAllowed {
		t.Errorf("POST to the UI prefix: status %d, want 405", got)
	}
}

// TestUIOnlyCallsAllowlistedRoutes pins every API path the shipped UI's
// JavaScript names to the routes docs/slices/V.md §5a lists for the
// water:// scheme handler's allowlist, and checks it never names a route
// the UI must never reach.
func TestUIOnlyCallsAllowlistedRoutes(t *testing.T) {
	b, err := webui.ReadFile("api.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// /v1/notifications is native-only (V-notify): the page must never list
	// or mark notifications.
	// /v1/turns stays native-only too (V-ui2): the page's held mic asks the
	// native side to run the voice turn, it never posts one itself.
	for _, never := range []string{"/v1/tools", "/v1/quick", "/v1/twinlink", "/v1/turns", "turns", "thread_id", "/v1/intents", "/v1/state", "/v1/notifications", "notifications", "/email"} {
		if strings.Contains(src, never) {
			t.Errorf("api.js names %q, which the UI must not call", never)
		}
	}
	// Approval edit (V-ui2) is reachable, but only on an approval: every
	// '/edit' suffix in api.js must be exactly this one expression, and it
	// must be there (added by hand here, in api.js and in
	// WorkspaceAllowlist.routes).
	const editRoute = `'/v1/approvals/' + enc(id) + '/edit'`
	if n, m := strings.Count(src, "'/edit'"), strings.Count(src, editRoute); n != 1 || m != 1 {
		t.Errorf("api.js has %d '/edit' suffixes and %d %s; want exactly the one approval edit route", n, m, editRoute)
	}
	// Every other quoted path suffix api.js appends to an id is one of these.
	suffixes := map[string]bool{
		"'/stage'": true, "'/dismiss'": true, "'/decision'": true, "'/edit'": true, "'/messages'": true,
		"'/cancel'": true, "'/request-changes'": true,
		// Phase 3b: the per-action stage route's middle segment and the
		// related-data route.
		"'/actions/'": true, "'/related'": true,
		// Phase 3c: Drafts' "Send for approval" route.
		"'/submit'": true,
		// Phase 5b: the People workspace's draft-creating buttons.
		"'/drafts'": true,
		// Phase 5c: Ideas' "Start research"/"Propose" and Research's Attach.
		"'/research'": true, "'/propose'": true, "'/attach'": true,
	}
	for _, m := range regexp.MustCompile(`\+ '(/[^']*)'`).FindAllStringSubmatch(src, -1) {
		if lit := "'" + m[1] + "'"; !suffixes[lit] {
			t.Errorf("api.js appends %s to a path, which is not on the UI's allowlist", lit)
		}
	}
	allowed := []string{
		"'/v1/today'", "'/v1/decisions'", "'/v1/decisions/'", "'/v1/approvals?status='", "'/v1/approvals/'",
		"'/v1/threads'", "'/v1/threads/anchor'", "'/v1/threads/'", "'/v1/tasks/'", "'/v1/meetings?limit='", "'/v1/meetings/'",
		// Phase 3d: upcoming meetings, a query-param-shaped route like the
		// existing '/v1/meetings?limit=' entry above.
		"'/v1/meetings?upcoming=1&limit='",
		"'/v1/workspaces'",
		// Phase 5a: one workspace's own control-room tiles.
		"'/v1/workspaces/'",
		"'/v1/dashboards'", "'/v1/dashboards/'",
		// Phase 3c: the Drafts editor.
		"'/v1/drafts'", "'/v1/drafts/'",
		// Phase 5c: Ideas and Research.
		"'/v1/ideas'", "'/v1/ideas/'", "'/v1/research/runs'", "'/v1/research/runs/'",
	}
	// Every quoted '/v1...' literal in api.js must be one of the above.
	for _, part := range strings.Split(src, "'/v1")[1:] {
		lit := "'/v1" + part[:strings.Index(part, "'")+1]
		ok := false
		for _, a := range allowed {
			if lit == a {
				ok = true
			}
		}
		if !ok {
			t.Errorf("api.js calls %s, which is not on the UI's allowlist", lit)
		}
	}
	// Every other shipped .js file (dom.js, app.js, and every per-view
	// file the UI splits its views into) must never call the API
	// directly — only api.js does. Driven by webui.Files() rather than a
	// hardcoded list, so a new view file added later is checked
	// automatically instead of silently escaping this test.
	files, err := webui.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".js") || f == "api.js" {
			continue
		}
		b, err := webui.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "/v1/") || strings.Contains(string(b), "fetch(") {
			t.Errorf("%s calls the API directly; every call goes through api.js", f)
		}
	}
}

// TestApprovalsViewRendersWarnings is docs/slices/UI.md Phase 3a's finding
// 23: W's Envelope.Warnings must render visibly on the web approval card as
// an amber box, not only through the CLI/voice read-back
// (internal/approvals/readback.go). This pins that view_approvals.js's own
// source actually reads the envelope's warnings field, rather than only
// app.js's shared renderApprovalDetail doing so -- the Approvals queue's
// dense rows flag a warned approval before it's even opened.
func TestApprovalsViewRendersWarnings(t *testing.T) {
	b, err := webui.ReadFile("view_approvals.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "warnings") {
		t.Error("view_approvals.js must reference an envelope's warnings (finding 23's amber box)")
	}
}

// TestDecisionsViewMapsEveryEvidenceKindToAnIcon is docs/slices/UI.md
// Phase 3b: the left column's evidence lines carry an icon from
// decisions.EvidenceKind's closed set, and never inline source text.
func TestDecisionsViewMapsEveryEvidenceKindToAnIcon(t *testing.T) {
	b, err := webui.ReadFile("view_decisions.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, kind := range []string{"money", "customer", "issue", "mail", "calendar", "research"} {
		if !strings.Contains(src, "'"+kind+"'") && !strings.Contains(src, kind+":") {
			t.Errorf("view_decisions.js has no icon mapping for evidence kind %q", kind)
		}
	}
	if strings.Contains(src, "'ev-source'") || strings.Contains(src, "\"ev-source\"") {
		t.Error("view_decisions.js still renders an evidence line's inline source (Phase 3b: source is reachable only through 'View related data')")
	}
}

// TestDecisionsViewRendersMissingInfoOnlyWhenGapsExist pins U12's exact
// wording: a muted "Missing info: ..." line under the recommendation, never
// rendered as an unconditional empty line.
func TestDecisionsViewRendersMissingInfoOnlyWhenGapsExist(t *testing.T) {
	b, err := webui.ReadFile("view_decisions.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "Missing info: ") {
		t.Error(`view_decisions.js must render the literal "Missing info: " line (U12)`)
	}
	if !strings.Contains(src, "missing-info") {
		t.Error("view_decisions.js must mark the missing-info line as muted (class missing-info)")
	}
	if !regexp.MustCompile(`if\s*\(\s*gaps\.length\s*\)\s*\{`).MatchString(src) {
		t.Error(`view_decisions.js must gate the missing-info line on "gaps.length" (shown only when gaps exist)`)
	}
}

// TestDecisionsViewRendersTeamSignalSimulatedSuffix pins U8's rule as
// rendered on the card: a team signal line reads "(simulated)" only when
// Signal.Simulated is set.
func TestDecisionsViewRendersTeamSignalSimulatedSuffix(t *testing.T) {
	b, err := webui.ReadFile("view_decisions.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "(simulated)") {
		t.Error(`view_decisions.js must append "(simulated)" to a simulated team signal`)
	}
	if !strings.Contains(src, "TeamSignal") && !strings.Contains(src, "team_signal") {
		t.Error("view_decisions.js must render Card.TeamSignal")
	}
}

// TestDecisionsViewRelatedCountAndReview pins the "View related data (N)"
// count formula and the per-action Review flow's endpoint calls.
func TestDecisionsViewRelatedCountAndReview(t *testing.T) {
	b, err := webui.ReadFile("view_decisions.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "View related data (") {
		t.Error(`view_decisions.js must render the literal "View related data (N)" button`)
	}
	if !strings.Contains(src, "SourceItemIDs") && !strings.Contains(src, "source_item_ids") {
		t.Error("view_decisions.js's related-data count must be drawn from SourceItemIDs")
	}
	if !strings.Contains(src, "evidence.length") {
		t.Error("view_decisions.js's related-data count must add the evidence sources' length")
	}
	if !strings.Contains(src, "stageDecisionAction") {
		t.Error("view_decisions.js's suggestion Review flow must call api.stageDecisionAction")
	}
	if !strings.Contains(src, "relatedDecision") {
		t.Error("view_decisions.js's related-data panel must call api.relatedDecision")
	}
}
