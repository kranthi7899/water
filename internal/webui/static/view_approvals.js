// view_approvals.js: the Approvals view (docs/slices/UI.md Phase 2 split
// app.js into one file per view). Registers window.views.approvals.
// approvalRow and renderApprovalDetail (shared with view_drafts.js) live in
// window.appShared; see app.js's header comment.
'use strict';

(function () {
  const { h, replace, get, list } = window.dom;
  const api = window.api;

  // priorityRank/lessForQueue mirror internal/approvals.PriorityRank/
  // LessForQueue (docs/slices/UI.md Phase 3a) client-side, the same way
  // app.js's decisionPriorityClass already mirrors needsyou.DecisionPriority:
  // the Go function is the small, directly-testable, canonical rule (see its
  // own doc comment and internal/approvals/sort_test.go); this is its JS
  // twin, since the Approvals queue's dense rows are sorted in the browser,
  // not by the server.
  function priorityRank(p) { return p === 'urgent' ? 0 : p === 'high' ? 1 : 2; }
  function lessForQueue(a, b) {
    const ra = priorityRank(get(a, 'priority')), rb = priorityRank(get(b, 'priority'));
    if (ra !== rb) return ra - rb;
    const ta = Date.parse(get(a, 'deadline') || ''), tb = Date.parse(get(b, 'deadline') || '');
    const az = isNaN(ta), bz = isNaN(tb);
    if (az !== bz) return az ? 1 : -1; // the one with a deadline sorts first
    if (!az && ta !== tb) return ta - tb;
    return String(get(a, 'created_at') || '').localeCompare(String(get(b, 'created_at') || ''));
  }

  // ---------- row icon + priority edge (docs/slices/UI-polish.md, "Approvals":
  // icon, title, one origin line, priority badge, coloured left edge, sorted
  // by priority) ----------
  //
  // approvalRow itself (window.appShared.approvalRow) is shared with a
  // decision card's per-action result rendering and lives in app.js, out of
  // this file's scope for this slice. Everything below only reshapes the
  // node approvalRow already returns -- a wrapping .row-line (the same
  // pattern view_today.js already uses for its own kind glyph, app.css's
  // .row-line/.row-kind, "wraps the glyph and .row-main in one flex row
  // without touching .row itself") plus the reference's edge-*/badge
  // classes, both already defined in app.css. No new markup vocabulary.

  // A function-name -> icon-glyph map, the same idea view_decisions.js's
  // KIND_ICONS already uses for evidence kinds, extended to approval
  // actions. Falls back to S.isOutward: an outward send reads as mail, an
  // internal write reads as a flag, unless the action name itself points at
  // a more specific glyph.
  function approvalIconName(e, S) {
    const action = String(get(e, 'action') || '');
    if (/budget|finance|spend|cost/.test(action)) return 'report-money';
    if (/priority/.test(action)) return 'flag';
    if (/comment/.test(action)) return 'message-circle';
    if (/sign|contract|agreement/.test(action)) return 'signature';
    if (/send_message|mail/.test(action)) return 'mail';
    return S.isOutward(action) ? 'mail' : 'flag';
  }

  function edgeClass(priority) {
    return priority === 'urgent' ? 'edge-danger' : priority === 'high' ? 'edge-warn' : 'edge-none';
  }

  function priorityBadge(priority, S) {
    if (priority === 'urgent') return S.badge('Urgent', 'danger');
    if (priority === 'high') return S.badge('High', 'warn');
    return S.badge('Normal');
  }

  function denseApprovalRow(e, selected, S) {
    const row = S.approvalRow(e, selected, 'approvals');
    const btn = row.firstElementChild; // the <button class="row ...">
    const priority = get(e, 'priority');
    btn.classList.add(edgeClass(priority));
    const existing = btn.firstElementChild; // the row-line(avatar+main) or bare row-main
    replace(btn, h('span', { class: 'row-line' }, S.icon(approvalIconName(e, S)), existing));
    const meta = btn.querySelector('.row-meta');
    if (meta) meta.appendChild(priorityBadge(priority, S));
    // W's recipient warnings (finding 23): flagged on the row too, not only
    // the amber box on the card, so a warned approval stands out before it's
    // even opened.
    if (meta && list(get(e, 'warnings')).length) meta.appendChild(S.badge('Warning', 'warn'));
    return row;
  }

  async function viewApprovals(c, param, gen) {
    const S = window.appShared;
    const tabs = h('div', { class: 'tabs', role: 'tablist' });
    for (const [key, label] of [['pending', 'Pending'], ['decided', 'Decided']]) {
      tabs.appendChild(h('button', {
        type: 'button', role: 'tab', class: 'tab' + (S.state.approvalTab === key ? ' active' : ''),
        'aria-selected': S.state.approvalTab === key ? 'true' : 'false',
        on: { click: () => { S.state.approvalTab = key; S.render(); } },
      }, label));
    }
    const listPane = h('div', { class: 'list-pane' }, tabs);
    const detail = h('div', { class: 'detail-pane' });
    replace(c, S.header('Approvals', 'Every outward action waits here for your yes.'), h('div', { class: 'split' }, listPane, detail));

    // Fetch the list and (if there's a selected approval) the detail pane in
    // parallel, same as before -- but the "select an approval" placeholder
    // is no longer part of that parallel fetch. It depends on the list's
    // own length (docs/slices/UI-polish.md's Findings: this was a real
    // ordering bug -- the placeholder used to be written unconditionally,
    // before items.length was known, so it showed even for an empty list).
    // It's decided below, after envs resolves, only when there is no
    // selected approval to show instead.
    const envsPromise = api.approvals(S.state.approvalTab, 100);
    const detailPromise = param ? S.renderApprovalDetail(detail, param, gen, 'approvals') : null;

    const envs = await envsPromise;
    if (!S.current(gen)) return;
    const items = list(envs);
    // Dense rows sorted by priority then deadline (docs/slices/UI.md Phase
    // 3a), not the server's own created_at order.
    items.sort(lessForQueue);
    if (S.state.approvalTab === 'pending') S.setCount('approvals', items.length);

    if (!param) {
      // Only shown when the list is non-empty (docs/slices/UI-polish.md);
      // an empty list already says so itself, below -- no second, redundant
      // placeholder in the detail pane.
      replace(detail, items.length ? S.empty('Select an approval to review it.') : null);
    }

    const ul = h('ul', { class: 'rows' });
    if (!items.length) {
      listPane.appendChild(S.empty(S.state.approvalTab === 'pending'
        ? 'No approvals waiting. Anything the assistant wants to send or change will appear here.'
        : 'Nothing decided yet.'));
    }
    for (const e of items) ul.appendChild(denseApprovalRow(e, get(e, 'id') === param, S));
    listPane.appendChild(ul);

    if (detailPromise) await detailPromise;
  }

  window.views = window.views || {};
  window.views.approvals = viewApprovals;
})();
