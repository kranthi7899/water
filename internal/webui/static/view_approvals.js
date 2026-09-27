// view_approvals.js: the Approvals view (docs/slices/UI.md Phase 2 split
// app.js into one file per view). Registers window.views.approvals.
// approvalRow and renderApprovalDetail (shared with view_drafts.js) live in
// window.appShared; see app.js's header comment.
'use strict';

(function () {
  const { h, replace, get, list } = window.dom;
  const api = window.api;

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
    if (S.state.approvalTab === 'pending') S.setCount('approvals', items.length);
    const ul = h('ul', { class: 'rows' });
    if (!items.length) listPane.appendChild(S.empty(S.state.approvalTab === 'pending' ? 'No approvals waiting.' : 'Nothing decided yet.'));
    for (const e of items) ul.appendChild(S.approvalRow(e, get(e, 'id') === param, 'approvals'));
    listPane.appendChild(ul);
  }

  window.views = window.views || {};
  window.views.approvals = viewApprovals;
})();
