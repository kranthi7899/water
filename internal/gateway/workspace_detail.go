// This file implements docs/slices/UI.md Phase 5a: GET /v1/workspaces/{id},
// a workspace's filtered existing sections (found via in_workspace edges)
// plus its own control-room tiles, shaped per Spec.Template.
//
// THE INVARIANT: no workspace control may ever create a decision. This file
// never imports water/internal/decisions and never calls
// water/internal/store's SetCardState/SetCardActionState/
// SetDecisionClassification/UpsertDecisionRecord — every function below is
// a pure read, built only from Gate.Invoke (a read-level R function),
// internal/dashboards' own gated-read reuse (Rows/Issues/Dashboard) and
// plain store/roster lookups. TestWorkspaceDetailRoutesNeverWriteADecision
// (workspace_detail_test.go) proves this at the route level with a
// counting store; TestWorkspaceDetailFileNeverReferencesDecisionWrites
// (same file) proves it by scanning this file's own AST, mirroring
// internal/dashboards' own TestComputeHasNoHardcodedMetricValues scan.
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"water/internal/connectors/github"
	"water/internal/connectors/linear"
	"water/internal/dashboards"
	"water/internal/gate"
	"water/internal/needsyou"
	"water/internal/roster"
	"water/internal/store"
	"water/internal/workspaces"
)

// workspaceOrigin is the gate.Origin every GitHub call below uses, matching
// internal/dashboards' own dashboardOrigin: a gated read that prepares a UI
// surface the CEO just opened, not the CEO's own immediate tool-call turn
// (never gate.P0).
const workspaceOrigin = gate.P1

// workspaceSectionLimit bounds how many of a workspace's filtered
// meetings/threads/needs-you rows this endpoint returns: a preview list
// below the control-room tiles (docs/slices/UI.md Phase 5a: "recent
// meetings and threads"), not a full listing (GET /v1/meetings and GET
// /v1/threads already exist for that).
const workspaceSectionLimit = 10

// WorkspaceView is GET /v1/workspaces/{id}'s payload: the spec's identity,
// its filtered existing sections (found via in_workspace edges, read-only),
// and its own control-room tiles for whichever template it names. Only one
// of Project/Finance/Clients is ever set, matching spec.Template; the other
// templates (people/ideas/research/marketing) get no extra tiles this
// sub-phase — Phase 5b-5d's own job, docs/slices/UI.md Phase 5a's own
// scoping note.
type WorkspaceView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Template    string `json:"template"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`

	NeedsYou []needsyou.Item `json:"needs_you"`
	Meetings []meetingView   `json:"meetings"`
	Threads  []threadView    `json:"threads"`

	Project *projectWorkspaceView `json:"project,omitempty"`
	Finance *financeWorkspaceView `json:"finance,omitempty"`
	Clients *clientsWorkspaceView `json:"clients,omitempty"`
}

// handleGetWorkspace serves GET /v1/workspaces/{id}. An unknown id (or no
// loaded registry at all) is a 404, matching handleGetDashboard's own
// convention for an unknown dashboard id.
func (d *Daemon) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	if d.cfg.Workspaces == nil {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	spec, ok := d.cfg.Workspaces.Spec(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	view := WorkspaceView{ID: spec.ID, Name: spec.Name, Template: spec.Template, Source: spec.Source, Description: spec.Description}
	view.NeedsYou, view.Meetings, view.Threads = d.workspaceSections(ctx, spec.ID)
	if view.NeedsYou == nil {
		view.NeedsYou = []needsyou.Item{}
	}
	if view.Meetings == nil {
		view.Meetings = []meetingView{}
	}
	if view.Threads == nil {
		view.Threads = []threadView{}
	}

	switch spec.Template {
	case "project":
		view.Project = d.projectWorkspaceView(ctx, spec)
	case "finance":
		view.Finance = d.financeWorkspaceView(ctx)
	case "clients":
		view.Clients = d.clientsWorkspaceView(ctx)
		// people, ideas, research, marketing: no extra tiles this sub-phase
		// (Phase 5b-5d's own job) -- the base fields above are already a
		// valid, non-crashing shape.
	}
	writeJSON(w, http.StatusOK, view)
}

// workspaceSections finds a workspace's filtered existing sections via
// in_workspace edges (store.LinkInWorkspace, "decision/meeting/job/thread/
// project -> workspace"): every record some writer has already linked to
// this workspace, rendered read-only with each record kind's own existing
// view (meetingViewOf, viewThread) or, for a decision, filtered from the
// current needs-you snapshot by card id. Nothing here writes anything.
func (d *Daemon) workspaceSections(ctx context.Context, workspaceID string) (needsYou []needsyou.Item, meetings []meetingView, threads []threadView) {
	if d.cfg.Store == nil {
		return nil, nil, nil
	}
	links, err := d.cfg.Store.LinksTo(ctx, "workspace", workspaceID, store.LinkInWorkspace)
	if err != nil {
		return nil, nil, nil
	}
	var decisionIDs map[string]bool
	for _, l := range links {
		switch l.FromType {
		case "decision":
			if decisionIDs == nil {
				decisionIDs = map[string]bool{}
			}
			decisionIDs[l.FromID] = true
		case "meeting":
			row, err := d.cfg.Store.GetMeetingSession(ctx, l.FromID)
			if err != nil {
				continue
			}
			meetings = append(meetings, d.meetingViewOf(ctx, row))
		case "thread":
			t, err := d.cfg.Store.GetThread(ctx, l.FromID)
			if err != nil {
				continue
			}
			threads = append(threads, viewThread(t, false))
		}
	}
	sort.Slice(meetings, func(i, j int) bool { return meetings[i].StartedAt.After(meetings[j].StartedAt) })
	if len(meetings) > workspaceSectionLimit {
		meetings = meetings[:workspaceSectionLimit]
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].UpdatedAt.After(threads[j].UpdatedAt) })
	if len(threads) > workspaceSectionLimit {
		threads = threads[:workspaceSectionLimit]
	}
	if decisionIDs != nil && d.cfg.NeedsYou != nil {
		for _, it := range d.cfg.NeedsYou.Snapshot() {
			if it.Kind == needsyou.KindDecision && decisionIDs[it.ID] {
				needsYou = append(needsYou, it)
				if len(needsYou) >= workspaceSectionLimit {
					break
				}
			}
		}
	}
	return needsYou, meetings, threads
}

// ---- shared tile shapes ----

// metricTileView is one plain number tile (open PRs, merged this week,
// blocked issues, ...): a value plus how it got that value
// (dashboards.TileState), reusing internal/dashboards' own state enum
// rather than a second one.
type metricTileView struct {
	Value float64              `json:"value,omitempty"`
	State dashboards.TileState `json:"state"`
}

// computeNow is this handler's clock: internal/dashboards.Compute's own
// Now field when Compute is configured (so a test can pin "today" exactly
// like compute_test.go's fixedClock does for Finance/Delivery/Clients
// dashboard tests), else time.Now -- the same nil-defaults-to-time.Now
// convention internal/dashboards.Compute.now and internal/runtime.Env.Now
// already use.
func (d *Daemon) computeNow() time.Time {
	if d.cfg.Compute != nil && d.cfg.Compute.Now != nil {
		return d.cfg.Compute.Now()
	}
	return time.Now()
}

// workspaceInvoker returns the same gate.Invoke path internal/dashboards'
// own Compute.Gate already wraps (internal/cli/cmd_daemon.go wires
// Compute.Gate to the same *gate.Gate as Config.Gate), reused directly here
// for the GitHub calls (list_prs, list_commits) Compute's own no-argument
// fetchJSON registry doesn't cover, rather than adding a second Invoker
// field to Config. nil when Compute itself isn't configured (matching every
// other handler's "missing optional dependency degrades, never crashes"
// posture).
func (d *Daemon) workspaceInvoker() dashboards.Invoker {
	if d.cfg.Compute != nil && d.cfg.Compute.Gate != nil {
		return d.cfg.Compute.Gate
	}
	return nil
}

// workspaceClassify mirrors internal/dashboards' own classify: not_connected
// for the gate's credential-missing denial (dashboards.IsNotConnected),
// unavailable for anything else, ok for no error.
func workspaceClassify(err error) dashboards.TileState {
	if err == nil {
		return dashboards.TileOK
	}
	if dashboards.IsNotConnected(err) {
		return dashboards.TileNotConnected
	}
	return dashboards.TileUnavailable
}

// cellString/cellFloat read one sheet row's column by index, tolerating a
// short row or the wrong JSON type rather than panicking -- the same small
// helpers internal/dashboards/compute.go's own cellString/cellFloat
// implement (unexported there, so duplicated here rather than exported
// purely for this file's convenience).
func cellString(row []any, i int) (string, bool) {
	if i < 0 || i >= len(row) {
		return "", false
	}
	s, ok := row[i].(string)
	return s, ok
}

func cellFloat(row []any, i int) (float64, bool) {
	if i < 0 || i >= len(row) {
		return 0, false
	}
	switch v := row[i].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

// ---- Project workspace ----

// issueSummary is one Linear issue as a project workspace's "Building now"
// list renders it.
type issueSummary struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

// issueListTile is a list of issues plus a tile state.
type issueListTile struct {
	State dashboards.TileState `json:"state"`
	Items []issueSummary       `json:"items,omitempty"`
}

// progressTileView is the project workspace's progress bar (docs/
// slices/UI.md Phase 5a): the earliest upcoming roster TargetAt among the
// workspace's own Linear teams' projects, plus the share of that Linear
// team's issues done. The web/Swift client renders this with <progress>/
// <meter> (value=Done, max=Total), never a styled div.
type progressTileView struct {
	State    dashboards.TileState `json:"state"`
	TargetAt *time.Time           `json:"target_at,omitempty"`
	Done     float64              `json:"done,omitempty"`
	Total    float64              `json:"total,omitempty"`
}

// commitBarsTile is the 4-week commit bars tile: one commit count per week,
// oldest first, from the new github.list_commits R function.
type commitBarsTile struct {
	State dashboards.TileState `json:"state"`
	Weeks []int                `json:"weeks,omitempty"`
}

// projectWorkspaceView is a project-template workspace's control-room
// tiles.
type projectWorkspaceView struct {
	Progress       progressTileView `json:"progress"`
	OpenPRs        metricTileView   `json:"open_prs"`
	MergedThisWeek metricTileView   `json:"merged_this_week"`
	BlockedIssues  metricTileView   `json:"blocked_issues"`
	BuildingNow    issueListTile    `json:"building_now"`
	CommitBars     commitBarsTile   `json:"commit_bars"`
}

func (d *Daemon) projectWorkspaceView(ctx context.Context, spec workspaces.Spec) *projectWorkspaceView {
	now := d.computeNow()
	issues, issueState := d.projectTeamIssues(ctx, spec)
	target, hasTarget := d.projectEarliestTarget(ctx, spec.Teams)
	openPRs, merged, bars := d.projectGitHubTiles(ctx, spec, now)
	return &projectWorkspaceView{
		Progress:       projectProgressTile(issues, issueState, target, hasTarget),
		OpenPRs:        openPRs,
		MergedThisWeek: merged,
		BlockedIssues:  projectBlockedIssuesTile(issues, issueState),
		BuildingNow:    projectBuildingNowTile(issues, issueState),
		CommitBars:     bars,
	}
}

// projectTeamIssues fetches every Linear issue (dashboards.Issues -- the
// same cached, gated read the Delivery dashboard uses) and filters to
// spec's own team keys (workspaces.Spec.Teams), a project workspace's
// Linear scope.
func (d *Daemon) projectTeamIssues(ctx context.Context, spec workspaces.Spec) ([]linear.Issue, dashboards.TileState) {
	if d.cfg.Compute == nil {
		return nil, dashboards.TileUnavailable
	}
	if len(spec.Teams) == 0 {
		return nil, dashboards.TileUnavailable
	}
	all, state, _ := dashboards.Issues(ctx, d.cfg.Compute)
	if state != dashboards.TileOK {
		return nil, state
	}
	teams := map[string]bool{}
	for _, k := range spec.Teams {
		teams[k] = true
	}
	var out []linear.Issue
	for _, is := range all {
		if teams[is.Team] {
			out = append(out, is)
		}
	}
	return out, dashboards.TileOK
}

// projectEarliestTarget resolves teamKeys (Linear team keys) to their
// roster teams (roster.TeamByLinearKey) and then to every project on each
// team (roster.ProjectsForTeam), returning the earliest non-zero TargetAt
// among them. Judgment call: docs/slices/UI.md Phase 5a says "the project's
// TargetAt" (singular), but a roster team can own more than one project
// (twins/ceo/seed/people.yaml: crawler has both ingestion-v2 and
// ranking-quality) -- there is no other deterministic link from a project
// workspace (scoped by Linear team) to exactly one roster project. The
// earliest upcoming date is the most actionable single date to show on a
// progress bar, so that is what this returns; ok is false when no team
// resolves or no project under it has a target date at all.
func (d *Daemon) projectEarliestTarget(ctx context.Context, teamKeys []string) (target time.Time, ok bool) {
	if d.cfg.Store == nil {
		return time.Time{}, false
	}
	for _, key := range teamKeys {
		team, err := roster.TeamByLinearKey(ctx, d.cfg.Store, key)
		if err != nil {
			continue
		}
		projectIDs, err := roster.ProjectsForTeam(ctx, d.cfg.Store, team.SourceID)
		if err != nil {
			continue
		}
		for _, pid := range projectIDs {
			p, err := store.Get[store.Project](ctx, d.cfg.Store, "seed", pid)
			if err != nil || p.TargetAt.IsZero() {
				continue
			}
			if !ok || p.TargetAt.Before(target) {
				target, ok = p.TargetAt, true
			}
		}
	}
	return target, ok
}

// linearIssueOpen mirrors internal/dashboards.isOpenStateType: Linear's own
// coarse state.type is triage|backlog|unstarted|started|completed|
// cancelled; "open" is anything that is not a finished state. Duplicated
// (rather than exported from internal/dashboards purely for this one-line
// use) since it is a trivial, self-contained predicate.
func linearIssueOpen(stateType string) bool {
	return stateType != "completed" && stateType != "cancelled"
}

func projectProgressTile(issues []linear.Issue, state dashboards.TileState, target time.Time, hasTarget bool) progressTileView {
	v := progressTileView{State: state}
	if hasTarget {
		t := target
		v.TargetAt = &t
	}
	if state != dashboards.TileOK {
		return v
	}
	var done, total float64
	for _, is := range issues {
		total++
		if is.StateType == "completed" {
			done++
		}
	}
	v.Done, v.Total = done, total
	return v
}

// projectBlockedIssuesTile counts this team's open issues that have an
// inverse "blocks" relation whose blocker is itself still open -- exactly
// internal/dashboards.deliveryBlockedIssues' own definition, scoped to an
// already-team-filtered issue slice.
func projectBlockedIssuesTile(issues []linear.Issue, state dashboards.TileState) metricTileView {
	if state != dashboards.TileOK {
		return metricTileView{State: state}
	}
	n := 0
	for _, is := range issues {
		if !linearIssueOpen(is.StateType) {
			continue
		}
		for _, rel := range is.InverseRelations {
			if rel.Type == "blocks" && linearIssueOpen(rel.StateType) {
				n++
				break
			}
		}
	}
	return metricTileView{Value: float64(n), State: dashboards.TileOK}
}

// projectBuildingNowTile lists this team's in-progress issues (Linear
// state.type "started"), sorted by identifier for a deterministic order.
func projectBuildingNowTile(issues []linear.Issue, state dashboards.TileState) issueListTile {
	if state != dashboards.TileOK {
		return issueListTile{State: state}
	}
	var items []issueSummary
	for _, is := range issues {
		if is.StateType == "started" {
			items = append(items, issueSummary{Identifier: is.Identifier, Title: is.Title})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Identifier < items[j].Identifier })
	return issueListTile{Items: items, State: dashboards.TileOK}
}

// projectGitHubTiles computes the PR and commit tiles (docs/slices/UI.md
// Phase 5a): "Connect GitHub" -- rendered as dashboards.TileNotConnected,
// never a number -- when the workspace's spec has no github: source at
// all (a pure spec check, no call), or when the github.list_prs/
// list_commits call itself comes back not_connected (a missing credential,
// detected exactly like internal/dashboards' own not_connected tiles via
// dashboards.IsNotConnected -- the same detection mechanism, not a second
// one). Both cases render the identical tile state.
func (d *Daemon) projectGitHubTiles(ctx context.Context, spec workspaces.Spec, now time.Time) (openPRs, merged metricTileView, bars commitBarsTile) {
	repo, ok := strings.CutPrefix(spec.Source, "github:")
	if !ok || repo == "" {
		return metricTileView{State: dashboards.TileNotConnected}, metricTileView{State: dashboards.TileNotConnected}, commitBarsTile{State: dashboards.TileNotConnected}
	}
	inv := d.workspaceInvoker()
	if inv == nil {
		return metricTileView{State: dashboards.TileUnavailable}, metricTileView{State: dashboards.TileUnavailable}, commitBarsTile{State: dashboards.TileUnavailable}
	}

	prState := dashboards.TileUnavailable
	if res, err := inv.Invoke(ctx, gate.Call{Function: "github.list_prs", Args: map[string]any{"state": "all"}, Origin: workspaceOrigin, Taint: gate.Clean}); err == nil {
		var prs []github.PR
		if derr := json.Unmarshal(res.Output, &prs); derr != nil {
			prState = dashboards.TileUnavailable
		} else {
			prState = dashboards.TileOK
			weekAgo := now.Add(-7 * 24 * time.Hour)
			var openN, mergedN float64
			for _, pr := range prs {
				switch pr.State {
				case "open":
					openN++
				case "merged":
					if at, perr := time.Parse(time.RFC3339, pr.UpdatedAt); perr == nil && !at.Before(weekAgo) && !at.After(now) {
						mergedN++
					}
				}
			}
			openPRs.Value, merged.Value = openN, mergedN
		}
	} else {
		prState = workspaceClassify(err)
	}
	openPRs.State, merged.State = prState, prState

	commitsSince := now.Add(-28 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if res, err := inv.Invoke(ctx, gate.Call{Function: "github.list_commits", Args: map[string]any{"since": commitsSince}, Origin: workspaceOrigin, Taint: gate.Clean}); err == nil {
		var commits []github.Commit
		if derr := json.Unmarshal(res.Output, &commits); derr != nil {
			bars = commitBarsTile{State: dashboards.TileUnavailable}
		} else {
			bars = commitBarsTile{State: dashboards.TileOK, Weeks: weeklyCommitBars(commits, now)}
		}
	} else {
		bars = commitBarsTile{State: workspaceClassify(err)}
	}
	return openPRs, merged, bars
}

// weeklyCommitBars buckets commits into 4 weekly bins ending at now, oldest
// first (bars[0] = 22-28 days ago, bars[3] = the last 7 days). A commit
// outside that 4-week window (or with an unparseable date) is dropped.
func weeklyCommitBars(commits []github.Commit, now time.Time) []int {
	bars := make([]int, 4)
	for _, c := range commits {
		at, err := time.Parse(time.RFC3339, c.CommittedAt)
		if err != nil {
			continue
		}
		age := now.Sub(at)
		if age < 0 || age >= 28*24*time.Hour {
			continue
		}
		idx := 3 - int(age/(7*24*time.Hour))
		if idx < 0 {
			idx = 0
		}
		if idx > 3 {
			idx = 3
		}
		bars[idx]++
	}
	return bars
}

// ---- Finance workspace ----

// invoiceView is one company_finance.outstanding_invoices row plus its
// computed aging (docs/slices/UI.md Phase 5a: "invoices with aging"). Column
// mapping matches internal/dashboards/compute.go's own invoiceColAccount/
// invoiceColAmount documentation (gsheets_test.go's own fixture:
// ["inv-1","Acme",5000,"2026-09-01","Open"]): id, account, amount, date,
// status.
type invoiceView struct {
	ID        string  `json:"id"`
	Account   string  `json:"account"`
	AmountUSD float64 `json:"amount_usd"`
	Date      string  `json:"date"`
	Status    string  `json:"status"`
	AgingDays float64 `json:"aging_days,omitempty"`
}

type invoiceListTile struct {
	State dashboards.TileState `json:"state"`
	Items []invoiceView        `json:"items,omitempty"`
}

// vendorView is one roster vendor plus its renewal date (docs/slices/UI.md
// Phase 5a: "vendor renewals from roster vendors").
type vendorView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Product    string     `json:"product"`
	MonthlyUSD float64    `json:"monthly_usd"`
	RenewalAt  *time.Time `json:"renewal_at,omitempty"`
}

type vendorListTile struct {
	State dashboards.TileState `json:"state"`
	Items []vendorView         `json:"items,omitempty"`
}

// financeWorkspaceView is the finance-template workspace's control-room
// tiles: the Finance dashboard's own tiles (reused directly, Phase 4's
// compute registry -- see d.cfg.Compute.Dashboard below), invoices with
// aging, and roster vendor renewals.
type financeWorkspaceView struct {
	Dashboard dashboards.DashboardView `json:"dashboard"`
	Invoices  invoiceListTile          `json:"invoices"`
	Vendors   vendorListTile           `json:"vendors"`
}

func (d *Daemon) financeWorkspaceView(ctx context.Context) *financeWorkspaceView {
	view := &financeWorkspaceView{Invoices: d.financeInvoicesTile(ctx), Vendors: d.financeVendorsTile(ctx)}
	if d.cfg.Dashboards != nil && d.cfg.Compute != nil {
		if dspec, ok := d.cfg.Dashboards.Spec("finance"); ok {
			view.Dashboard = d.cfg.Compute.Dashboard(ctx, dspec)
		}
	}
	return view
}

func (d *Daemon) financeInvoicesTile(ctx context.Context) invoiceListTile {
	if d.cfg.Compute == nil {
		return invoiceListTile{State: dashboards.TileUnavailable}
	}
	rows, state, _ := dashboards.Rows(ctx, d.cfg.Compute, "company_finance.outstanding_invoices")
	if state != dashboards.TileOK {
		return invoiceListTile{State: state}
	}
	now := d.computeNow()
	var items []invoiceView
	for _, row := range rows {
		account, ok := cellString(row, 1)
		if !ok || account == "" {
			continue
		}
		iv := invoiceView{Account: account}
		if id, ok := cellString(row, 0); ok {
			iv.ID = id
		}
		if amt, ok := cellFloat(row, 2); ok {
			iv.AmountUSD = amt
		}
		if date, ok := cellString(row, 3); ok {
			iv.Date = date
			if t, perr := time.Parse("2006-01-02", strings.TrimSpace(date)); perr == nil {
				iv.AgingDays = now.Sub(t).Hours() / 24
			}
		}
		if status, ok := cellString(row, 4); ok {
			iv.Status = status
		}
		items = append(items, iv)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AgingDays > items[j].AgingDays })
	return invoiceListTile{Items: items, State: dashboards.TileOK}
}

func (d *Daemon) financeVendorsTile(ctx context.Context) vendorListTile {
	if d.cfg.Store == nil {
		return vendorListTile{State: dashboards.TileUnavailable}
	}
	vendors, err := store.List[store.Vendor](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil {
		return vendorListTile{State: dashboards.TileUnavailable}
	}
	var items []vendorView
	for _, v := range vendors {
		vv := vendorView{ID: v.SourceID, Name: v.Name, Product: v.Product, MonthlyUSD: float64(v.MonthlyMinor) / 100}
		if !v.RenewalAt.IsZero() {
			t := v.RenewalAt
			vv.RenewalAt = &t
		}
		items = append(items, vv)
	}
	sort.Slice(items, func(i, j int) bool {
		ri, rj := items[i].RenewalAt, items[j].RenewalAt
		switch {
		case ri == nil && rj == nil:
			return items[i].Name < items[j].Name
		case ri == nil:
			return false
		case rj == nil:
			return true
		default:
			return ri.Before(*rj)
		}
	})
	return vendorListTile{Items: items, State: dashboards.TileOK}
}

// ---- Clients workspace ----

// clientAccountView is one company_customers.accounts row (docs/
// slices/UI.md Phase 5a: "accounts with health, days since contact, and
// open tickets"), plus revenue joined from Finance (OutstandingUSD -- the
// same company_finance.outstanding_invoices-by-account-name join Phase 4's
// own longest_silent_account_with_balance callout already established;
// judgment call, see clientsAccountsTile's own doc comment) and the
// account's owner resolved via the roster's owns_client link.
type clientAccountView struct {
	Name             string   `json:"name"`
	Health           string   `json:"health,omitempty"`
	OpenTickets      float64  `json:"open_tickets,omitempty"`
	NPS              *float64 `json:"nps,omitempty"`
	DaysSinceContact *float64 `json:"days_since_contact,omitempty"`
	OutstandingUSD   float64  `json:"outstanding_usd,omitempty"`
	OwnerName        string   `json:"owner_name,omitempty"`
	OwnerInitials    string   `json:"owner_initials,omitempty"`
}

type clientAccountsTile struct {
	State dashboards.TileState `json:"state"`
	Items []clientAccountView  `json:"items,omitempty"`
}

// clientsWorkspaceView is the clients-template workspace's control-room
// tiles: the Clients dashboard's own tiles (reused directly, Phase 4's
// compute registry) plus the per-account list.
type clientsWorkspaceView struct {
	Dashboard dashboards.DashboardView `json:"dashboard"`
	Accounts  clientAccountsTile       `json:"accounts"`
}

func (d *Daemon) clientsWorkspaceView(ctx context.Context) *clientsWorkspaceView {
	view := &clientsWorkspaceView{Accounts: d.clientsAccountsTile(ctx)}
	if d.cfg.Dashboards != nil && d.cfg.Compute != nil {
		if dspec, ok := d.cfg.Dashboards.Spec("clients"); ok {
			view.Dashboard = d.cfg.Compute.Dashboard(ctx, dspec)
		}
	}
	return view
}

// clientsAccountsTile lists company_customers.accounts (health, open
// tickets, NPS, days since contact), joined against
// company_finance.outstanding_invoices by account name for "revenue
// (Finance)" -- docs/slices/UI.md Phase 5a's own parenthetical naming
// Finance as this field's source. Judgment call: the plan doesn't define
// what "revenue" means precisely for a per-account row (there is no
// per-account revenue/MRR column on either sheet Phase 4 already reads);
// this reuses the one cross-connector join Phase 4 itself already built for
// exactly these two sheets (clientsSilentAccountCallout, "money
// outstanding"), rather than inventing an unestablished mapping, and names
// the JSON field outstanding_usd rather than "revenue" so the client never
// mislabels an amount owed as money already earned.
func (d *Daemon) clientsAccountsTile(ctx context.Context) clientAccountsTile {
	if d.cfg.Compute == nil {
		return clientAccountsTile{State: dashboards.TileUnavailable}
	}
	rows, state, _ := dashboards.Rows(ctx, d.cfg.Compute, "company_customers.accounts")
	if state != dashboards.TileOK {
		return clientAccountsTile{State: state}
	}
	balances := map[string]float64{}
	if invRows, invState, _ := dashboards.Rows(ctx, d.cfg.Compute, "company_finance.outstanding_invoices"); invState == dashboards.TileOK {
		for _, row := range invRows {
			name, ok := cellString(row, 1)
			if !ok {
				continue
			}
			amt, ok := cellFloat(row, 2)
			if !ok {
				continue
			}
			balances[strings.ToLower(strings.TrimSpace(name))] += amt
		}
	}
	now := d.computeNow()
	var items []clientAccountView
	for _, row := range rows {
		name, ok := cellString(row, 0)
		if !ok || name == "" {
			continue
		}
		av := clientAccountView{Name: name}
		if h, ok := cellString(row, 1); ok {
			av.Health = strings.TrimSpace(h)
		}
		if tk, ok := cellFloat(row, 2); ok {
			av.OpenTickets = tk
		}
		if nps, ok := cellFloat(row, 3); ok {
			n := nps
			av.NPS = &n
		}
		if lc, ok := cellString(row, 4); ok {
			if t, perr := time.Parse("2006-01-02", strings.TrimSpace(lc)); perr == nil {
				days := now.Sub(t).Hours() / 24
				av.DaysSinceContact = &days
			}
		}
		if bal, ok := balances[strings.ToLower(strings.TrimSpace(name))]; ok {
			av.OutstandingUSD = bal
		}
		if ownerName, initials, ok := d.clientOwner(ctx, name); ok {
			av.OwnerName, av.OwnerInitials = ownerName, initials
		}
		items = append(items, av)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return clientAccountsTile{Items: items, State: dashboards.TileOK}
}

// clientOwner resolves accountName (a company_customers.accounts row's own
// name string) to a roster client with a case-insensitive, trimmed name
// match, then to that client's owner via the roster's owns_client link
// (store.LinkOwnsClient, person -> client). ok is false on any miss: no
// matching roster client, or a client with no recorded owner.
func (d *Daemon) clientOwner(ctx context.Context, accountName string) (name, initials string, ok bool) {
	if d.cfg.Store == nil {
		return "", "", false
	}
	clients, err := store.List[store.Client](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil {
		return "", "", false
	}
	target := strings.ToLower(strings.TrimSpace(accountName))
	clientID := ""
	for _, c := range clients {
		if strings.ToLower(strings.TrimSpace(c.Name)) == target {
			clientID = c.SourceID
			break
		}
	}
	if clientID == "" {
		return "", "", false
	}
	links, err := d.cfg.Store.LinksTo(ctx, "client", clientID, store.LinkOwnsClient)
	if err != nil || len(links) == 0 {
		return "", "", false
	}
	p, err := store.Get[store.Person](ctx, d.cfg.Store, "seed", links[0].FromID)
	if err != nil {
		return "", "", false
	}
	return p.Name, Initials(p.Name), true
}
