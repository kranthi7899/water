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

    const [envs] = await Promise.all([
      api.approvals(S.state.approvalTab, 100),
      param ? S.renderApprovalDetail(detail, param, gen, 'approvals') : Promise.resolve(replace(detail, S.empty('Select an approval to review it.'))),
    ]);
    if (!S.current(gen)) return;
    const items = list(envs);
    // Dense rows sorted by priority then deadline (docs/slices/UI.md Phase
    // 3a), not the server's own created_at order.
    items.sort(lessForQueue);
    if (S.state.approvalTab === 'pending') S.setCount('approvals', items.length);
    const ul = h('ul', { class: 'rows' });
    if (!items.length) listPane.appendChild(S.empty(S.state.approvalTab === 'pending' ? 'No approvals waiting.' : 'Nothing decided yet.'));
    for (const e of items) {
      const row = S.approvalRow(e, get(e, 'id') === param, 'approvals');
      // W's recipient warnings (finding 23): flagged on the row too, not
      // only the amber box on the card, so a warned approval stands out
      // before it's even opened.
      if (list(get(e, 'warnings')).length) {
        const meta = row.querySelector('.row-meta');
        if (meta) meta.appendChild(S.badge('Warning', 'warn'));
      }
      ul.appendChild(row);
    }
    listPane.appendChild(ul);
  }

  window.views = window.views || {};
  window.views.approvals = viewApprovals;
})();
