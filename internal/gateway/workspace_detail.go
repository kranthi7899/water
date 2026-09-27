// This file implements docs/slices/UI.md Phase 5a: GET /v1/workspaces/{id},
// a workspace's filtered existing sections (found via in_workspace edges)
// plus its own control-room tiles, shaped per Spec.Template. Phase 5b
// (docs/slices/UI.md, "5b. People") extends it with the people-template
// tiles (resource bar, team tiles, cross-team callout) and one new route,
// POST /v1/workspaces/{id}/drafts, for the People workspace's "Message a
// team"/"Send pulse check" buttons.
//
// THE INVARIANT: no workspace control may ever create a decision. This file
// never imports water/internal/decisions and never calls
// water/internal/store's SetCardState/SetCardActionState/
// SetDecisionClassification/UpsertDecisionRecord — every function below is
// a pure read, built only from Gate.Invoke (a read-level R function),
// internal/dashboards' own gated-read reuse (Rows/Issues/Dashboard) and
// plain store/roster lookups, plus (Phase 5b) store.CreateDraft, which
// creates a drafts row — never an approval envelope and never a decision
// card. TestWorkspaceDetailRoutesNeverWriteADecision
// (workspace_detail_test.go) proves this at the route level with a
// counting store; TestWorkspaceDetailFileNeverReferencesDecisionWrites
// (same file) proves it by scanning this file's own AST, mirroring
// internal/dashboards' own TestComputeHasNoHardcodedMetricValues scan. Both
// tests cover this file's Phase 5b additions too, since they scan/exercise
// this whole file and every route in it, not just Phase 5a's three.
//
// THE MORALE INVARIANT (docs/slices/UI.md Phase 5b, its own named
// acceptance item): "Morale comes only from load, overdue work and pulse
// answers people chose to give." See peopleStrain's own doc comment below
// for how its signature makes this checkable by inspection, and
// TestPeopleStrainSignatureCarriesNoTextParameter
// (workspace_detail_test.go) for the AST-based proof.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
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
// of Project/Finance/Clients/People is ever set, matching spec.Template;
// the remaining templates (ideas/research/marketing) get no extra tiles yet
// — Phase 5c-5d's own job, docs/slices/UI.md Phase 5a's own scoping note.
type WorkspaceView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Template    string `json:"template"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`

	NeedsYou []needsyou.Item `json:"needs_you"`
	Meetings []meetingView   `json:"meetings"`
	Threads  []threadView    `json:"threads"`

	Project  *projectWorkspaceView  `json:"project,omitempty"`
	Finance  *financeWorkspaceView  `json:"finance,omitempty"`
	Clients  *clientsWorkspaceView  `json:"clients,omitempty"`
	People   *peopleWorkspaceView   `json:"people,omitempty"`
	Ideas    *ideasWorkspaceView    `json:"ideas,omitempty"`
	Research *researchWorkspaceView `json:"research,omitempty"`
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
	case "people":
		view.People = d.peopleWorkspaceView(ctx)
	case "ideas":
		view.Ideas = d.ideasWorkspaceView(ctx)
	case "research":
		view.Research = d.researchWorkspaceView(ctx)
		// marketing: no extra tiles yet (Phase 5d's own job) -- the base
		// fields above are already a valid, non-crashing shape.
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

// ---- People workspace (docs/slices/UI.md Phase 5b) ----
//
// A resource bar (budget left, hours this week, people with free capacity),
// one team tile per roster Team (a strain border, member initials coloured
// by individual load, a load bar, an overdue count), and a cross-team
// callout when the same person is on deadlines across different teams
// within 7 days of each other. Every number here is derived at request
// time from roster.HoursInvested/PersonProjectHours-style allocation data
// and internal/dashboards' own cached Linear read (dashboards.Issues,
// shared with the Project workspace's own tiles) -- nothing is stored.
//
// Judgment call: roster has no "budget" concept at team or company
// granularity (twins/ceo/seed/people.yaml's own `company:` block carries
// only headcount and a blended hourly cost, and internal/roster.Write
// never even persists that block to the store -- see roster.go's own
// yamlCompany/Write). The nearest real number this codebase already
// computes is the Finance dashboard's own "cash_position" metric (company
// cash on hand, internal/dashboards/compute.go's financeCashMetric), so
// peopleBudgetLeftTile reuses that directly (same gated, cached read the
// Finance workspace/dashboard already share) rather than inventing a new
// roster schema element this phase isn't scoped to add. This is
// company-wide cash, not a per-team budget -- documented here rather than
// silently relabelled as something more precise than it is.
const (
	// peopleHoursPerWeek mirrors internal/roster/hours.go's own unexported
	// hoursPerWeek (a full allocation is a 40h work week) -- duplicated
	// here for the same reason this file's cellString/cellFloat duplicate
	// internal/dashboards' own unexported helpers (see cellString's own
	// doc comment above): a trivial, self-contained constant, not worth a
	// second exported package surface.
	peopleHoursPerWeek = 40

	// issuesPerLoadUnit is a documented judgment call: docs/slices/UI.md
	// Phase 5b names no exact formula for combining "Linear assignee
	// counts plus allocation" into one load number, only that both must
	// contribute. Every 5 currently-open Linear issues assigned to a
	// person adds one full load unit on top of their roster allocation
	// fraction -- a deliberately round, easy-to-explain number with no
	// real usage history to calibrate against yet. This is the one place
	// to retune it once real Linear volume is observed.
	issuesPerLoadUnit = 5

	// Load-to-strain thresholds. A load of 1.0 reads as "exactly one
	// person-week of roster allocation, no extra Linear churn on top" --
	// the natural "fully loaded" baseline personLoad/teamLoad are built
	// around. Below strainLowMax is under-loaded; at or above
	// strainHighMin is over-loaded; the (inclusive) range between the two
	// is normal.
	strainLowMax  = 0.7
	strainHighMin = 1.2

	// strainOverdueBump: carrying this many or more overdue open issues
	// bumps strain up one level even when the load number alone would
	// read normal -- overdue work is its own signal (docs/slices/UI.md
	// Phase 5b: "load, overdue work and pulse answers"), not just a
	// downstream effect of load.
	strainOverdueBump = 3

	// teamLoadMeterMax bounds the team load bar's <meter> max: a team
	// averaging double its nominal capacity is already the top of the
	// visible scale.
	teamLoadMeterMax = 2.0

	// crossTeamWindow is the plan's own "within 7 days of each other".
	crossTeamWindowDays = 7
)

// StrainLevel is peopleStrain's result: exactly "low", "normal" or "high".
// A defined type (not a bare string) so the JSON field it fills
// (teamTileView.Strain, teamMemberView.LoadLevel) always carries one of
// these three values.
type StrainLevel string

const (
	StrainLow    StrainLevel = "low"
	StrainNormal StrainLevel = "normal"
	StrainHigh   StrainLevel = "high"
)

// PulseAnswer is one pulse-check response a person chose to give: a
// fixed-scale self-report, never free text (THE MORALE INVARIANT above).
// Score runs 1 (overloaded) to 5 (plenty of spare capacity); 3 is neutral.
//
// No pulse-response storage exists yet: this phase's "Send pulse check"
// button (handleCreateWorkspaceDraft below) only ever creates a drafts row
// with a code-built question in it -- there is no endpoint anywhere yet
// that records a reply. peopleStrain's own pulseAnswers argument is
// therefore always nil today. It is part of the signature now, rather than
// added by a later phase (which would have to reopen and re-audit this
// exact function's no-text-parameter guarantee), so a future phase can
// wire in real answers without ever widening peopleStrain to accept
// anything text-shaped.
type PulseAnswer struct {
	Score int
}

// averagePulseScore is peopleStrain's own helper: the mean Score across
// answers, and whether there were any at all (an empty slice never
// pretends to have an opinion).
func averagePulseScore(answers []PulseAnswer) (float64, bool) {
	if len(answers) == 0 {
		return 0, false
	}
	var sum int
	for _, a := range answers {
		sum += a.Score
	}
	return float64(sum) / float64(len(answers)), true
}

// peopleStrain is THIS FILE'S one morale/strain compute function
// (docs/slices/UI.md Phase 5b's own named acceptance item, THE MORALE
// INVARIANT in this file's own top-of-file doc comment): float64, int and
// []PulseAnswer are its only parameters. There is no string parameter, no
// []byte, nothing that could carry a message body, a Slack/email excerpt
// or any other free text -- so it is structurally impossible for message
// content to ever reach a strain computation through this function, not
// merely a convention this file happens to follow.
// TestPeopleStrainSignatureCarriesNoTextParameter
// (workspace_detail_test.go) parses this file's own AST and checks this
// exact function's parameter types, proving that by inspection rather than
// by a bare unit test's behaviour alone.
//
// load is personLoad/teamLoad's own combined allocation-plus-Linear-churn
// number (see their own doc comments); overdueCount is a raw count of open,
// past-due issues; pulseAnswers is discussed on PulseAnswer's own doc
// comment above (always nil today).
func peopleStrain(load float64, overdueCount int, pulseAnswers []PulseAnswer) StrainLevel {
	level := 1 // 0 low, 1 normal, 2 high
	switch {
	case load > strainHighMin:
		level = 2
	case load < strainLowMax:
		level = 0
	}
	if overdueCount >= strainOverdueBump && level < 2 {
		level++
	}
	if avg, ok := averagePulseScore(pulseAnswers); ok {
		switch {
		case avg <= 2 && level < 2:
			level++
		case avg >= 4 && level > 0:
			level--
		}
	}
	switch level {
	case 0:
		return StrainLow
	case 2:
		return StrainHigh
	default:
		return StrainNormal
	}
}

// personLoad combines a roster allocation fraction with a Linear
// assignee-count signal (docs/slices/UI.md Phase 5b: "Load comes from
// Linear assignee counts plus allocation ... not just one or the other
// alone"): the person's own summed allocated_to fraction, plus their
// currently-open Linear-assigned issue count divided by issuesPerLoadUnit.
func personLoad(allocationFraction float64, openAssignedIssues int) float64 {
	return allocationFraction + float64(openAssignedIssues)/issuesPerLoadUnit
}

func averageFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// peopleResourceBarView is the People workspace's resource bar.
type peopleResourceBarView struct {
	BudgetLeftUSD          metricTileView `json:"budget_left_usd"`
	HoursThisWeek          metricTileView `json:"hours_this_week"`
	PeopleWithFreeCapacity metricTileView `json:"people_with_free_capacity"`
}

// teamMemberView is one team tile's own member row: initials coloured by
// individual load (docs/slices/UI.md Phase 5b).
type teamMemberView struct {
	Name      string      `json:"name"`
	Initials  string      `json:"initials"`
	Load      float64     `json:"load"`
	LoadLevel StrainLevel `json:"load_level"`
}

// teamTileView is one roster Team's People-workspace tile: a strain border,
// member initials coloured by individual load, a load bar and an overdue
// count.
type teamTileView struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Strain  StrainLevel          `json:"strain"`
	Load    float64              `json:"load"`
	LoadMax float64              `json:"load_max"`
	Overdue float64              `json:"overdue"`
	Members []teamMemberView     `json:"members"`
	State   dashboards.TileState `json:"state"`
}

// crossTeamDeadlineView is one leg of a cross-team callout: the issue and
// the roster team its own Linear team key resolves to (never the person's
// home team -- see peopleCrossTeamCallouts' own doc comment).
type crossTeamDeadlineView struct {
	Team            string `json:"team"`
	IssueIdentifier string `json:"issue_identifier"`
	DueDate         string `json:"due_date"`
}

// crossTeamCalloutView is docs/slices/UI.md Phase 5b's cross-team callout:
// one person who has deadlines on more than one roster team within
// crossTeamWindowDays of each other.
type crossTeamCalloutView struct {
	Person     string                  `json:"person"`
	Teams      []string                `json:"teams"`
	WindowDays int                     `json:"window_days"`
	Deadlines  []crossTeamDeadlineView `json:"deadlines"`
}

// peopleWorkspaceView is the people-template workspace's control-room
// tiles.
type peopleWorkspaceView struct {
	ResourceBar       peopleResourceBarView  `json:"resource_bar"`
	Teams             []teamTileView         `json:"teams"`
	CrossTeamCallouts []crossTeamCalloutView `json:"cross_team_callouts,omitempty"`
	// Policies is twins/<id>/policies.md verbatim (Config.PoliciesMD, loaded
	// once at daemon startup): a static, read-only document, never a draft
	// and never edited from the workspace UI. Empty when the twin has no
	// policies.md of its own and no twins/ceo/policies.md fallback exists
	// either (loadPoliciesMD's own doc comment).
	Policies string `json:"policies,omitempty"`
}

func (d *Daemon) peopleWorkspaceView(ctx context.Context) *peopleWorkspaceView {
	now := d.computeNow()
	issues, issuesState := d.peopleIssues(ctx)
	view := &peopleWorkspaceView{
		ResourceBar:       d.peopleResourceBar(ctx, now),
		Teams:             d.peopleTeamTiles(ctx, issues, issuesState, now),
		CrossTeamCallouts: d.peopleCrossTeamCallouts(ctx, issues, now),
		Policies:          d.cfg.PoliciesMD,
	}
	if view.Teams == nil {
		view.Teams = []teamTileView{}
	}
	return view
}

// peopleIssues fetches every Linear issue the same cached, gated way
// projectTeamIssues does (dashboards.Issues, guarding the nil-Compute case
// itself since dashboards.Issues dereferences its *Compute argument).
func (d *Daemon) peopleIssues(ctx context.Context) ([]linear.Issue, dashboards.TileState) {
	if d.cfg.Compute == nil {
		return nil, dashboards.TileUnavailable
	}
	issues, state, _ := dashboards.Issues(ctx, d.cfg.Compute)
	return issues, state
}

// peopleBudgetLeftTile: see this section's own top-of-section doc comment
// for the judgment call (company-wide cash position, reused from the
// Finance dashboard's own "cash_position" metric).
func (d *Daemon) peopleBudgetLeftTile(ctx context.Context) metricTileView {
	if d.cfg.Dashboards == nil || d.cfg.Compute == nil {
		return metricTileView{State: dashboards.TileUnavailable}
	}
	spec, ok := d.cfg.Dashboards.Spec("finance")
	if !ok {
		return metricTileView{State: dashboards.TileUnavailable}
	}
	dv := d.cfg.Compute.Dashboard(ctx, spec)
	for _, m := range dv.Metrics {
		if m.ID == "cash_position" {
			return metricTileView{Value: m.Value, State: m.State}
		}
	}
	return metricTileView{State: dashboards.TileUnavailable}
}

// personAllocationFraction sums personID's own allocated_to fractions
// across every project (roster.HoursInvested/PersonProjectHours' own
// underlying data, read directly here since this needs the summed
// fraction, not one project's derived hours).
func (d *Daemon) personAllocationFraction(ctx context.Context, personID string) float64 {
	links, err := d.cfg.Store.LinksFrom(ctx, "person", personID, store.LinkAllocated)
	if err != nil {
		return 0
	}
	var total float64
	for _, l := range links {
		total += l.Fraction
	}
	return total
}

// personIdentityIndex maps every roster person's own "linear_owner_label"
// identity value to that person (internal/roster.PersonByIdentity's own
// key, read here as a one-pass index rather than a per-issue lookup since
// this file resolves many issues' assignees at once).
func (d *Daemon) personIdentityIndex(ctx context.Context) map[string]store.Person {
	out := map[string]store.Person{}
	if d.cfg.Store == nil {
		return out
	}
	people, err := store.List[store.Person](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil {
		return out
	}
	for _, p := range people {
		if v := p.Identity("linear_owner_label"); v != "" {
			out[v] = p
		}
	}
	return out
}

// teamsByLinearKey maps every roster Team's own LinearKey (upper-cased) to
// that Team, for resolving a Linear issue's Team field back to a roster
// team.
func (d *Daemon) teamsByLinearKey(ctx context.Context) map[string]store.Team {
	out := map[string]store.Team{}
	if d.cfg.Store == nil {
		return out
	}
	teams, err := store.List[store.Team](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil {
		return out
	}
	for _, t := range teams {
		if t.LinearKey != "" {
			out[strings.ToUpper(t.LinearKey)] = t
		}
	}
	return out
}

// peopleResourceBar computes budget left, hours this week and people with
// free capacity -- every number derived from roster allocations at request
// time, never stored (roster.HoursInvested's own "never stored" convention,
// this section's own top-of-section doc comment).
//
// hoursThisWeek sums fraction*peopleHoursPerWeek across every allocated_to
// link whose project has already started as of now (StartAt <= now,
// mirroring roster.HoursInvested's own "before the project's own start ...
// 0" rule) -- a person allocated to a project that hasn't started yet
// contributes no hours this week. There is no explicit "project already
// finished" cutoff (roster has no completion flag beyond TargetAt, which
// is a target date, not a recorded completion -- projectEarliestTarget's
// own doc comment makes the same point), so an allocation link past its
// project's TargetAt still counts; documented here as a known limitation
// rather than a guessed cutoff.
//
// peopleWithFreeCapacity counts roster people whose summed allocation
// fraction across every project is strictly less than 1.0 -- not fully
// committed to a 40h week's worth of project work.
func (d *Daemon) peopleResourceBar(ctx context.Context, now time.Time) peopleResourceBarView {
	view := peopleResourceBarView{
		BudgetLeftUSD:          d.peopleBudgetLeftTile(ctx),
		HoursThisWeek:          metricTileView{State: dashboards.TileUnavailable},
		PeopleWithFreeCapacity: metricTileView{State: dashboards.TileUnavailable},
	}
	if d.cfg.Store == nil {
		return view
	}
	people, err := store.List[store.Person](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil {
		return view
	}
	var hoursThisWeek float64
	var freeCapacityCount float64
	for _, p := range people {
		links, err := d.cfg.Store.LinksFrom(ctx, "person", p.SourceID, store.LinkAllocated)
		if err != nil {
			continue
		}
		var totalFraction float64
		for _, l := range links {
			totalFraction += l.Fraction
			proj, perr := store.Get[store.Project](ctx, d.cfg.Store, "seed", l.ToID)
			if perr != nil || proj.StartAt.IsZero() || now.Before(proj.StartAt) {
				continue
			}
			hoursThisWeek += l.Fraction * peopleHoursPerWeek
		}
		if totalFraction < 1.0 {
			freeCapacityCount++
		}
	}
	view.HoursThisWeek = metricTileView{Value: hoursThisWeek, State: dashboards.TileOK}
	view.PeopleWithFreeCapacity = metricTileView{Value: freeCapacityCount, State: dashboards.TileOK}
	return view
}

// peopleTeamTiles builds one tile per roster Team, sorted by name. issues/
// issuesState is the shared dashboards.Issues read (peopleIssues above);
// when it isn't ok, the roster-derived parts of each tile (members,
// allocation-only load) still render, but every Linear-derived number
// (open-issue counts, overdue) is 0 and the tile's own State says so, so
// the client never mistakes an unavailable Linear read for "no issues".
func (d *Daemon) peopleTeamTiles(ctx context.Context, issues []linear.Issue, issuesState dashboards.TileState, now time.Time) []teamTileView {
	if d.cfg.Store == nil {
		return nil
	}
	teams, err := store.List[store.Team](ctx, d.cfg.Store, store.Query{Source: "seed"})
	if err != nil {
		return nil
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i].Name < teams[j].Name })

	var out []teamTileView
	for _, team := range teams {
		out = append(out, d.peopleTeamTile(ctx, team, issues, issuesState, now))
	}
	return out
}

func (d *Daemon) peopleTeamTile(ctx context.Context, team store.Team, issues []linear.Issue, issuesState dashboards.TileState, now time.Time) teamTileView {
	memberLinks, _ := d.cfg.Store.LinksTo(ctx, "team", team.SourceID, store.LinkMemberOf)

	var overdue float64
	if issuesState == dashboards.TileOK {
		for _, is := range issues {
			if !strings.EqualFold(is.Team, team.LinearKey) || !linearIssueOpen(is.StateType) || is.DueDate == "" {
				continue
			}
			if due, perr := time.Parse("2006-01-02", is.DueDate); perr == nil && due.Before(now) {
				overdue++
			}
		}
	}

	var members []teamMemberView
	var loads []float64
	for _, link := range memberLinks {
		if link.FromType != "person" {
			continue
		}
		person, err := store.Get[store.Person](ctx, d.cfg.Store, "seed", link.FromID)
		if err != nil {
			continue
		}
		fraction := d.personAllocationFraction(ctx, person.SourceID)
		var openIssues, personOverdue int
		if issuesState == dashboards.TileOK {
			idValue := person.Identity("linear_owner_label")
			for _, is := range issues {
				if idValue == "" || is.Assignee != idValue || !linearIssueOpen(is.StateType) {
					continue
				}
				openIssues++
				if is.DueDate != "" {
					if due, perr := time.Parse("2006-01-02", is.DueDate); perr == nil && due.Before(now) {
						personOverdue++
					}
				}
			}
		}
		load := personLoad(fraction, openIssues)
		loads = append(loads, load)
		members = append(members, teamMemberView{
			Name: person.Name, Initials: Initials(person.Name),
			Load: load, LoadLevel: peopleStrain(load, personOverdue, nil),
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	teamLoad := averageFloat(loads)

	return teamTileView{
		ID: team.SourceID, Name: team.Name,
		Strain:  peopleStrain(teamLoad, int(overdue), nil),
		Load:    teamLoad,
		LoadMax: teamLoadMeterMax,
		Overdue: overdue,
		Members: members,
		State:   issuesState,
	}
}

// peopleCrossTeamCallouts fires when one roster person has open, due-dated
// Linear issues on more than one roster team within crossTeamWindowDays of
// each other (docs/slices/UI.md Phase 5b: "the same people are on
// deadlines ... across different teams within 7 days of each other").
//
// Each issue's own Team field (the Linear team key it actually lives under)
// resolves to a roster team here, not the assignee's home_team/
// also_on_teams -- a person can be assigned an issue on a team they aren't
// a roster member of at all, and the callout is about where the deadline
// itself lives, matching how peopleTeamTile's own overdue count is scoped
// by issue Team, not by roster membership.
func (d *Daemon) peopleCrossTeamCallouts(ctx context.Context, issues []linear.Issue, now time.Time) []crossTeamCalloutView {
	if d.cfg.Store == nil || len(issues) == 0 {
		return nil
	}
	identities := d.personIdentityIndex(ctx)
	teamsByKey := d.teamsByLinearKey(ctx)
	if len(identities) == 0 || len(teamsByKey) == 0 {
		return nil
	}

	type deadline struct {
		team    string
		issue   string
		dueDate time.Time
	}
	byPerson := map[string][]deadline{}
	for _, is := range issues {
		if !linearIssueOpen(is.StateType) || is.DueDate == "" || is.Assignee == "" {
			continue
		}
		person, ok := identities[is.Assignee]
		if !ok {
			continue
		}
		team, ok := teamsByKey[strings.ToUpper(is.Team)]
		if !ok {
			continue
		}
		due, perr := time.Parse("2006-01-02", is.DueDate)
		if perr != nil {
			continue
		}
		byPerson[person.SourceID] = append(byPerson[person.SourceID], deadline{team: team.Name, issue: is.Identifier, dueDate: due})
	}

	var out []crossTeamCalloutView
	for personID, deadlines := range byPerson {
		if len(deadlines) < 2 {
			continue
		}
		sort.Slice(deadlines, func(i, j int) bool { return deadlines[i].dueDate.Before(deadlines[j].dueDate) })

		seen := map[string]bool{}
		var fired []deadline
		for i := 0; i < len(deadlines); i++ {
			for j := i + 1; j < len(deadlines); j++ {
				if deadlines[j].dueDate.Sub(deadlines[i].dueDate) > crossTeamWindowDays*24*time.Hour {
					break
				}
				if deadlines[i].team == deadlines[j].team {
					continue
				}
				for _, dl := range []deadline{deadlines[i], deadlines[j]} {
					key := dl.team + "|" + dl.issue
					if !seen[key] {
						seen[key] = true
						fired = append(fired, dl)
					}
				}
			}
		}
		if len(fired) == 0 {
			continue
		}
		sort.Slice(fired, func(i, j int) bool { return fired[i].dueDate.Before(fired[j].dueDate) })

		person, err := store.Get[store.Person](ctx, d.cfg.Store, "seed", personID)
		name := personID
		if err == nil {
			name = person.Name
		}
		seenTeam := map[string]bool{}
		var teamNames []string
		var items []crossTeamDeadlineView
		for _, dl := range fired {
			if !seenTeam[dl.team] {
				seenTeam[dl.team] = true
				teamNames = append(teamNames, dl.team)
			}
			items = append(items, crossTeamDeadlineView{Team: dl.team, IssueIdentifier: dl.issue, DueDate: dl.dueDate.Format("2006-01-02")})
		}
		sort.Strings(teamNames)
		out = append(out, crossTeamCalloutView{Person: name, Teams: teamNames, WindowDays: crossTeamWindowDays, Deadlines: items})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Person < out[j].Person })
	return out
}

// ---- People workspace drafts: POST /v1/workspaces/{id}/drafts ----

// workspaceDraftRequest is POST /v1/workspaces/{id}/drafts' body
// (docs/slices/UI.md Phase 5b): kind picks which code-built template fills
// the new draft, team names the roster team it's about.
type workspaceDraftRequest struct {
	Kind string `json:"kind"`
	Team string `json:"team"`
}

// workspaceTeamMessageBody/workspacePulseCheckBody: docs/slices/UI.md Phase
// 5b's "Message a team"/"Send pulse check" buttons never call a model --
// both bodies are fixed, code-built text parameterized only by the team's
// own name, exactly like this file's own dashboard callout Detail phrases
// (financeWorstAppMarginCallout above) are code-built rather than
// model-phrased.
//
// To is left blank rather than guessed: every person in
// twins/ceo/seed/people.yaml has identities.email set to null today, so
// this file has no real address to put here. The CEO fills one in before
// "Send for approval", exactly like any other incomplete draft
// (store.SaveDraft's own doc comment: "a draft is allowed to be
// incomplete").
func workspaceTeamMessageBody(team store.Team) (subject, body string) {
	return "Update for " + team.Name, "Hi " + team.Name + " team,\n\n[Add your update here.]\n"
}

func workspacePulseCheckBody(team store.Team) (subject, body string) {
	return "Quick pulse check — " + team.Name,
		"Hi " + team.Name + " team — quick pulse check: on a scale of 1 (overloaded) to 5 (plenty of spare capacity), how loaded do you feel this week? Reply with a number, and anything else you want me to know."
}

// workspaceDraftForRequest resolves req into a store.Draft ready for
// CreateDraft: the closed kind -> store.Draft.Template mapping
// (docs/slices/UI.md Phase 5b: POST /v1/workspaces/{id}/drafts {kind:
// team_message|pulse_check, team}), matching store.draftTemplates' own
// closed enum (migration 0023 extends it with exactly these two values).
// An unknown kind, or a team id that doesn't resolve to a roster Team, is
// an error -- never silently accepted, never silently defaulted.
func workspaceDraftForRequest(ctx context.Context, st *store.Store, req workspaceDraftRequest) (store.Draft, error) {
	if st == nil {
		return store.Draft{}, fmt.Errorf("no store configured")
	}
	team, err := store.Get[store.Team](ctx, st, "seed", req.Team)
	if err != nil {
		return store.Draft{}, fmt.Errorf("no such team %q", req.Team)
	}
	var subject, body string
	switch req.Kind {
	case "team_message":
		subject, body = workspaceTeamMessageBody(*team)
	case "pulse_check":
		subject, body = workspacePulseCheckBody(*team)
	default:
		return store.Draft{}, fmt.Errorf("unknown draft kind %q", req.Kind)
	}
	return store.Draft{Template: req.Kind, Subject: subject, Body: body}, nil
}

// handleCreateWorkspaceDraft serves POST /v1/workspaces/{id}/drafts
// (docs/slices/UI.md Phase 5b): creates a drafts row via store.CreateDraft
// pre-filled with a code-built template body -- never a model call, and
// never an approval envelope or a decision (this file's own THE INVARIANT
// above). The workspace id in the path is validated the same way every
// other /v1/workspaces/{id} route validates it (a 404 for an unknown
// workspace) even though the draft itself is really about the team, not
// the workspace -- consistent with every other route in this file taking
// its workspace id from the path.
func (d *Daemon) handleCreateWorkspaceDraft(w http.ResponseWriter, r *http.Request) {
	if d.cfg.Workspaces == nil {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	if _, ok := d.cfg.Workspaces.Spec(r.PathValue("id")); !ok {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	var body workspaceDraftRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkspaceBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	draft, err := workspaceDraftForRequest(ctx, d.cfg.Store, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dr, err := d.cfg.Store.CreateDraft(ctx, draft)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, draftViewOf(dr))
}

// ---- Ideas / Research workspaces (docs/slices/UI.md Phase 5c) ----
//
// Unlike Project/Finance/Clients/People above, Ideas and Research are not
// scoped to this one workspace id at all: ideas.go's GET /v1/ideas and
// research_runs.go's GET /v1/research/runs are global lists (there is only
// ever one "ideas" workspace and one "research" workspace per twin, per
// twins/ceo/workspaces/ideas.yaml and research.yaml). ideasWorkspaceView/
// researchWorkspaceView below are pure reads over those same store
// accessors -- ideaViewOf/researchRunViewOf, defined in ideas.go/
// research_runs.go -- reused here rather than duplicated, so the workspace
// page and the two list endpoints always agree on shape. Nothing here
// writes anything, and nothing here calls Gate.Invoke: nothing this file
// needs to prepare requires a gated read (unlike the Project workspace's
// GitHub tiles), so THE INVARIANT at the top of this file holds trivially
// for both.

// ideasWorkspaceView is the ideas-template workspace's control-room
// content: every idea, split into the plan's own Raw/Explored groups by its
// own stage field (docs/slices/UI.md Phase 5c: "Raw and Explored groups").
type ideasWorkspaceView struct {
	Raw      []ideaView `json:"raw"`
	Explored []ideaView `json:"explored"`
}

func (d *Daemon) ideasWorkspaceView(ctx context.Context) *ideasWorkspaceView {
	view := &ideasWorkspaceView{Raw: []ideaView{}, Explored: []ideaView{}}
	if d.cfg.Store == nil {
		return view
	}
	ideas, err := d.cfg.Store.ListIdeas(ctx)
	if err != nil {
		return view
	}
	for _, idea := range ideas {
		iv := ideaViewOf(idea)
		if idea.Stage == "explored" {
			view.Explored = append(view.Explored, iv)
		} else {
			view.Raw = append(view.Raw, iv)
		}
	}
	return view
}

// researchWorkspaceView is the research-template workspace's control-room
// content: every research run, split into the plan's own Queued/Running/
// Finished columns by its own status field (docs/slices/UI.md Phase 5c:
// "Research columns: Queued, Running, Finished"). A "failed" run (a denial
// or a metered refusal stopped it, research_runner.go) is surfaced under
// Finished too, since there is no fourth column in the plan and a failed
// run is, like a finished one, no longer queued or running -- its own
// status field (still "failed", never silently rewritten to "finished")
// tells the client which it is.
type researchWorkspaceView struct {
	Queued   []researchRunView `json:"queued"`
	Running  []researchRunView `json:"running"`
	Finished []researchRunView `json:"finished"`
}

func (d *Daemon) researchWorkspaceView(ctx context.Context) *researchWorkspaceView {
	view := &researchWorkspaceView{Queued: []researchRunView{}, Running: []researchRunView{}, Finished: []researchRunView{}}
	if d.cfg.Store == nil {
		return view
	}
	runs, err := d.cfg.Store.ListResearchRuns(ctx)
	if err != nil {
		return view
	}
	for _, run := range runs {
		rv := researchRunViewOf(run)
		switch run.Status {
		case "queued":
			view.Queued = append(view.Queued, rv)
		case "running":
			view.Running = append(view.Running, rv)
		default: // finished or failed
			view.Finished = append(view.Finished, rv)
		}
	}
	return view
}
