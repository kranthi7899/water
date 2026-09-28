// view_workspaces.js: the Workspaces sidebar disclosure and its main-pane
// view (docs/slices/UI.md Phase 2's routing shape, Phase 5a's real
// project/finance/clients content, Phase 5b's real people content). A
// workspace's page is its filtered existing sections (needs-you rows,
// recent meetings and threads) plus its own control-room tiles from
// api.workspace(id) (internal/gateway/workspace_detail.go's WorkspaceView)
// -- every number here is read-only and this file never proposes, stages
// or decides anything by itself; "Message a team"/"Send pulse check" only
// ever create a drafts row (api.createWorkspaceDraft), never send anything,
// and everything else only opens a decision/approval/meeting/thread/draft
// the CEO already has, exactly like Today's own rows do. Phase 5c's Ideas
// and Research templates follow the same posture: "Start research" queues
// a run (api.startIdeaResearch, entirely server-side from there --
// research_runner.go), "Discuss" anchors a thread, "Propose" creates a
// drafts row, and Research's own Attach inserts only into
// card_evidence_extra server-side (internal/gateway/research_runs.go's own
// sanctioned exception) -- nothing here ever proposes an envelope or
// reaches a decision.
//
// Phase 5d's Marketing template follows the same posture: trend tiles name
// the research run they came from (illustrative with none), "Draft
// outreach" and "Draft reply" both only ever create a drafts row
// (api.createWorkspaceDraft, kinds prospect_outreach/review_reply -- the
// same route Phase 5b's buttons use, not a new one), and the public-reviews
// tile always reports not_connected: no real public-reviews data source
// exists anywhere in this codebase yet, so this file never renders a
// review row, only the tile's own honest state.
//
// Slice UI-polish (2026-09-28) restyles every template's markup to
// docs/design/design-reference.html's component vocabulary (.card, .metric,
// .list/.row, .pipeline/.mini) -- see docs/slices/UI-polish.md's Findings
// section. No server contract changed: every field this file reads below
// already existed in WorkspaceView before this pass. The Marketing
// analytics kit specifically ships layout-only, per that doc's own
// recommendation -- rating/feedback-score/cost-per-lead/themes/feedback-mix
// /content-pipeline have zero server-side computation behind them today
// (confirmed by reading internal/gateway/workspace_detail.go's marketing
// section in full before writing this), so every one of those tiles below
// renders its honest not_connected/empty state, never a fabricated number.
//
// The hash route is "workspaces" with an optional two-segment param,
// "<id>/<sub>" (e.g. "#workspaces/finance/overview", parsed by app.js's
// parseHash and built by goWorkspace). SUBS lists the sub-pages this phase
// knows about; DEFAULT_WORKSPACE_SUB (window.appShared, from app.js) is
// "overview", so a bare "#workspaces/finance" (or a click from the list/nav,
// which never itself sends a sub) lands on the same place a full "/overview"
// would.
'use strict';

(function () {
  const { h, replace, get, list, fmtDay, fmtDate } = window.dom;
  const api = window.api;

  // SUBS is every sub-page this phase's placeholder knows how to switch
  // between. There is exactly one today; a later phase (5) adds the real
  // ones (activity, people, ...) per template.
  const SUBS = [{ id: 'overview', label: 'Overview' }];

  function splitParam(param) {
    const s = String(param || '');
    const i = s.indexOf('/');
    return i < 0 ? { id: s, sub: '' } : { id: s.slice(0, i), sub: s.slice(i + 1) };
  }

  async function viewWorkspaces(c, param, gen) {
    const S = window.appShared;
    const items = list(await api.workspaces());
    if (!S.current(gen)) return;
    renderNavList(items);

    const { id, sub } = splitParam(param);
    if (!id) {
      replace(c, S.header('Workspaces', 'Grouped views over one project, client set or domain.'), workspacesList(items));
      return;
    }
    const spec = items.find((w) => get(w, 'id') === id);
    if (!spec) {
      replace(c, S.header('Workspaces', ''), S.errorBox('Could not open this workspace', new Error('No workspace named "' + id + '"')));
      return;
    }
    replace(c, S.header(get(spec, 'name') || id, workspaceSubtitle(spec)), h('p', { class: 'loading' }, 'Loading…'));
    await renderWorkspaceDetail(c, spec, sub || S.DEFAULT_WORKSPACE_SUB, gen);
  }

  // workspaceSubtitle: one short human line per template, never the old
  // internal "Template: ideas · Source: research" pair (docs/slices/
  // UI-polish.md's brief, "Workspaces" section). Keyed on the spec's own
  // template field -- real data, not invented per-workspace text.
  const TEMPLATE_SUBTITLES = {
    project: 'Progress, code activity and what needs a decision.',
    finance: 'Cash, spend and invoices.',
    clients: 'Accounts, health and outstanding work.',
    people: 'Team capacity, load and policies.',
    ideas: "Ideas you haven't turned into work yet.",
    research: "What's being researched, and what it found.",
    marketing: 'Public perception, prospects and content.',
  };
  function workspaceSubtitle(spec) {
    return TEMPLATE_SUBTITLES[get(spec, 'template')] || '';
  }

  function workspacesList(items) {
    const S = window.appShared;
    if (!items.length) return S.empty('No workspaces are configured for this twin.');
    const listEl = h('div', { class: 'list' });
    for (const w of items) {
      const id = get(w, 'id');
      listEl.appendChild(h('button', {
        type: 'button', class: 'row', on: { click: () => S.goWorkspace(id, S.DEFAULT_WORKSPACE_SUB) },
      },
      h('span', { class: 'body' },
        h('p', { class: 'title' }, get(w, 'name') || id),
        h('p', { class: 'meta' }, TEMPLATE_SUBTITLES[get(w, 'template')] || get(w, 'source') || ''))));
    }
    return listEl;
  }

  // ---- tile state badges ----
  //
  // Two badge functions, not one: a Finance/Clients tile reused straight
  // from internal/dashboards.DashboardView reads "Not connected"
  // (mirroring view_dashboards.js's own stateBadge exactly, for the same
  // tile shape), but a GitHub-sourced tile's not_connected state is this
  // phase's own "Connect GitHub" wording (docs/slices/UI.md Phase 5a) --
  // the same tile state, a different, more actionable label for a
  // different underlying cause.
  function dashboardStateBadge(S, tileState) {
    switch (tileState) {
      case 'ok': return null;
      case 'not_connected': return S.badge('Not connected', 'warn');
      case 'unavailable': return S.badge('Unavailable', 'status-denied');
      case 'illustrative': return S.badge('Illustrative', 'warn');
      default: return S.badge(String(tileState || ''), '');
    }
  }

  function githubStateBadge(S, tileState) {
    switch (tileState) {
      case 'ok': return null;
      case 'not_connected': return S.badge('Connect GitHub', 'warn');
      case 'unavailable': return S.badge('Unavailable', 'status-denied');
      default: return S.badge(String(tileState || ''), '');
    }
  }

  // priorityEdgeClass/priorityBadge translate a needs-you row's own
  // priorityClass ('p-urgent'/'p-high'/'p-normal', app.js) into the design
  // reference's dense-list vocabulary (.row.edge-danger/.edge-warn/
  // .edge-none, already in app.css) -- same underlying signal, the
  // reference's row shape instead of the standalone bordered-box one.
  function priorityEdgeClass(pClass) {
    if (pClass === 'p-urgent') return 'edge-danger';
    if (pClass === 'p-high') return 'edge-warn';
    return 'edge-none';
  }
  function priorityBadge(S, pClass) {
    if (pClass === 'p-urgent') return S.badge('Urgent', 'danger');
    if (pClass === 'p-high') return S.badge('High', 'warn');
    return null;
  }

  function fmtNumber(n) {
    if (typeof n !== 'number' || !isFinite(n)) return '—';
    const r = Math.round(n * 100) / 100;
    return String(r);
  }

  // metricBox: docs/design/design-reference.html's .metric tile (icon-less
  // label, one large value, a state badge in place of a caption when the
  // tile isn't real) -- replaces the older .panel/.dashboard-tile pairing
  // this file used before this pass. Always place the result inside a
  // ".metrics" grid, never standalone.
  function metricBox(label, tile, state, badgeFn) {
    return h('div', { class: 'metric' },
      h('p', { class: 'k' }, label),
      h('p', { class: 'v' }, state === 'ok' ? fmtNumber(get(tile, 'value')) : '—'),
      badgeFn(window.appShared, state));
  }

  // ---- project workspace ----

  // progressCard: the reference's project header card -- a progress bar and
  // "N% complete · Target <date>" line, in a .card (not .panel, which this
  // file reserves for smaller nested boxes below).
  function progressCard(tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    if (state !== 'ok') {
      return h('div', { class: 'card' }, h('p', { class: 'label' }, 'Progress'), dashboardStateBadge(S, state) || S.empty('No progress data yet.'));
    }
    const done = get(tile, 'done') || 0;
    const total = get(tile, 'total') || 0;
    const target = get(tile, 'target_at');
    const pct = total > 0 ? Math.round((done / total) * 100) : 0;
    return h('div', { class: 'card' },
      h('progress', { value: done, max: Math.max(total, 1) }),
      h('div', { class: 'meta' },
        h('span', null, total > 0 ? (pct + '% complete (' + done + ' of ' + total + ' issues)') : 'No issues on this team yet.'),
        h('span', null, target ? ' · Target ' + fmtDay(target) : ' · No target date on record')));
  }

  function buildingNowPanel(tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    const items = list(get(tile, 'items'));
    if (state !== 'ok' || !items.length) {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Building now'), dashboardStateBadge(S, state) || S.empty('Nothing in progress.'));
    }
    const ul = h('ul', { class: 'rows' });
    for (const it of items) {
      ul.appendChild(h('li', null, h('div', { class: 'row row-static' },
        h('span', { class: 'row-main' },
          h('span', { class: 'row-title' }, get(it, 'identifier') + ' ' + get(it, 'title'))))));
    }
    return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Building now'), ul);
  }

  function commitBarsPanel(tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    if (state !== 'ok') {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Commits, last 4 weeks'), githubStateBadge(S, state) || S.empty('No commit data.'));
    }
    const weeks = list(get(tile, 'weeks'));
    const max = Math.max(1, ...weeks.map((n) => Number(n) || 0));
    const bars = h('div', { class: 'commit-bars' });
    weeks.forEach((n, i) => {
      bars.appendChild(h('div', { class: 'commit-bar' },
        h('meter', { min: 0, max: max, value: Number(n) || 0, low: 0, high: max, optimum: max }),
        h('span', { class: 'muted small' }, String(n))));
    });
    return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Commits, last 4 weeks (oldest first)'), bars);
  }

  function projectTiles(view) {
    const p = get(view, 'project');
    if (!p) return null;
    const openPRsState = get(get(p, 'open_prs'), 'state');
    const mergedState = get(get(p, 'merged_this_week'), 'state');
    const blockedState = get(get(p, 'blocked_issues'), 'state');
    return h('div', { class: 'stack' },
      progressCard(get(p, 'progress')),
      h('div', { class: 'metrics' },
        metricBox('Open PRs', get(p, 'open_prs'), openPRsState, githubStateBadge),
        metricBox('Merged this week', get(p, 'merged_this_week'), mergedState, githubStateBadge),
        metricBox('Blocked issues', get(p, 'blocked_issues'), blockedState, dashboardStateBadge)),
      buildingNowPanel(get(p, 'building_now')),
      commitBarsPanel(get(p, 'commit_bars')));
  }

  // ---- finance / clients workspace (reuse the Dashboard tile shape) ----

  function dashboardMetricsRow(dashboardView) {
    const metrics = list(get(dashboardView, 'metrics'));
    if (!metrics.length) return null;
    return h('div', { class: 'metrics' }, metrics.map((m) =>
      metricBox(metricLabel(get(m, 'id')), m, get(m, 'state'), dashboardStateBadge)));
  }

  function metricLabel(id) {
    const words = String(id || '').split('_');
    return words.map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join(' ');
  }

  function calloutPanel(dashboardView) {
    const S = window.appShared;
    const c = get(dashboardView, 'callout');
    const state = get(c, 'state');
    if (state !== 'ok') {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Callout'), dashboardStateBadge(S, state) || S.empty('No callout yet.'));
    }
    return h('div', { class: 'panel' },
      h('div', { class: 'row-meta' }, 'Callout'),
      h('div', { class: 'row-title' }, get(c, 'title') || ''),
      h('p', null, get(c, 'detail') || ''));
  }

  function invoicesPanel(tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    const items = list(get(tile, 'items'));
    if (state !== 'ok' || !items.length) {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Invoices'), dashboardStateBadge(S, state) || S.empty('No outstanding invoices.'));
    }
    const ul = h('ul', { class: 'rows' });
    for (const it of items) {
      const aging = get(it, 'aging_days');
      ul.appendChild(h('li', null, h('div', { class: 'row row-static' },
        h('span', { class: 'row-main' },
          h('span', { class: 'row-title' }, get(it, 'account')),
          h('span', { class: 'row-meta' },
            h('span', { class: 'muted' }, '$' + fmtNumber(get(it, 'amount_usd'))),
            h('span', { class: 'muted' }, get(it, 'status') || ''),
            typeof aging === 'number' ? h('span', { class: 'muted' }, Math.round(aging) + 'd outstanding') : null)))));
    }
    return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Invoices'), ul);
  }

  function vendorsPanel(tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    const items = list(get(tile, 'items'));
    if (state !== 'ok' || !items.length) {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Vendor renewals'), dashboardStateBadge(S, state) || S.empty('No vendors on record.'));
    }
    const ul = h('ul', { class: 'rows' });
    for (const v of items) {
      const renewal = get(v, 'renewal_at');
      ul.appendChild(h('li', null, h('div', { class: 'row row-static' },
        h('span', { class: 'row-main' },
          h('span', { class: 'row-title' }, get(v, 'name')),
          h('span', { class: 'row-meta' },
            h('span', { class: 'muted' }, '$' + fmtNumber(get(v, 'monthly_usd')) + '/mo'),
            renewal ? h('span', { class: 'muted' }, 'Renews ' + fmtDay(renewal)) : null)))));
    }
    return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Vendor renewals'), ul);
  }

  function financeTiles(view) {
    const f = get(view, 'finance');
    if (!f) return null;
    const dv = get(f, 'dashboard');
    return h('div', { class: 'stack' },
      dashboardMetricsRow(dv),
      calloutPanel(dv),
      invoicesPanel(get(f, 'invoices')),
      vendorsPanel(get(f, 'vendors')));
  }

  function accountsPanel(tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    const items = list(get(tile, 'items'));
    if (state !== 'ok' || !items.length) {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Accounts'), dashboardStateBadge(S, state) || S.empty('No accounts on record.'));
    }
    const ul = h('ul', { class: 'rows' });
    for (const a of items) {
      const days = get(a, 'days_since_contact');
      ul.appendChild(h('li', null, h('div', { class: 'row row-static' },
        h('span', { class: 'row-main' },
          h('span', { class: 'row-title' }, get(a, 'name')),
          h('span', { class: 'row-meta' },
            get(a, 'health') ? S.badge(get(a, 'health')) : null,
            h('span', { class: 'muted' }, fmtNumber(get(a, 'open_tickets')) + ' open tickets'),
            typeof days === 'number' ? h('span', { class: 'muted' }, Math.round(days) + 'd since contact') : null,
            get(a, 'outstanding_usd') ? h('span', { class: 'muted' }, '$' + fmtNumber(get(a, 'outstanding_usd')) + ' outstanding') : null,
            get(a, 'owner_name') ? h('span', { class: 'muted' }, 'Owner: ' + get(a, 'owner_name')) : null)))));
    }
    return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Accounts'), ul);
  }

  function clientsTiles(view) {
    const cl = get(view, 'clients');
    if (!cl) return null;
    const dv = get(cl, 'dashboard');
    return h('div', { class: 'stack' },
      dashboardMetricsRow(dv),
      calloutPanel(dv),
      accountsPanel(get(cl, 'accounts')));
  }

  // ---- people workspace (docs/slices/UI.md Phase 5b) ----
  //
  // A resource bar, one tile per roster team (a strain border, member
  // initials coloured by individual load, a load bar, an overdue count),
  // a cross-team callout, and three buttons. "Message a team"/"Send pulse
  // check" call api.createWorkspaceDraft, which only ever creates a drafts
  // row (internal/gateway/workspace_detail.go's handleCreateWorkspaceDraft)
  // -- nothing here sends anything or proposes an envelope. "Policies"
  // toggles an already-fetched, read-only <pre> block; no second network
  // call, no new endpoint, nothing editable.

  function strainClass(level) {
    const l = String(level || 'normal');
    return 'strain-' + (l === 'low' || l === 'high' ? l : 'normal');
  }

  function loadClass(level) {
    const l = String(level || 'normal');
    return 'load-' + (l === 'low' || l === 'high' ? l : 'normal');
  }

  function peopleResourceBarPanel(bar) {
    return h('div', { class: 'metrics' },
      metricBox('Budget left', get(bar, 'budget_left_usd'), get(get(bar, 'budget_left_usd'), 'state'), dashboardStateBadge),
      metricBox('Hours this week', get(bar, 'hours_this_week'), get(get(bar, 'hours_this_week'), 'state'), dashboardStateBadge),
      metricBox('People with free capacity', get(bar, 'people_with_free_capacity'), get(get(bar, 'people_with_free_capacity'), 'state'), dashboardStateBadge));
  }

  // createTeamDraft is "Message a team"/"Send pulse check": it only ever
  // creates a drafts row and opens it in the Drafts editor for the CEO to
  // fill in and send -- exactly like every other draft-creating control in
  // this codebase (Phase 3c).
  async function createTeamDraft(workspaceID, teamID, kind, label) {
    const S = window.appShared;
    try {
      const draft = await api.createWorkspaceDraft(workspaceID, kind, { team: teamID });
      S.toast(label + ' drafted.');
      S.go('drafts', get(draft, 'id'));
    } catch (err) {
      S.toast('Could not create the draft: ' + S.errText(err), 'bad');
    }
  }

  function teamTilePanel(workspaceID, team) {
    const S = window.appShared;
    const members = list(get(team, 'members'));
    const memberRow = h('div', { class: 'team-members' }, members.map((m) =>
      h('span', {
        class: 'avatar ' + loadClass(get(m, 'load_level')),
        title: get(m, 'name') + ' · load ' + fmtNumber(get(m, 'load')),
      }, get(m, 'initials'))));
    const load = get(team, 'load') || 0;
    const loadMax = get(team, 'load_max') || 2;
    const teamID = get(team, 'id');
    const state = get(team, 'state');
    return h('div', { class: 'panel team-tile ' + strainClass(get(team, 'strain')) },
      h('div', { class: 'row-title' }, get(team, 'name')),
      memberRow,
      h('meter', { min: 0, max: loadMax, value: Math.min(load, loadMax), low: loadMax * 0.35, high: loadMax * 0.6, optimum: 0 }),
      h('div', { class: 'row-meta' },
        h('span', { class: 'muted' }, fmtNumber(get(team, 'overdue')) + ' overdue'),
        state !== 'ok' ? dashboardStateBadge(S, state) : null),
      h('div', { class: 'head-actions' },
        S.button('Message a team', () => createTeamDraft(workspaceID, teamID, 'team_message', 'Team message'), 'secondary'),
        S.button('Send pulse check', () => createTeamDraft(workspaceID, teamID, 'pulse_check', 'Pulse check'), 'secondary')));
  }

  function crossTeamCalloutsPanel(items) {
    if (!items.length) return null;
    const panel = h('section', { class: 'panel' }, h('h2', null, 'Cross-team deadlines'));
    const ul = h('ul', { class: 'rows' });
    for (const c of items) {
      const deadlines = list(get(c, 'deadlines'));
      ul.appendChild(h('li', null, h('div', { class: 'row row-static' },
        h('span', { class: 'row-main' },
          h('span', { class: 'row-title' }, get(c, 'person') + ' — ' + list(get(c, 'teams')).join(', ')),
          h('span', { class: 'row-meta' }, deadlines.map((dl) =>
            h('span', { class: 'muted' },
              get(dl, 'issue_identifier') + ' (' + get(dl, 'team') + ') ' + fmtDay(get(dl, 'due_date')))))))));
    }
    panel.appendChild(ul);
    return panel;
  }

  function peopleTiles(view, workspaceID) {
    const S = window.appShared;
    const p = get(view, 'people');
    if (!p) return null;
    const teams = list(get(p, 'teams'));
    const callouts = list(get(p, 'cross_team_callouts'));
    const policiesPanel = h('section', { class: 'panel', hidden: true },
      h('h2', null, 'Policies'),
      h('pre', { class: 'policies-text' }, get(p, 'policies') || 'No policies document is configured for this twin.'));
    const policiesButton = S.button('Policies', () => { policiesPanel.hidden = !policiesPanel.hidden; }, 'secondary');
    return h('div', { class: 'stack' },
      peopleResourceBarPanel(get(p, 'resource_bar')),
      teams.length
        ? h('div', { class: 'dashboard-tiles' }, teams.map((team) => teamTilePanel(workspaceID, team)))
        : S.empty('No roster teams on record.'),
      crossTeamCalloutsPanel(callouts),
      h('div', { class: 'head-actions' }, policiesButton),
      policiesPanel);
  }

  // ---- ideas workspace (docs/slices/UI.md Phase 5c) ----
  //
  // A single-row capture bar (a POST body, never a query string --
  // api.createIdea) and Raw/Explored grouped .list panels
  // (docs/slices/UI-polish.md brief). "Start research" queues a run
  // (api.startIdeaResearch, entirely server-side from here on --
  // research_runner.go), "Discuss" anchors a thread of type "idea"
  // (api.anchorThread, Phase 3d's existing endpoint, reused as-is) and
  // opens it, "Propose" creates a drafts row (api.proposeIdea) and opens it
  // in the Drafts editor. None of these three ever proposes an envelope or
  // reaches a decision by itself.

  async function discussIdea(ideaID) {
    const S = window.appShared;
    try {
      const out = await api.anchorThread('idea', ideaID);
      S.go('threads', get(get(out, 'thread'), 'id'));
    } catch (err) {
      S.toast('Could not open a thread: ' + S.errText(err), 'bad');
    }
  }

  async function proposeIdeaDraft(ideaID) {
    const S = window.appShared;
    try {
      const draft = await api.proposeIdea(ideaID);
      S.toast('Proposal drafted.');
      S.go('drafts', get(draft, 'id'));
    } catch (err) {
      S.toast('Could not create the draft: ' + S.errText(err), 'bad');
    }
  }

  // ideaRow: one dense .list row (icon-less -- ideas have no fixed kind
  // glyph) with title/gist and its three actions.
  function ideaRow(idea, onStartResearch) {
    const S = window.appShared;
    const id = get(idea, 'id');
    return h('div', { class: 'row' },
      h('span', { class: 'body' },
        h('p', { class: 'title' }, get(idea, 'title')),
        get(idea, 'gist') ? h('p', { class: 'meta' }, get(idea, 'gist')) : null),
      h('div', { class: 'head-actions' },
        S.button('Start research', () => onStartResearch(id), 'secondary'),
        S.button('Discuss', () => discussIdea(id), 'secondary'),
        S.button('Propose', () => proposeIdeaDraft(id), 'secondary')));
  }

  function ideaGroupPanel(title, items, emptyText, onStartResearch) {
    const S = window.appShared;
    const heading = h('p', { class: 'label' }, title);
    if (!items.length) return h('div', null, heading, S.empty(emptyText));
    const listEl = h('div', { class: 'list' });
    for (const idea of items) listEl.appendChild(ideaRow(idea, onStartResearch));
    return h('div', null, heading, listEl);
  }

  // ideaCaptureBar posts the new idea as a JSON body (api.createIdea),
  // never a query string, then calls onCreated to refresh the page. A
  // single-row .card per the reference: an icon, the two inputs and Add,
  // no section header.
  function ideaCaptureBar(onCreated) {
    const S = window.appShared;
    const title = h('input', { type: 'text', placeholder: 'Jot an idea', maxlength: 200, autocomplete: 'off' });
    const gist = h('input', { type: 'text', placeholder: 'One-line gist (optional)', autocomplete: 'off' });
    const msg = h('div');
    const add = S.button('Add', async () => {
      const t = title.value.trim();
      if (!t) { replace(msg, S.errorBox('Title is required', new Error('empty title'))); return; }
      add.disabled = true;
      try {
        await api.createIdea(t, gist.value.trim());
        title.value = '';
        gist.value = '';
        replace(msg, null);
        onCreated();
      } catch (err) {
        replace(msg, S.errorBox('Could not add this idea', err));
      } finally {
        add.disabled = false;
      }
    }, 'primary');
    return h('div', { class: 'card' },
      h('div', { class: 'form inline' }, S.icon('bulb'), title, gist, add),
      msg);
  }

  function ideasTiles(view, reload) {
    const idv = get(view, 'ideas');
    if (!idv) return null;
    const onStartResearch = async (ideaID) => {
      const S = window.appShared;
      try {
        await api.startIdeaResearch(ideaID);
        S.toast('Research queued.');
        reload();
      } catch (err) {
        S.toast('Could not start research: ' + S.errText(err), 'bad');
      }
    };
    return h('div', { class: 'stack' },
      ideaCaptureBar(reload),
      ideaGroupPanel('Raw', list(get(idv, 'raw')), 'No raw ideas yet. Jot one above.', onStartResearch),
      ideaGroupPanel('Explored', list(get(idv, 'explored')), 'Nothing explored yet. Start research on a raw idea.', onStartResearch));
  }

  // ---- research workspace (docs/slices/UI.md Phase 5c) ----
  //
  // Three side-by-side .pipeline columns (Queued/Running/Finished), each of
  // .mini cards. Each card expands in place (no new page/route) into its
  // own steps ("N of 5", research_steps) and, once finished, either
  // "In <card>" (attached_card_id) or an Attach control that posts a card
  // id (api.attachResearchRun) -- inserting only into card_evidence_extra
  // server-side (internal/gateway/research_runs.go's own sanctioned
  // exception), never anything this file has to know about. A Running
  // card's step count is fetched once, eagerly, straight from the existing
  // GET research run endpoint (already used for the click-to-
  // expand detail below) so the reference's inline "3 of 5 steps" bar shows
  // without waiting for a click -- no new server read, just an earlier call
  // to the one that already exists.

  function researchStatusBadge(S, status) {
    switch (status) {
      case 'queued': return S.badge('Queued', '');
      case 'running': return S.badge('Running', 'warn');
      case 'finished': return S.badge('Finished', 'ok');
      case 'failed': return S.badge('Failed', 'status-denied');
      default: return S.badge(String(status || ''), '');
    }
  }

  function researchStepsList(steps) {
    const S = window.appShared;
    const ul = h('ul', { class: 'rows' });
    for (const s of steps) {
      ul.appendChild(h('li', null, h('div', { class: 'row row-static' },
        h('span', { class: 'row-main' },
          h('span', { class: 'row-title' }, get(s, 'label')),
          h('span', { class: 'row-meta' }, researchStatusBadge(S, get(s, 'status')))))));
    }
    return ul;
  }

  // researchAttachForm is the Attach control: a card id input plus a
  // button, refresh is called (re-fetching this same run) once the attach
  // succeeds so the row immediately switches to "In <card>".
  function researchAttachForm(runID, refresh) {
    const S = window.appShared;
    const cardID = h('input', { type: 'text', placeholder: 'Decision card id', autocomplete: 'off' });
    const msg = h('div');
    const attach = S.button('Attach', async () => {
      const id = cardID.value.trim();
      if (!id) { replace(msg, S.errorBox('Card id is required', new Error('empty card id'))); return; }
      attach.disabled = true;
      try {
        await api.attachResearchRun(runID, id);
        S.toast('Attached.');
        await refresh();
      } catch (err) {
        replace(msg, S.errorBox('Could not attach', err));
      } finally {
        attach.disabled = false;
      }
    }, 'secondary');
    return h('div', null, h('div', { class: 'form inline' }, cardID, attach), msg);
  }

  function researchRunDetailPanel(run, refresh) {
    const steps = list(get(run, 'steps'));
    const done = steps.filter((s) => get(s, 'status') === 'done').length;
    const attachedCardID = get(run, 'attached_card_id');
    let attachArea = null;
    if (attachedCardID) {
      attachArea = h('p', { class: 'muted small' }, 'In ' + attachedCardID);
    } else if (get(run, 'status') === 'finished') {
      attachArea = researchAttachForm(get(run, 'id'), refresh);
    }
    return h('div', { class: 'stack' },
      h('p', { class: 'muted small' }, done + ' of ' + steps.length + ' steps'),
      researchStepsList(steps),
      get(run, 'report_text') ? h('pre', { class: 'policies-text' }, get(run, 'report_text')) : null,
      attachArea);
  }

  // runningStepsMeta: the inline progress bar + "N of 5 steps" line the
  // reference shows directly on a Running card, fetched once eagerly (see
  // the section comment above) rather than only on click.
  function runningStepsMeta(runID) {
    const S = window.appShared;
    const area = h('p', { class: 'meta' }, 'Loading steps…');
    (async () => {
      try {
        const run = await api.researchRun(runID);
        const steps = list(get(run, 'steps'));
        const done = steps.filter((s) => get(s, 'status') === 'done').length;
        const total = steps.length;
        if (!total) { replace(area, 'No steps recorded yet.'); return; }
        replace(area, h('progress', { value: done, max: total }), ' ' + done + ' of ' + total + ' steps');
      } catch (err) {
        replace(area, S.errText(err));
      }
    })();
    return area;
  }

  // researchRunRow: a .mini card (styled by combining .row's button reset
  // with .mini's box, both single-class rules so .mini's later-declared
  // border/padding/background win over .row's near-identical values --
  // see docs/slices/UI-polish.md's own note on why no new CSS class was
  // needed here) that toggles a detail area in place.
  function researchRunRow(runSummary) {
    const S = window.appShared;
    const id = get(runSummary, 'id');
    const status = get(runSummary, 'status');
    const attachedCardID = get(runSummary, 'attached_card_id');
    const detail = h('div', { class: 'run-detail', hidden: true });
    let loaded = false;
    async function load() {
      try {
        const run = await api.researchRun(id);
        replace(detail, researchRunDetailPanel(run, load));
      } catch (err) {
        replace(detail, S.errorBox('Could not load this run', err));
      }
    }
    let metaLine;
    if (status === 'running') {
      metaLine = runningStepsMeta(id);
    } else if (status === 'finished') {
      metaLine = h('p', { class: 'meta' }, attachedCardID ? ('In ' + attachedCardID) : 'Not linked to a decision yet.');
    } else {
      metaLine = h('p', { class: 'meta' }, researchStatusBadge(S, status));
    }
    const toggle = h('button', {
      type: 'button', class: 'row mini',
      on: {
        click: async () => {
          detail.hidden = !detail.hidden;
          if (!detail.hidden && !loaded) {
            loaded = true;
            await load();
          }
        },
      },
    },
    h('p', { class: 'title' }, get(runSummary, 'topic')),
    metaLine);
    return h('div', null, toggle, detail);
  }

  const RESEARCH_EMPTY = {
    Queued: 'Nothing queued right now.',
    Running: 'Nothing running right now.',
    Finished: 'Nothing finished yet.',
  };

  function researchColumn(title, items) {
    const S = window.appShared;
    const heading = h('p', { class: 'label' }, title + ' · ' + items.length);
    if (!items.length) return h('div', null, heading, S.empty(RESEARCH_EMPTY[title] || 'Nothing here.'));
    const col = h('div', null, heading);
    for (const r of items) col.appendChild(researchRunRow(r));
    return col;
  }

  function researchTiles(view) {
    const rv = get(view, 'research');
    if (!rv) return null;
    return h('div', { class: 'pipeline' },
      researchColumn('Queued', list(get(rv, 'queued'))),
      researchColumn('Running', list(get(rv, 'running'))),
      researchColumn('Finished', list(get(rv, 'finished'))));
  }

  // ---- marketing workspace (docs/slices/UI.md Phase 5d, restyled per
  // docs/slices/UI-polish.md's Analytics kit section) ----
  //
  // Layout only, exactly as that brief's own Findings/Risks sections
  // conclude: internal/gateway/workspace_detail.go's marketingWorkspaceView
  // (read in full before writing this) carries exactly three real tiles --
  // TrendTiles, Prospects, PublicReviews -- plus an optional CapacityNote.
  // There is no rating, feedback-score, cost-per-lead, theme-count,
  // feedback-mix or content-pipeline field anywhere on that struct, and no
  // compute for any of them exists in this codebase. So: the four stat
  // tiles, the themes bar, the feedback-mix bar and the content-pipeline
  // stepper below all render their honest not_connected/empty state, using
  // this app's existing dashboards.TileState convention -- never a made-up
  // number. Only "Open prospects" (from the real Prospects tile) and the
  // trend tiles (real, already existed) show a real value. The market-
  // interest line chart is skipped entirely: internal/webui/static/vendor/
  // chart.umd.js does not exist in this checkout (the Foundation stage's
  // vendoring did not land it), so per this slice's own instructions this
  // renders the "Illustrative" badge with a text placeholder instead of a
  // canvas -- flagged again in this slice's report for the owner.

  function marketingStatTile(S, iconName, label, state, valueText, metaText) {
    return h('div', { class: 'metric' },
      h('p', { class: 'k' }, S.icon(iconName), ' ' + label),
      h('p', { class: 'v' }, state === 'ok' ? valueText : '—'),
      dashboardStateBadge(S, state),
      h('p', { class: 'meta' }, metaText));
  }

  function marketingStatTiles(prospectsTile) {
    const S = window.appShared;
    const pState = get(prospectsTile, 'state');
    const pItems = list(get(prospectsTile, 'items'));
    // .metrics is a 3-column grid in app.css (not this file's to widen to
    // 4 -- that CSS lives in app.css, out of this slice's scope); the
    // fourth tile wraps to its own row, which still reads fine dense.
    return h('div', { class: 'metrics' },
      marketingStatTile(S, 'star', 'Public rating', 'not_connected', '—', 'No public-reviews source connected yet.'),
      marketingStatTile(S, 'mood-smile', 'Feedback score', 'not_connected', '—', 'No feedback responses recorded yet.'),
      marketingStatTile(S, 'target-arrow', 'Open prospects', pState, String(pItems.length), pState === 'ok' ? (pItems.length + ' from the accounts sheet') : 'No prospect data on record.'),
      marketingStatTile(S, 'coin', 'Cost per lead', 'not_connected', '—', 'No brand budget or lead count wired up yet.'));
  }

  function trendTilePanel(tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    const value = get(tile, 'value');
    const unit = get(tile, 'unit') || '';
    const valueText = (state === 'ok' && typeof value === 'number') ? (fmtNumber(value) + unit) : '—';
    const sourceLine = (state === 'ok' && get(tile, 'run_topic'))
      ? h('span', { class: 'muted small' }, 'From: ' + get(tile, 'run_topic'))
      : h('span', { class: 'muted small' }, 'No research run linked yet.');
    return h('div', { class: 'panel dashboard-tile' },
      h('div', { class: 'row-meta' }, get(tile, 'label')),
      h('div', { class: 'dashboard-tile-value' }, valueText),
      dashboardStateBadge(window.appShared, state),
      sourceLine);
  }

  // marketInterestCard: the reference's "Market interest, 12 weeks" line
  // chart, minus the chart itself (see the section comment above -- no
  // vendored Chart.js in this checkout). Shows the "Illustrative" badge the
  // brief requires plus the real trend tiles this app already had, so the
  // card isn't empty even without a canvas.
  function marketInterestCard(trendTiles) {
    const S = window.appShared;
    const head = h('div', { class: 'row-meta' },
      h('span', { class: 'title' }, 'Market interest'),
      S.badge('Illustrative', 'warn'));
    if (!trendTiles.length) {
      return h('div', { class: 'card' }, head, S.empty('No research run has supplied a market-interest series yet.'));
    }
    return h('div', { class: 'card' }, head,
      h('p', { class: 'meta' }, 'No chart library is vendored offline yet, so each topic\'s current tile is shown instead of a chart.'),
      h('div', { class: 'metrics' }, trendTiles.map(trendTilePanel)));
  }

  function themesCard() {
    const S = window.appShared;
    return h('div', { class: 'card' },
      h('p', { class: 'title' }, 'What people mention'),
      h('p', { class: 'meta' }, 'Reviews, tickets, feedback'),
      S.empty('Nothing is tagged yet, so there is nothing to rank.'));
  }

  function feedbackMixCard() {
    const S = window.appShared;
    return h('div', { class: 'card' },
      h('p', { class: 'title' }, 'Feedback mix'),
      S.empty('No feedback responses recorded yet.'));
  }

  function contentPipelineCard() {
    const S = window.appShared;
    return h('div', { class: 'card' },
      h('p', { class: 'title' }, 'Content pipeline'),
      S.empty('Nothing is in the content pipeline yet.'));
  }

  // insightCard may only cite numbers the other tiles on this page show
  // (docs/slices/UI-polish.md's own data rule) -- today that is only the
  // real Prospects tile, so this either cites that count by name or, when
  // there's nothing real to cite, says so plainly rather than inventing a
  // generic-sounding insight.
  function insightCard(prospectsTile) {
    const S = window.appShared;
    const state = get(prospectsTile, 'state');
    const items = list(get(prospectsTile, 'items'));
    if (state !== 'ok' || !items.length) {
      return h('div', { class: 'card' }, S.icon('bulb'), h('span', { class: 'meta' }, ' Not enough data yet for an insight.'));
    }
    const names = items.map((p) => get(p, 'name')).join(', ');
    return h('div', { class: 'card' },
      h('div', { class: 'row-line' },
        S.icon('bulb'),
        h('p', null, items.length + ' open prospect' + (items.length === 1 ? '' : 's') + ': ' + names + '.')));
  }

  // createProspectOutreachDraft is "Draft outreach": it only ever creates a
  // drafts row from the prospect's own real name/data (never a model call)
  // and opens it in the Drafts editor, exactly like createTeamDraft above.
  async function createProspectOutreachDraft(workspaceID, prospectName) {
    const S = window.appShared;
    try {
      const draft = await api.createWorkspaceDraft(workspaceID, 'prospect_outreach', { prospect_id: prospectName });
      S.toast('Outreach drafted.');
      S.go('drafts', get(draft, 'id'));
    } catch (err) {
      S.toast('Could not create the draft: ' + S.errText(err), 'bad');
    }
  }

  function prospectsPanel(workspaceID, tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    const items = list(get(tile, 'items'));
    const head = h('p', { class: 'label' }, 'Prospects');
    if (state !== 'ok' || !items.length) {
      return h('div', null, head, dashboardStateBadge(S, state) || S.empty('No prospects on record.'));
    }
    const listEl = h('div', { class: 'list' });
    for (const p of items) {
      const days = get(p, 'days_since_contact');
      const name = get(p, 'name');
      listEl.appendChild(h('div', { class: 'row' },
        h('span', { class: 'body' },
          h('p', { class: 'title' }, name),
          h('p', { class: 'meta' },
            fmtNumber(get(p, 'open_tickets')) + ' open tickets' + (typeof days === 'number' ? (' · ' + Math.round(days) + 'd since contact') : ''))),
        S.button('Draft outreach', () => createProspectOutreachDraft(workspaceID, name), 'secondary')));
    }
    return h('div', null, head, listEl);
  }

  // createReviewReplyDraft is "Draft reply"'s mechanism: it never reads or
  // invents any review's actual text (internal/gateway/
  // workspace_detail.go's own doc comment on why not) -- it only ever
  // builds a generic, code-built reply shell from the review id typed into
  // the field below, ready for whenever a real public-reviews source
  // exists to list real rows here instead of this manual entry.
  async function createReviewReplyDraft(workspaceID, reviewID, msg) {
    const S = window.appShared;
    const id = (reviewID || '').trim();
    if (!id) { replace(msg, S.errorBox('Review id is required', new Error('empty review id'))); return; }
    try {
      const draft = await api.createWorkspaceDraft(workspaceID, 'review_reply', { review_id: id });
      replace(msg, null);
      S.toast('Reply drafted.');
      S.go('drafts', get(draft, 'id'));
    } catch (err) {
      replace(msg, S.errorBox('Could not create the draft', err));
    }
  }

  function publicReviewsPanel(workspaceID, tile) {
    const S = window.appShared;
    const state = get(tile, 'state');
    const reviewIDInput = h('input', { type: 'text', placeholder: 'Review id', autocomplete: 'off' });
    const msg = h('div');
    const draftButton = S.button('Draft reply', () => createReviewReplyDraft(workspaceID, reviewIDInput.value, msg), 'secondary');
    return h('div', { class: 'card' },
      h('div', { class: 'row-meta' }, h('span', { class: 'title' }, 'Reviews'), dashboardStateBadge(S, state)),
      S.empty('No public-reviews source is connected yet.'),
      h('p', { class: 'meta' }, 'The reply mechanism below already works: paste a review id to draft a reply shell for it.'),
      h('div', { class: 'form inline' }, reviewIDInput, draftButton),
      msg);
  }

  function capacityNotePanel(note) {
    if (!note) return null;
    return h('section', { class: 'panel' },
      h('h2', null, 'Capacity note'),
      h('p', null, get(note, 'person_name') + ' (design lead) is at ' + fmtNumber(get(note, 'allocation')) + ' allocation.'));
  }

  function marketingTiles(view, workspaceID) {
    const m = get(view, 'marketing');
    if (!m) return null;
    const prospects = get(m, 'prospects');
    return h('div', { class: 'stack' },
      marketingStatTiles(prospects),
      marketInterestCard(list(get(m, 'trend_tiles'))),
      themesCard(),
      feedbackMixCard(),
      contentPipelineCard(),
      insightCard(prospects),
      prospectsPanel(workspaceID, prospects),
      publicReviewsPanel(workspaceID, get(m, 'public_reviews')),
      capacityNotePanel(get(m, 'capacity_note')));
  }

  // ---- shared "filtered existing sections": needs-you, meetings, threads ----

  function needsYouPanel(items, label) {
    const S = window.appShared;
    const heading = h('p', { class: 'label' }, label || 'Needs you');
    if (!items.length) return h('div', null, heading, S.empty('Nothing needs your attention here right now.'));
    const listEl = h('div', { class: 'list' });
    for (const it of items) {
      const kind = get(it, 'Kind', 'kind');
      const id = get(it, 'ID', 'id');
      const pClass = S.priorityClass(it);
      const due = S.dueBadge(get(it, 'Deadline', 'deadline'));
      listEl.appendChild(h('button', {
        type: 'button', class: 'row ' + priorityEdgeClass(pClass),
        on: { click: () => S.go(kind === 'approval' ? 'approvals' : 'decisions', id) },
      },
      h('span', { class: 'ico' }, S.kindGlyph(it)),
      h('span', { class: 'body' },
        h('p', { class: 'title' }, get(it, 'Title', 'title') || id),
        due ? h('p', { class: 'meta' }, due) : null),
      priorityBadge(S, pClass)));
    }
    return h('div', null, heading, listEl);
  }

  function meetingsPanel(items) {
    const S = window.appShared;
    const heading = h('p', { class: 'label' }, 'Recent meetings');
    if (!items.length) return h('div', null, heading, S.empty('No meetings linked to this workspace yet.'));
    const listEl = h('div', { class: 'list' });
    for (const m of items) {
      const id = get(m, 'session_id');
      listEl.appendChild(h('button', {
        type: 'button', class: 'row', on: { click: () => S.go('meetings', id) },
      },
      S.icon('microphone'),
      h('span', { class: 'body' },
        h('p', { class: 'title' }, get(m, 'event_title') || 'Meeting'),
        h('p', { class: 'meta' }, fmtDate(get(m, 'started_at'))))));
    }
    return h('div', null, heading, listEl);
  }

  function threadsPanel(items) {
    const S = window.appShared;
    const heading = h('p', { class: 'label' }, 'Recent threads');
    if (!items.length) return h('div', null, heading, S.empty('No threads linked to this workspace yet.'));
    const listEl = h('div', { class: 'list' });
    for (const t of items) {
      const id = get(t, 'id');
      const label = get(t, 'anchor_label') || 'Unanchored';
      listEl.appendChild(h('button', {
        type: 'button', class: 'row', on: { click: () => S.go('threads', id) },
      },
      S.icon('message-circle'),
      h('span', { class: 'body' },
        h('p', { class: 'title' }, get(t, 'title') || label),
        h('p', { class: 'meta' }, label))));
    }
    return h('div', null, heading, listEl);
  }

  // ---- assembly ----

  async function renderWorkspaceDetail(c, spec, sub, gen) {
    const S = window.appShared;
    const id = get(spec, 'id');
    let view;
    try {
      view = await api.workspace(id);
    } catch (err) {
      if (S.current(gen)) replace(c, S.header(get(spec, 'name') || id, workspaceSubtitle(spec)), S.errorBox('Could not load this workspace', err));
      return;
    }
    if (!S.current(gen)) return;

    const tabs = h('div', { class: 'tabs', role: 'tablist' });
    for (const s of SUBS) {
      tabs.appendChild(h('button', {
        type: 'button', role: 'tab', class: 'tab' + (s.id === sub ? ' active' : ''),
        'aria-selected': s.id === sub ? 'true' : 'false',
        on: { click: () => S.goWorkspace(id, s.id) },
      }, s.label));
    }

    const template = get(view, 'template') || get(spec, 'template');
    let tiles = null;
    if (template === 'project') tiles = projectTiles(view);
    else if (template === 'finance') tiles = financeTiles(view);
    else if (template === 'clients') tiles = clientsTiles(view);
    else if (template === 'people') tiles = peopleTiles(view, id);
    else if (template === 'ideas') tiles = ideasTiles(view, () => renderWorkspaceDetail(c, spec, sub, gen));
    else if (template === 'research') tiles = researchTiles(view);
    else if (template === 'marketing') tiles = marketingTiles(view, id);

    const body = tiles
      ? h('div', { class: 'stack' }, tiles)
      : h('section', { class: 'panel' }, S.empty('The full ' + template + ' workspace view is not built yet.'));

    const name = get(view, 'name') || get(spec, 'name') || id;
    const subtitle = get(view, 'description') || TEMPLATE_SUBTITLES[template] || '';
    const needsYouLabel = template === 'project' ? ('Needs you, in ' + name) : 'Needs you';

    replace(c, S.header(name, subtitle),
      tabs, body,
      needsYouPanel(list(get(view, 'needs_you')), needsYouLabel),
      meetingsPanel(list(get(view, 'meetings'))),
      threadsPanel(list(get(view, 'threads'))));
  }

  // renderNavList fills the sidebar's Workspaces disclosure (index.html's
  // #workspaces-nav-list), highlighting the one open in the main pane, if
  // any. Called both at boot (window.views._workspaceNav) and on every
  // render of this view, so the highlight always matches what's on screen.
  function renderNavList(items) {
    const S = window.appShared;
    const el = document.getElementById('workspaces-nav-list');
    if (!el) return;
    const currentID = S.state.view === 'workspaces' ? splitParam(S.state.param).id : '';
    replace(el, list(items).map((w) => {
      const id = get(w, 'id');
      return h('li', null, h('button', {
        type: 'button', class: 'nav-sub-item' + (id === currentID ? ' active' : ''),
        title: get(w, 'name') || id,
        on: { click: () => S.goWorkspace(id, S.DEFAULT_WORKSPACE_SUB) },
      }, get(w, 'name') || id));
    }));
    if (!list(items).length) el.appendChild(h('li', { class: 'muted small' }, 'None yet'));
  }

  // _workspaceNav is boot's one-time, best-effort load of the sidebar
  // disclosure (workspaces rarely change while the daemon runs — they load
  // once from YAML at startup — so unlike Recent threads this is not on the
  // periodic refresh timer). It fails silently, exactly like refreshRecent.
  async function loadWorkspaceNav() {
    try { renderNavList(await api.workspaces()); } catch (_) { /* best-effort */ }
  }

  window.views = window.views || {};
  window.views.workspaces = viewWorkspaces;
  window.views._workspaceNav = loadWorkspaceNav;
})();
