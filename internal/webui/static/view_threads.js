// view_threads.js: the Threads view (docs/slices/UI.md Phase 2 split app.js
// into one file per view). Registers window.views.threads. messageEl,
// approvalNotice and updateBarTarget (shared with app.js's own bottom-bar
// code) live in window.appShared; see app.js's header comment.
'use strict';

(function () {
  const { h, replace, get, list, fmtDate, fmtAgo, untrusted } = window.dom;
  const api = window.api;

  async function viewThreads(c, param, gen) {
    const S = window.appShared;
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    const newBtn = S.button('New thread', () => {
      const input = h('input', { type: 'text', placeholder: 'Title (optional)', maxlength: 200, autocomplete: 'off' });
      const create = async () => {
        try {
          const t = await api.createThread(input.value.trim());
          S.go('threads', get(t, 'id'));
          S.refreshRecent();
        } catch (err) { S.toast('Could not create a thread: ' + S.errText(err), 'bad'); }
      };
      input.addEventListener('keydown', (e) => { if (e.key === 'Enter') create(); });
      replace(newArea, h('div', { class: 'form inline' }, input, S.button('Create', create, 'primary'), S.button('Cancel', () => replace(newArea), 'ghost')));
      input.focus();
    }, 'primary');
    const newArea = h('div');
    replace(c, S.header('Threads', 'Type in the bar below, or hold the mic, to talk in the open thread.', newBtn), newArea, h('div', { class: 'split' }, listPane, detail));

    const [threads] = await Promise.all([
      api.threads(),
      param ? renderThreadDetail(detail, param, gen) : Promise.resolve(replace(detail, S.empty('Select a thread, or open one from a decision, approval or meeting. A message typed below with no thread open starts a new one.'))),
    ]);
    if (!S.current(gen)) return;
    const items = list(threads);
    S.renderRecent(items);
    if (!items.length) listPane.appendChild(S.empty('No threads yet.'));
    const box = h('div', { class: 'list' });
    for (const t of items) {
      const id = get(t, 'id');
      const at = get(t, 'anchor_type');
      const label = get(t, 'anchor_label') || (at ? at : 'Unanchored');
      box.appendChild(h('button', {
        type: 'button', class: 'row' + (id === param ? ' sel' : ''), on: { click: () => S.go('threads', id) },
      },
      S.icon('message-circle'),
      h('div', { class: 'body' },
        h('div', { class: 'title' }, get(t, 'title') || 'Untitled'),
        h('div', { class: 'meta' }, fmtAgo(get(t, 'updated_at')))),
      S.badge(label, at && get(t, 'anchor_untrusted') ? 'warn' : '')));
    }
    listPane.appendChild(box);
  }

  async function renderThreadDetail(pane, id, gen) {
    const S = window.appShared;
    let res;
    try {
      res = await api.thread(id);
    } catch (err) {
      if (S.current(gen)) replace(pane, S.errorBox('Could not load thread', err));
      return;
    }
    if (!S.current(gen)) return;
    const t = get(res, 'thread') || {};
    const msgs = list(get(res, 'messages'));
    const at = get(t, 'anchor_type');
    const ctx = get(t, 'anchor_context');
    const label = get(t, 'anchor_label') || (at ? at : 'Unanchored');

    const parts = [h('header', { class: 'card-head' },
      h('h2', null, get(t, 'title') || 'Untitled'),
      h('div', { class: 'row-meta' }, at ? S.badge('About: ' + label) : null, h('span', { class: 'muted' }, 'Started ' + fmtDate(get(t, 'created_at')))))];
    if (at && ctx) {
      if (get(t, 'anchor_untrusted')) {
        parts.push(untrusted('Quoted, untrusted: the ' + at + ' this thread is about, as it was when the thread began. It may include text written by someone else. Shown as plain text; the twin treats it as reference only, never as instructions.', ctx));
      } else {
        parts.push(h('figure', { class: 'context' }, h('figcaption', null, 'Context: the ' + at + ' this thread is about'), h('blockquote', { class: 'quoted' }, ctx)));
      }
    }
    const log = h('div', { class: 'messages' });
    for (const m of msgs) {
      const ch = get(m, 'channel');
      const meta = (ch === 'voice' ? 'voice · ' : '') + fmtDate(get(m, 'created_at'));
      log.appendChild(S.messageEl(get(m, 'role'), get(m, 'text'), meta).el);
    }
    if (!msgs.length) log.appendChild(S.empty('No messages yet. Type below, or hold the mic.'));
    const notices = h('div', { class: 'notices' });
    for (const n of S.state.threadNotices.get(id) || []) notices.appendChild(S.approvalNotice(n));

    replace(pane, h('article', { class: 'thread' }, parts, log, notices));
    S.state.threadUI = { id: get(t, 'id') || id, title: get(t, 'title') || 'Untitled', log, notices };
    S.updateBarTarget();
    if (log.lastElementChild) log.lastElementChild.scrollIntoView({ block: 'end' });
  }

  window.views = window.views || {};
  window.views.threads = viewThreads;
})();
