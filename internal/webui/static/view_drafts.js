// view_drafts.js: the Drafts view (docs/slices/UI.md Phase 3c, U10-A).
//
// Before this phase, "Drafts" listed pending outward-message envelopes (V's
// D2-A: a pending gmail.send_message/twinlink.send_message envelope itself
// WAS the draft). The owner reopened that decision and picked option A: a
// real, separately-persisted `drafts` table (internal/store/drafts.go,
// migration 0021), so a draft can be edited and saved before it is ever
// proposed as an approval at all. This file is a full rewrite, not the
// Phase 2 placeholder it replaces -- it no longer shares approvalRow/
// renderApprovalDetail with view_approvals.js (see app.js's own updated
// comment on that section).
//
// The editor's body is a plain textarea: no rich text, no HTML rendering,
// per the plan's own deliberate simplicity choice.
//
// Save vs. "Send for approval" (the plan leaves this an open question --
// documented here and in api.js/internal/gateway/drafts.go): Send for
// approval is never coupled to Save. It always posts the editor's current
// to/subject/body directly through api.submitDraft, which proposes exactly
// those values -- never a separately re-fetched, possibly-stale stored row
// -- and never itself writes the drafts table. So clicking Send immediately
// after typing, without ever clicking Save, still proposes exactly what's
// on screen; clicking Save first only changes what a later re-open of this
// same draft would show, never what gets proposed now.
'use strict';

(function () {
  const { h, replace, get, list, fmtAgo } = window.dom;

  // draftRow: a dense row per docs/slices/UI-polish.md's cross-cutting
  // "dense rows over big bordered boxes" rule. template_label
  // (internal/store/drafts.go's DraftTemplateLabel) is already one human
  // line ("Reply", "Investor update section", ...), never the raw
  // "Template: X · Source: Y" form -- that literal string lives only in
  // view_workspaces.js (out of this file's scope), not here, so there was
  // nothing to remove; this only adds the row-kind icon + row-line wrapper
  // view_today.js and (now) view_approvals.js already use, for a consistent
  // dense look across every list in the app. 'signature' (a pen glyph) is
  // reused from the existing ICON_GLYPHS set (app.js) -- the closest match
  // to "a message being drafted"; no new icon name is introduced.
  function draftRow(d, selected) {
    const id = get(d, 'id');
    const subject = get(d, 'subject');
    const S = window.appShared;
    return h('li', null, h('button', {
      type: 'button', class: 'row' + (selected ? ' selected' : ''), on: { click: () => S.go('drafts', id) },
    }, h('span', { class: 'row-line' }, S.icon('signature'), h('span', { class: 'row-main' },
      h('span', { class: 'row-title' }, subject || '(no subject)'),
      h('span', { class: 'row-sub muted' }, get(d, 'template_label') || ''),
      h('span', { class: 'row-meta' }, h('span', { class: 'muted' }, fmtAgo(get(d, 'updated_at'))))))));
  }

  async function renderDraftDetail(pane, id, gen) {
    const S = window.appShared;
    let d;
    try {
      d = await window.api.draft(id);
    } catch (err) {
      if (S.current(gen)) replace(pane, S.errorBox('Could not load draft', err));
      return;
    }
    if (!S.current(gen)) return;

    const fid = 'draft-' + id;
    const to = h('input', { id: fid + '-to', type: 'text', autocomplete: 'off' });
    to.value = get(d, 'to') || '';
    const subject = h('input', { id: fid + '-subject', type: 'text', autocomplete: 'off' });
    subject.value = get(d, 'subject') || '';
    const body = h('textarea', { id: fid + '-body', rows: 14 });
    body.value = get(d, 'body') || '';
    const msg = h('div');

    const save = S.button('Save', async () => {
      save.disabled = true;
      try {
        await window.api.saveDraft(id, to.value, subject.value, body.value);
        replace(msg, h('p', { class: 'muted small' }, 'Saved.'));
        S.refreshCounts();
      } catch (err) {
        replace(msg, S.errorBox('Could not save', err));
      } finally {
        save.disabled = false;
      }
    }, 'secondary');

    const send = S.button('Send for approval', async () => {
      send.disabled = true;
      replace(msg, null);
      try {
        const out = await window.api.submitDraft(id, to.value, subject.value, body.value);
        S.toast('Sent for approval. Review it in Approvals.', '');
        S.refreshCounts();
        S.go('approvals', get(out, 'approval_id'));
      } catch (err) {
        send.disabled = false;
        replace(msg, S.errorBox(err.status === 422 ? 'This recipient needs a closer look' : 'Could not send this for approval', err));
      }
    }, 'primary');

    replace(pane,
      h('div', { class: 'card-sec' },
        h('h2', null, get(d, 'template_label') || 'Draft'),
        get(d, 'source_card_id') ? h('p', { class: 'muted small' }, 'From decision ' + get(d, 'source_card_id')) : null,
        h('div', { class: 'form' },
          h('label', { for: fid + '-to' }, 'To'), to,
          h('label', { for: fid + '-subject' }, 'Subject'), subject,
          h('label', { for: fid + '-body' }, 'Body'), body),
        msg,
        h('div', { class: 'actions' }, send, save)));
  }

  async function viewDrafts(c, param, gen) {
    const S = window.appShared;
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    replace(c, S.header('Drafts', 'Messages tagged Reply, Delegation or Investor update section, waiting for your edits before they go to Approvals.'),
      h('div', { class: 'split' }, listPane, detail));

    let items = [];
    try {
      items = list(await window.api.drafts());
    } catch (err) {
      if (!S.current(gen)) return;
      replace(listPane, S.errorBox('Could not load drafts', err));
    }
    if (!S.current(gen)) return;
    S.setCount('drafts', items.length);
    if (!items.length) {
      // Cross-cutting empty-state rule: one short useful line, not "Nothing
      // here yet." -- says what will show up here, same as the header's own
      // description just above.
      listPane.appendChild(S.empty('No drafts yet. A reply, delegation or investor update lands here before it goes to Approvals.'));
    } else {
      const ul = h('ul', { class: 'rows' });
      for (const d of items) ul.appendChild(draftRow(d, get(d, 'id') === param));
      listPane.appendChild(ul);
    }

    if (param) {
      await renderDraftDetail(detail, param, gen);
    } else if (items.length) {
      // Same ordering rule view_approvals.js now applies (docs/slices/
      // UI-polish.md's Findings): the "select one" placeholder only makes
      // sense when there is something to select.
      replace(detail, S.empty('Select a draft to edit it.'));
    } else {
      replace(detail, null);
    }
  }

  window.views = window.views || {};
  window.views.drafts = viewDrafts;
})();
