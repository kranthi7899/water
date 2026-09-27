// view_drafts.js: the Drafts view (docs/slices/UI.md Phase 2 split app.js
// into one file per view). Registers window.views.drafts. pendingDrafts,
// approvalRow and renderApprovalDetail (shared with view_approvals.js) live
// in window.appShared; see app.js's header comment.
'use strict';

(function () {
  const { h, replace, get } = window.dom;

  async function viewDrafts(c, param, gen) {
    const S = window.appShared;
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    replace(c, S.header('Drafts', 'Messages Water has written for you, waiting for your yes before they are sent. Gmail drafts saved straight to Gmail stay there and are not listed.'),
      h('div', { class: 'split' }, listPane, detail));
    const [items] = await Promise.all([
      S.pendingDrafts(),
      param ? S.renderApprovalDetail(detail, param, gen, 'drafts') : Promise.resolve(replace(detail, S.empty('Select a draft to read it before it is sent.'))),
    ]);
    if (!S.current(gen)) return;
    S.setCount('drafts', items.length);
    if (!items.length) listPane.appendChild(S.empty('No drafts waiting to be sent.'));
    const ul = h('ul', { class: 'rows' });
    for (const e of items) ul.appendChild(S.approvalRow(e, get(e, 'id') === param, 'drafts'));
    listPane.appendChild(ul);
  }

  window.views = window.views || {};
  window.views.drafts = viewDrafts;
})();
