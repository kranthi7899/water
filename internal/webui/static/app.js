// app.js: the workspace UI's router and shared glue (docs/slices/UI.md Phase
// 2 split app.js's former 1200-odd lines into one file per view —
// view_today.js, view_decisions.js, view_approvals.js, view_drafts.js,
// view_meetings.js, view_threads.js, view_dashboards.js, view_workspaces.js —
// plus this file, which now holds only what more than one view needs: the
// hash router, the shared UI helpers, the payload/staging forms, the bottom
// bar, and boot.
//
// Every helper a view file needs is exposed on window.appShared, assigned
// near the bottom of this IIFE. This file is loaded before the view files
// (see index.html), so by the time any view file's own IIFE runs,
// window.appShared and window.views (the per-view registry each view file
// fills in, keyed by hash view name) already exist. Nothing here calls
// boot() directly, though: it waits for DOMContentLoaded, which fires only
// once every deferred script — this one and every view file after it — has
// run, so render()'s first call always finds every view already registered.
//
// Rendering goes through dom.js only; every server string is a text node.
// Nothing here executes an outward action by itself: a decision card is
// staged into a PENDING approval envelope, and only an explicit, separate
// confirm click answers that envelope "yes", through the same
// approval-decision endpoint every other client uses (api.js). Editing an
// envelope voids it and stages a new one, which again needs its own yes.
'use strict';

(function () {
  const { h, replace, get, list, fmtDate, fmtDay, fmtAgo } = window.dom;
  const api = window.api;

  const main = document.getElementById('main');
  const toastEl = document.getElementById('toast');
  const VIEWS = ['today', 'decisions', 'drafts', 'approvals', 'threads', 'meetings', 'dashboards', 'workspaces'];
  // Outward-message actions (docs/slices/V.md D2): what Drafts lists, and
  // the actions whose "executed" reads as "sent".
  const OUTWARD = ['gmail.send_message', 'twinlink.send_message'];
  // The shape the store mints and the native mic handler accepts.
  const THREAD_ID = /^thr_[0-9a-f]+$/;
  // DEFAULT_WORKSPACE_SUB is the sub-page a bare workspace open (no second
  // segment) lands on (docs/slices/UI.md Phase 2: "pick a sensible default
  // <sub> value and document it"). Every workspace gets an "overview" for
  // now; per-workspace sub-navigation is Phase 5's job.
  const DEFAULT_WORKSPACE_SUB = 'overview';

  const state = {
    view: 'today',
    param: '',
    gen: 0, // bumped on every render; a response for an older render is dropped
    approvalTab: 'pending',
    // approval_required notices raised while a thread turn streamed, kept
    // per thread so they survive the re-fetch on done.
    threadNotices: new Map(),
    streaming: null, // {threadId, taskId}
    // The thread currently rendered in the Threads view: where the bottom
    // bar posts, and where a streaming reply is drawn.
    threadUI: null, // {id, title, log, notices}
    expectHash: null,
  };

  // ---------- small helpers ----------

  let toastTimer = 0;
  function toast(msg, kind) {
    toastEl.textContent = String(msg);
    toastEl.className = 'toast' + (kind ? ' ' + kind : '');
    toastEl.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { toastEl.hidden = true; }, 5000);
  }

  function errText(err) {
    if (err && (err.status === 401 || err.status === 403)) {
      return 'The workspace could not authenticate with the Water daemon.';
    }
    if (err && err.status === 404) return 'Not found. ' + (err.message || '');
    return (err && err.message) ? err.message : String(err);
  }

  function errorBox(prefix, err) {
    return h('div', { class: 'error', role: 'alert' }, prefix + ': ' + errText(err));
  }

  function badge(text, kind) {
    return h('span', { class: 'badge' + (kind ? ' ' + kind : '') }, text);
  }

  // armed disables a confirm button for a moment after it appears, so a
  // double-click on the button it replaced can't land on it: approving stays
  // two deliberate clicks (D3).
  function armed(b) {
    b.disabled = true;
    setTimeout(() => { b.disabled = false; }, 600);
    return b;
  }

  function button(label, onClick, cls, extra) {
    return h('button', Object.assign({ type: 'button', class: cls || 'secondary', on: { click: onClick } }, extra || {}), label);
  }

  function header(title, sub, actions) {
    return h('header', { class: 'view-head' },
      h('div', null, h('h1', null, title), sub ? h('p', { class: 'sub' }, sub) : null),
      actions ? h('div', { class: 'head-actions' }, actions) : null);
  }

  function empty(text) { return h('p', { class: 'empty' }, text); }

  function setCount(name, n) {
    const el = document.querySelector('[data-count="' + name + '"]');
    if (el) el.textContent = n > 0 ? String(n) : '';
  }

  function current(gen) { return gen === state.gen; }

  function isOutward(action) { return OUTWARD.includes(String(action || '')); }

  // statusLabel is an envelope's status as shown: an executed outward
  // message reads "sent".
  function statusLabel(status, action) {
    if (status === 'executed' && isOutward(action)) return 'sent';
    return status || '';
  }

  function statusBadge(env) {
    const s = get(env, 'status');
    return badge(statusLabel(s, get(env, 'action')), 'status-' + s);
  }

  // riskLabel turns a connector risk level into a plain-language phrase
  // (docs/slices/UI.md Phase 0b: "Risk x" -> "Medium risk"), instead of
  // echoing the raw "low"/"medium"/"high" enum value.
  function riskLabel(r) {
    return { low: 'Low risk', medium: 'Medium risk', high: 'High risk' }[r] || String(r || '');
  }

  // actionLabel turns a "connector.function" id into plain language
  // (docs/slices/UI.md Phase 0b: "Prepare gmail.send_message" -> "Prepare
  // email"). Known mail-shaped functions get a specific phrase; anything
  // else falls back to its own function name with underscores as spaces,
  // never the raw dotted id.
  const ACTION_LABELS = {
    'gmail.send_message': 'email', 'gmail.draft_message': 'email draft', 'gmail.draft_for_review': 'email draft',
    'twinlink.send_message': 'message',
  };
  function actionLabel(fn) {
    fn = String(fn || '');
    if (ACTION_LABELS[fn]) return ACTION_LABELS[fn];
    const tail = fn.split('.').pop() || fn;
    return tail.replace(/_/g, ' ') || fn;
  }

  // dueBadge is a plain, date-only "Due Oct 15" chip (docs/slices/UI.md
  // Phase 0b): no time-of-day, which reads calmer than the full timestamp
  // fmtDate gives.
  function dueBadge(date) {
    const d = fmtDay(date);
    return d ? h('span', { class: 'muted' }, 'Due ' + d) : null;
  }

  // extGlyph replaces the old raw "External content" pill with a small,
  // titled glyph (docs/slices/UI.md Phase 0b). It never touches how the
  // untrusted content itself is quoted/escaped elsewhere (dom.untrusted) —
  // only this label.
  function extGlyph() {
    return h('span', { class: 'ext-glyph', title: 'Built from an outside email' }, '⚠');
  }

  // decisionPriorityClass mirrors internal/needsyou.DecisionPriority
  // (U14) client-side: the Decisions view's own card list carries Severity
  // and Deadline but not a server-computed Priority field in this phase —
  // only needsyou.Item (Today's needs-you list) does. Keeping this in step
  // with priority.go's rule is this function's whole job; it has no other
  // reason to exist once Card itself carries Priority.
  function decisionPriorityClass(sev, deadline) {
    const days = deadlineDays(deadline);
    if (sev >= 3 || (days !== null && days <= 3)) return 'p-urgent';
    if (sev === 2 || (days !== null && days <= 7)) return 'p-high';
    return 'p-normal';
  }

  function deadlineDays(deadline) {
    if (!deadline) return null;
    const t = Date.parse(deadline);
    if (isNaN(t)) return null;
    return (t - Date.now()) / 86400000;
  }

  // priorityClass reads a needs-you item's own server-computed Priority
  // field (needsyou.Item.Priority, U14) straight through, falling back to
  // 'p-normal' for an item from before this field existed.
  function priorityClass(it) {
    const p = get(it, 'Priority', 'priority');
    return p === 'urgent' ? 'p-urgent' : p === 'high' ? 'p-high' : 'p-normal';
  }

  // kindGlyph picks a plain Unicode glyph for a Today row's kind
  // (docs/slices/UI.md Phase 3a: "a kind icon... pick sensible ones per
  // Item.Kind/whatever richer kind concept exists"): a decision card is
  // "◆"; an approval is "✉" (an outward action awaiting send) unless it's a
  // person's own request (OriginKind "person_request", U15), which reads
  // "☞" instead, so a request FOR you never looks like a message Water is
  // about to send.
  function kindGlyph(it) {
    if (get(it, 'Kind', 'kind') !== 'approval') return '◆';
    return get(it, 'OriginKind', 'origin_kind') === 'person_request' ? '☞' : '✉';
  }

  // readinessLabel is only ever called for a non-ready state (Today and the
  // decision card both check readiness !== 'ready' first): "ready" itself
  // is shown by omission, not a badge (docs/slices/UI.md Phase 0b).
  function readinessLabel(r) {
    return { missing_info: 'Missing info', blocked: 'Blocked' }[r] || String(r);
  }

  // ---------- routing ----------

  // parseHash reads location.hash into {view, param}. Every view except
  // "workspaces" keeps its original one-segment shape: param is whatever
  // follows the first "/", decoded as one component. "workspaces" gains an
  // optional second segment (docs/slices/UI.md Phase 2, "workspaces/<id>/
  // <sub>"): the two segments are decoded independently, so an id or sub
  // that itself needed a "%2F" can never be confused with the segment
  // divider.
  function parseHash() {
    const raw = location.hash.replace(/^#/, '');
    const i = raw.indexOf('/');
    let view = i < 0 ? raw : raw.slice(0, i);
    let rest = i < 0 ? '' : raw.slice(i + 1);
    if (!VIEWS.includes(view)) { view = 'today'; rest = ''; }
    let param = '';
    if (view === 'workspaces') {
      const j = rest.indexOf('/');
      let id = j < 0 ? rest : rest.slice(0, j);
      let sub = j < 0 ? '' : rest.slice(j + 1);
      try { id = id ? decodeURIComponent(id) : ''; } catch (_) { id = ''; }
      try { sub = sub ? decodeURIComponent(sub) : ''; } catch (_) { sub = ''; }
      param = id ? id + (sub ? '/' + sub : '') : '';
    } else if (rest) {
      try { param = decodeURIComponent(rest); } catch (_) { param = ''; }
    }
    return { view, param };
  }

  // go renders view/param now and resolves once it has; the hashchange the
  // new hash causes is recognised and skipped, so nothing renders twice.
  function go(view, param) {
    const hash = '#' + view + (param ? '/' + encodeURIComponent(param) : '');
    if (location.hash !== hash) {
      state.expectHash = hash;
      location.hash = hash;
    }
    return render();
  }

  // goWorkspace is go's workspaces-specific sibling: id and sub are encoded
  // (and so decoded) as two independent path segments, never as one
  // component, so the hash reads as the documented "#workspaces/finance/
  // overview" rather than "#workspaces/finance%2Foverview".
  function goWorkspace(id, sub) {
    const hash = '#workspaces/' + encodeURIComponent(id) + (sub ? '/' + encodeURIComponent(sub) : '');
    if (location.hash !== hash) {
      state.expectHash = hash;
      location.hash = hash;
    }
    return render();
  }

  function onHashChange() {
    if (state.expectHash && location.hash === state.expectHash) {
      state.expectHash = null;
      return;
    }
    state.expectHash = null;
    render();
  }

  // render dispatches to window.views[view], the per-view file's own
  // registered renderer (view_today.js's viewToday, and so on). A view name
  // with nothing registered (which should never happen for a name in VIEWS)
  // shows an error rather than throwing.
  function render() {
    const { view, param } = parseHash();
    state.view = view;
    state.param = param;
    state.threadUI = null;
    const gen = ++state.gen;
    for (const b of document.querySelectorAll('.nav')) {
      const on = b.dataset.view === view;
      b.classList.toggle('active', on);
      if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
    }
    const container = h('div', { class: 'view view-' + view });
    replace(main, container);
    container.appendChild(h('p', { class: 'loading' }, 'Loading…'));
    updateBarTarget();
    const fn = window.views[view];
    if (typeof fn !== 'function') {
      replace(container, errorBox('Could not load ' + view, new Error('no view registered for ' + view)));
      return Promise.resolve();
    }
    return fn(container, param, gen).catch((err) => {
      if (current(gen)) replace(container, errorBox('Could not load ' + view, err));
    }).finally(() => { if (current(gen)) updateBarTarget(); });
  }

  async function openThreadAbout(type, id) {
    try {
      const res = await api.anchorThread(type, id);
      go('threads', get(res && res.thread, 'id'));
      refreshRecent();
    } catch (err) {
      toast('Could not open a thread: ' + errText(err), 'bad');
    }
  }

  // openExternal posts {type: 'open-external', source, id, url} to the
  // native side (docs/slices/UI.md U16, "View related data"'s per-source
  // open affordance). url is exactly what api.relatedDecision already
  // resolved server-side from the record's own stored field -- this page
  // never edits, guesses at or otherwise fabricates it, and it never
  // navigates anywhere by itself (there is no other way out of this page:
  // every outbound link goes through this one message). The native side is
  // the only thing that decides whether to actually hand the URL off
  // (checking a fixed host allowlist first), so a page not running inside
  // the native shell (no water message handler present) simply can't do
  // this at all -- the caller should only offer the affordance when a
  // source actually carries a url.
  function openExternal(source, id, url) {
    const wk = window.webkit;
    const handler = wk && wk.messageHandlers && wk.messageHandlers.water;
    if (!handler || typeof handler.postMessage !== 'function') {
      toast('Opening external links needs the Water app.', 'warn');
      return;
    }
    try {
      handler.postMessage({ type: 'open-external', source: String(source || ''), id: String(id || ''), url: String(url || '') });
    } catch (err) {
      toast('Could not open that: ' + errText(err), 'bad');
    }
  }

  // ---------- payload forms (staging and editing) ----------
  //
  // Shared by view_decisions.js (stageActionForm) and view_approvals.js /
  // view_drafts.js (renderApprovalDetail's Edit button), so they live here
  // rather than in either view file.

  // payloadFields decides the editable fields for one staged action: the
  // card's own payload keys when it carries one, the usual to/subject/body
  // for a mail action, or a raw JSON box otherwise.
  function payloadFields(action) {
    const p = get(action, 'Payload', 'payload');
    const fn = get(action, 'Function', 'function') || '';
    if (p && typeof p === 'object' && Object.keys(p).length) return fieldsOf(p);
    if (/\.(send_message|draft_message|draft_for_review)$/.test(fn)) {
      return [{ key: 'to', kind: 'list', value: [] }, { key: 'subject', kind: 'text', value: '' }, { key: 'body', kind: 'long', value: '' }];
    }
    return [{ key: '', kind: 'json', value: {} }];
  }

  function fieldsOf(p) {
    return Object.keys(p).map((k) => ({ key: k, kind: kindOf(p[k]), value: p[k] }));
  }

  function kindOf(v) {
    if (Array.isArray(v) && v.every((x) => typeof x === 'string')) return 'list';
    if (typeof v === 'string') return (v.length > 80 || v.includes('\n')) ? 'long' : 'text';
    return 'json';
  }

  let formSeq = 0;

  // payloadInputs builds one labelled input per field into form and
  // returns read(), which gives the payload back (throwing on bad JSON).
  function payloadInputs(form, fields) {
    const inputs = [];
    const prefix = 'f' + (++formSeq) + '-';
    fields.forEach((f, i) => {
      const fid = prefix + i;
      let input;
      if (f.kind === 'long' || f.kind === 'json') {
        input = h('textarea', { id: fid, rows: f.kind === 'json' ? 6 : 8, spellcheck: f.kind === 'json' ? 'false' : 'true' });
        input.value = f.kind === 'json' ? JSON.stringify(f.value, null, 2) : String(f.value || '');
      } else {
        input = h('input', { id: fid, type: 'text', autocomplete: 'off' });
        input.value = f.kind === 'list' ? list(f.value).join(', ') : String(f.value || '');
      }
      inputs.push({ f, input });
      const label = f.key ? f.key + (f.kind === 'list' ? ' (comma separated)' : f.kind === 'json' ? ' (JSON)' : '') : 'Payload (JSON)';
      form.appendChild(h('label', { for: fid }, label));
      form.appendChild(input);
    });
    return function read() {
      let payload = {};
      for (const { f, input } of inputs) {
        let v;
        if (f.kind === 'list') v = input.value.split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
        else if (f.kind === 'json') v = JSON.parse(input.value || 'null');
        else v = input.value;
        if (f.key) payload[f.key] = v; else payload = v;
      }
      return payload;
    };
  }

  // editForm edits a pending envelope (docs/slices/V.md D3): the old one is
  // voided and a new pending one is staged, which needs its own yes.
  // onEdited receives the new envelope.
  function editForm(env, onEdited, onCancel) {
    const form = h('div', { class: 'form' }, h('h3', null, 'Edit ' + (get(env, 'action') || '')),
      h('p', { class: 'muted small' }, 'Saving replaces this approval with a new one. Nothing is sent until you approve the new one.'));
    const read = payloadInputs(form, fieldsOf(get(env, 'payload') || {}));
    const msg = h('div');
    const save = button('Save as a new approval', async () => {
      let payload;
      try { payload = read(); } catch (err) {
        replace(msg, h('div', { class: 'error' }, 'The payload is not valid JSON: ' + err.message));
        return;
      }
      save.disabled = true;
      try {
        const res = await api.editApproval(get(env, 'id'), get(env, 'payload_hash'), payload);
        toast('Saved. The old approval was voided; review the new one.', 'warn');
        refreshCounts();
        onEdited(get(res, 'envelope') || {});
      } catch (err) {
        save.disabled = false;
        replace(msg, errorBox(err.status === 409 ? 'This approval changed or was already answered' : 'Could not save the edit', err));
      }
    }, 'primary');
    form.appendChild(msg);
    form.appendChild(h('div', { class: 'actions' }, save, button('Cancel', onCancel, 'ghost')));
    return form;
  }

  // decisionResult renders api.decideApproval's answer.
  function decisionResult(res, action) {
    const done = h('div', { class: get(res, 'error') ? 'error' : 'ok' });
    if (get(res, 'answer') === 'yes') {
      done.appendChild(h('p', null, get(res, 'executed')
        ? (isOutward(action) ? 'Approved and sent.' : 'Approved and done.')
        : 'Approved, but it did not run.'));
    } else {
      done.appendChild(h('p', null, 'Denied. Nothing was done.'));
    }
    if (get(res, 'error')) done.appendChild(h('p', null, 'Error: ' + get(res, 'error')));
    if (get(res, 'outcome_unknown')) {
      done.appendChild(h('p', { class: 'warn-text' }, 'The outcome is unknown: it may have happened. Check before asking for it again.'));
    }
    if (get(res, 'output') !== undefined) {
      done.appendChild(h('details', null, h('summary', null, 'Result'),
        h('pre', { class: 'mono' }, JSON.stringify(get(res, 'output'), null, 2))));
    }
    return done;
  }

  // ---------- Approvals/Drafts shared rendering ----------
  //
  // approvalRow and renderApprovalDetail are used by both view_approvals.js
  // and view_drafts.js (the same envelope list/detail shape, filtered
  // differently); pendingDrafts is also used by refreshCounts below.

  function approvalRow(e, selected, view) {
    const id = get(e, 'id');
    // Phase 3a's "agent-draft card"/"person-request card" origin line: the
    // decision this was staged from, or who's asking, server-built
    // (source_card_title/requested_by_name -- never guessed here), omitted
    // entirely rather than shown empty when neither resolves.
    const sourceTitle = get(e, 'source_card_title');
    const requestedByName = get(e, 'requested_by_name');
    const originLine = sourceTitle ? 'From ' + sourceTitle : requestedByName ? 'Requested by ' + requestedByName : '';
    const avatar = requestedByName ? h('span', { class: 'avatar', title: requestedByName }, get(e, 'requester_initials') || '?') : null;
    const main = h('span', { class: 'row-main' },
      h('span', { class: 'row-title' }, get(e, 'action') || id),
      originLine ? h('span', { class: 'row-origin muted' }, originLine) : null,
      // gist (the body's one-sentence summary) when there is one; the plain
      // list summary otherwise (a non-mail action, or an empty body).
      h('span', { class: 'row-sub' }, get(e, 'gist') || get(e, 'summary') || ''),
      get(e, 'risk_phrase') ? h('span', { class: 'risk-phrase' }, get(e, 'risk_phrase')) : null,
      h('span', { class: 'row-meta' },
        statusBadge(e),
        get(e, 'risk') ? badge(riskLabel(get(e, 'risk')), 'risk-' + get(e, 'risk')) : null,
        h('span', { class: 'muted' }, fmtAgo(get(e, 'created_at')))));
    return h('li', null, h('button', {
      type: 'button', class: 'row' + (selected ? ' selected' : ''), on: { click: () => go(view, id) },
    }, avatar ? h('span', { class: 'row-line' }, avatar, main) : main));
  }

  // pendingDrafts is D2-A: the pending envelopes whose action is an outward
  // message, one ?kind= call per action, merged oldest first.
  async function pendingDrafts() {
    const lists = await Promise.all(OUTWARD.map((k) => api.approvals('pending', 100, k)));
    const all = [].concat(...lists.map(list));
    all.sort((a, b) => String(get(a, 'created_at')).localeCompare(String(get(b, 'created_at'))));
    return all;
  }

  // TRAIL_STEPS mirrors approvals.Envelope.Trail's four stages
  // (docs/slices/UI.md Phase 3a): a plain row of labeled steps, the current
  // one highlighted -- text and CSS only, no images.
  const TRAIL_STEPS = [['staged', 'Staged'], ['approved', 'Approved'], ['sent', 'Sent'], ['reply', 'Reply']];

  function trailRow(stage) {
    if (!stage) return null;
    return h('div', { class: 'trail' }, TRAIL_STEPS.map(([key, label]) =>
      h('span', { class: 'trail-step' + (key === stage ? ' current' : '') }, label)));
  }

  // warningsBox is W's recipient warnings (finding 23) as a visible amber
  // box on the approval card: today Envelope.Warnings is only spoken/shown
  // via the CLI/voice read-back (internal/approvals/readback.go's
  // warningLine, folded into read_back's own text); this is that same list,
  // rendered separately so it can't be missed by skimming the read-back
  // paragraph. null when there are none (a decided envelope never carries
  // them either -- Queue.withWarnings only computes them for pending/
  // approved).
  function warningsBox(env) {
    const warnings = list(get(env, 'warnings'));
    if (!warnings.length) return null;
    return h('div', { class: 'warnings-box', role: 'alert' },
      h('strong', null, 'Warning: '), warnings.join(' '));
  }

  // requesterInfo is the person-request card's "Requested by <name>" line
  // with an initials avatar (docs/slices/UI.md Phase 3a); null for an
  // agent-drafted envelope (requested_by_name is only ever set when the
  // server resolved one).
  function requesterInfo(env) {
    const name = get(env, 'requested_by_name');
    if (!name) return null;
    return h('div', { class: 'requester' },
      h('span', { class: 'avatar', title: name }, get(env, 'requester_initials') || '?'),
      h('span', null, 'Requested by ' + name));
  }

  // requestChangesForm is the person-request card's inline note field (no
  // modal/dialog library, plain DOM): submitting calls the new
  // request-changes endpoint, which denies the original and proposes a
  // fresh reply -- onSent re-renders the card so it shows the now-denied
  // original.
  function requestChangesForm(env, requesterName, onSent, onCancel) {
    const form = h('div', { class: 'form' }, h('h3', null, 'Request changes'),
      h('p', { class: 'muted small' }, 'Sends your notes back to ' + requesterName + '.'));
    const note = h('textarea', { rows: 4, placeholder: 'What needs to change?' });
    const msg = h('div');
    const send = button('Send note', async () => {
      const text = note.value.trim();
      if (!text) {
        replace(msg, h('div', { class: 'error' }, 'A note is required.'));
        return;
      }
      send.disabled = true;
      try {
        await api.requestChanges(get(env, 'id'), text);
        toast('Sent. The original was denied; your note is on its way.', 'warn');
        refreshCounts();
        onSent();
      } catch (err) {
        send.disabled = false;
        replace(msg, errorBox(err.status === 409 ? 'This could not be sent' : 'Could not send your note', err));
      }
    }, 'primary');
    form.appendChild(note);
    form.appendChild(msg);
    form.appendChild(h('div', { class: 'actions' }, send, button('Cancel', onCancel, 'ghost')));
    return form;
  }

  async function renderApprovalDetail(pane, id, gen, view) {
    let env;
    try {
      env = await api.approval(id);
    } catch (err) {
      if (current(gen)) replace(pane, errorBox('Could not load approval', err));
      return;
    }
    if (!current(gen)) return;
    const status = get(env, 'status');
    const action = get(env, 'action');
    const out = h('div', { class: 'result' });
    const expires = get(env, 'expires_at');
    const pending = status === 'pending';

    const payload = get(env, 'payload') || {};
    const pl = h('dl', { class: 'kv' });
    for (const k of Object.keys(payload)) {
      const v = payload[k];
      pl.appendChild(h('dt', null, k));
      pl.appendChild(h('dd', null, typeof v === 'string' ? v : JSON.stringify(v, null, 2)));
    }
    const refs = list(get(env, 'evidence_refs'));

    const requesterName = get(env, 'requested_by_name');
    const isPersonRequest = get(env, 'origin_kind') === 'person_request';

    const actions = h('div', { class: 'actions' });
    const threadBtn = button('Open a thread about this', () => openThreadAbout('approval', id));
    let reset = () => replace(actions, threadBtn);
    if (pending) {
      const approve = button('Approve…', () => {
        replace(actions,
          h('span', { class: 'confirm-q' }, isOutward(action) ? 'Send this now, exactly as read back above?' : 'Run this now, exactly as read back above?'),
          armed(button(isOutward(action) ? 'Yes, send it' : 'Yes, approve and run', () => decide('yes'), 'primary')),
          button('Not yet', () => reset(), 'ghost'));
      }, 'primary');
      const deny = button('Deny', () => decide('no'), 'danger');
      // U11 (kept exactly as before, both branches): Approve… -> the exact
      // read-back -> "Yes, send it"/"Yes, approve and run", armed only
      // after 600ms (armed()). The only thing that changes per card is the
      // middle button: a person-request card's own "Request changes"
      // (U15/Phase 3a) in place of Edit, since editing someone else's
      // request payload isn't the point -- sending them a note is.
      if (isPersonRequest) {
        const requestChanges = button('Request changes', () => {
          replace(out, requestChangesForm(env, requesterName || 'them', () => {
            if (current(gen)) renderApprovalDetail(pane, id, gen, view);
          }, () => replace(out)));
        }, 'secondary', { title: 'Sends your notes back to ' + (requesterName || 'them') });
        reset = () => replace(actions, approve, requestChanges, deny, threadBtn);
      } else {
        const edit = button('Edit', () => {
          replace(out, editForm(env, (next) => go(view, get(next, 'id')), () => replace(out)));
        });
        reset = () => replace(actions, approve, edit, deny, threadBtn);
      }
    }
    reset();

    async function decide(reply) {
      for (const b of actions.querySelectorAll('button')) b.disabled = true;
      try {
        const res = await api.decideApproval(id, get(env, 'payload_hash'), reply);
        replace(out, decisionResult(res, action));
        replace(actions, threadBtn);
        refreshCounts();
      } catch (err) {
        if (err.status === 409) {
          toast('This approval changed since you opened it. Showing the current version.', 'warn');
          if (current(gen)) renderApprovalDetail(pane, id, gen, view);
          return;
        }
        for (const b of actions.querySelectorAll('button')) b.disabled = false;
        replace(out, errorBox('Could not record your answer', err));
      }
    }

    const sourceCardTitle = get(env, 'source_card_title');

    replace(pane,
      h('article', { class: 'approval' },
        h('header', { class: 'card-head' },
          h('h2', null, action || id),
          h('div', { class: 'row-meta' },
            statusBadge(env),
            get(env, 'risk') ? badge(riskLabel(get(env, 'risk')), 'risk-' + get(env, 'risk')) : null,
            h('span', { class: 'muted' }, 'Created ' + fmtDate(get(env, 'created_at'))),
            pending && expires ? h('span', { class: 'muted' }, 'Expires ' + fmtAgo(expires)) : null)),
        sourceCardTitle ? h('p', { class: 'muted small' }, 'From ' + sourceCardTitle) : null,
        requesterInfo(env),
        trailRow(get(env, 'trail')),
        h('section', { class: 'card-sec' },
          h('h3', null, 'Read-back'),
          h('p', { class: 'muted small' }, 'Written by Water from the exact payload this approval is bound to.'),
          h('div', { class: 'readback' }, get(env, 'read_back') || '')),
        warningsBox(env),
        get(env, 'reason') ? h('p', { class: 'muted' }, 'Reason: ' + get(env, 'reason')) : null,
        actions,
        out,
        h('details', { class: 'card-sec' }, h('summary', null, 'Payload and evidence'),
          h('figure', { class: 'untrusted' },
            h('figcaption', null, 'Payload (may be drafted from external content; shown as text)'), pl),
          refs.length ? h('ul', { class: 'refs' }, refs.map((r) => h('li', { class: 'mono' }, String(r)))) : null)));
  }

  // ---------- Threads shared rendering ----------
  //
  // messageEl and approvalNotice are used both by view_threads.js's own
  // renderThreadDetail and by sendToThread below (the bar posts into
  // whichever thread is open, regardless of which view is on screen).

  function messageEl(role, text, meta) {
    const body = h('div', { class: 'msg-text' }, text || '');
    const el = h('div', { class: 'msg ' + (role === 'ceo' ? 'from-ceo' : 'from-twin') },
      h('div', { class: 'msg-meta' }, role === 'ceo' ? 'You' : 'Twin', meta ? ' · ' + meta : ''),
      body);
    return { el, body };
  }

  function approvalNotice(n) {
    return h('div', { class: 'notice' },
      h('strong', null, 'Approval needed: '), n.action || '',
      h('div', { class: 'readback' }, n.readBack || ''),
      button('Review', () => go(isOutward(n.action) ? 'drafts' : 'approvals', n.id), 'primary'));
  }

  // ---------- the bottom bar ----------

  const bar = {
    input: document.getElementById('bar-input'),
    send: document.getElementById('bar-send'),
    stop: document.getElementById('bar-stop'),
    mic: document.getElementById('mic'),
    status: document.getElementById('bar-status'),
    target: document.getElementById('bar-target'),
  };

  function barStatus(text) { bar.status.textContent = text || ''; }

  // openThreadID is the thread the bar talks to: the one showing in the
  // Threads view, or '' (the bar then starts a new free-standing thread).
  function openThreadID() {
    const ui = state.threadUI;
    return state.view === 'threads' && ui && ui.id === state.param ? ui.id : '';
  }

  function updateBarTarget() {
    const ui = state.threadUI;
    bar.target.textContent = openThreadID() && ui ? 'In thread: ' + ui.title : 'No thread open: sending starts a new thread.';
  }

  function titleFrom(text) {
    const one = text.replace(/\s+/g, ' ').trim();
    return one.length > 60 ? one.slice(0, 59) + '…' : one;
  }

  function setBarBusy(busy) {
    bar.send.disabled = busy;
    bar.input.disabled = busy;
    bar.stop.hidden = !busy;
  }

  async function barSubmit() {
    const text = bar.input.value.trim();
    if (!text || state.streaming) return;
    setBarBusy(true);
    let id = openThreadID();
    if (!id) {
      try {
        id = get(await api.createThread(titleFrom(text)), 'id');
      } catch (err) {
        setBarBusy(false);
        toast('Could not start a thread: ' + errText(err), 'bad');
        return;
      }
    }
    bar.input.value = '';
    if (openThreadID() !== id) await go('threads', id);
    await sendToThread(id, text);
    refreshRecent();
  }

  // sendToThread posts text to thread id (api.postThreadMessage,
  // the one turn path) and draws the reply as it streams, when that thread
  // is on screen. On done the thread is re-fetched, so the view shows
  // exactly what the daemon stored.
  async function sendToThread(id, text) {
    const ui = state.threadUI && state.threadUI.id === id ? state.threadUI : null;
    const log = ui ? ui.log : h('div');
    const notices = ui ? ui.notices : h('div');
    const emptyEl = log.querySelector('.empty');
    if (emptyEl) emptyEl.remove();
    log.appendChild(messageEl('ceo', text, 'now').el);
    const reply = messageEl('twin', '', 'thinking…');
    reply.el.classList.add('pending');
    log.appendChild(reply.el);
    if (ui) reply.el.scrollIntoView({ block: 'end' });
    setBarBusy(true);
    barStatus('');
    state.streaming = { threadId: id, taskId: '' };
    let failed = '';
    try {
      await api.postThreadMessage(id, text, (taskId) => { state.streaming.taskId = taskId; }, (ev) => {
        switch (ev.kind) {
          case 'queued':
            barStatus('Waiting for another turn to finish…');
            break;
          case 'delta':
            barStatus('');
            reply.body.textContent += ev.text || '';
            break;
          case 'approval_required': {
            const n = { id: ev.approval_id, action: ev.action, readBack: ev.read_back };
            const arr = state.threadNotices.get(id) || [];
            arr.push(n);
            state.threadNotices.set(id, arr);
            notices.appendChild(approvalNotice(n));
            refreshCounts();
            break;
          }
          case 'done':
            if (!reply.body.textContent && ev.text) reply.body.textContent = ev.text;
            break;
          case 'error':
            failed = ev.error || ev.text || 'the turn failed';
            break;
          default:
            break; // ack, sentence, tool steps and anything newer: ignored
        }
      });
    } catch (err) {
      failed = errText(err);
    } finally {
      state.streaming = null;
      setBarBusy(false);
      barStatus('');
    }
    if (failed) {
      reply.el.classList.remove('pending');
      reply.el.classList.add('failed');
      reply.body.textContent = (reply.body.textContent ? reply.body.textContent + '\n\n' : '') + 'Error: ' + failed;
      return;
    }
    if (state.view === 'threads' && state.param === id) render();
  }

  function setupBar() {
    bar.send.addEventListener('click', barSubmit);
    bar.input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); barSubmit(); }
    });
    bar.stop.addEventListener('click', async () => {
      if (state.streaming && state.streaming.taskId) {
        try { await api.cancelTask(state.streaming.taskId); } catch (_) { /* the turn may already be done */ }
      }
    });
    updateBarTarget();
  }

  // setupMic wires the bar's hold-to-talk mic. The page never runs a voice
  // turn itself: pressing posts {type: 'mic-down', thread} to the native
  // side, releasing posts {type: 'mic-up'}. The native side listens, sends
  // the transcript as a voice turn into that thread, speaks the reply, and
  // then calls window.water.refresh().
  function setupMic() {
    const wk = window.webkit;
    const handler = wk && wk.messageHandlers && wk.messageHandlers.water;
    if (!handler || typeof handler.postMessage !== 'function') {
      bar.mic.hidden = true;
      return;
    }
    bar.mic.hidden = false;
    const post = (msg) => {
      try { handler.postMessage(msg); return true; } catch (err) { toast('Voice is unavailable: ' + errText(err), 'bad'); return false; }
    };
    let holding = false; // the button is held down right now
    let sentDown = false; // this hold reached the native side

    async function down() {
      if (holding || state.streaming) return;
      holding = true;
      sentDown = false;
      bar.mic.classList.add('holding');
      let id = openThreadID();
      if (!id) {
        barStatus('Starting a new thread…');
        try {
          id = get(await api.createThread('Voice conversation'), 'id');
        } catch (err) {
          holding = false;
          bar.mic.classList.remove('holding');
          barStatus('');
          toast('Could not start a thread: ' + errText(err), 'bad');
          return;
        }
        await go('threads', id);
        refreshRecent();
      }
      if (!holding) { barStatus(''); return; } // released before the thread was ready
      if (!THREAD_ID.test(id)) { up(); return; }
      if (post({ type: 'mic-down', thread: id })) {
        sentDown = true;
        barStatus('Listening… release to send.');
      }
    }

    function up() {
      if (!holding) return;
      holding = false;
      bar.mic.classList.remove('holding');
      if (!sentDown) return;
      sentDown = false;
      if (post({ type: 'mic-up' })) barStatus('Sent by voice. The reply is spoken, then appears in this thread.');
    }

    bar.mic.addEventListener('pointerdown', (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      try { bar.mic.setPointerCapture(e.pointerId); } catch (_) { /* not capturable */ }
      down();
    });
    for (const ev of ['pointerup', 'pointercancel', 'lostpointercapture']) bar.mic.addEventListener(ev, up);
    bar.mic.addEventListener('keydown', (e) => {
      if ((e.key === ' ' || e.key === 'Enter') && !e.repeat) { e.preventDefault(); down(); }
    });
    bar.mic.addEventListener('keyup', (e) => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); up(); } });
    bar.mic.addEventListener('blur', up);
    window.addEventListener('blur', up);
  }

  // ---------- counts, recent threads, boot ----------

  const RECENT = 6;

  function renderRecent(threads) {
    const ul = document.getElementById('recent-threads');
    const items = list(threads).slice(0, RECENT);
    replace(ul, items.map((t) => {
      const id = get(t, 'id');
      return h('li', null, h('button', {
        type: 'button', class: 'recent-item' + (state.view === 'threads' && state.param === id ? ' active' : ''),
        title: get(t, 'title') || 'Untitled',
        on: { click: () => go('threads', id) },
      }, get(t, 'title') || 'Untitled'));
    }));
    if (!items.length) ul.appendChild(h('li', { class: 'muted small' }, 'None yet'));
  }

  async function refreshRecent() {
    try { renderRecent(await api.threads()); } catch (_) { /* best-effort */ }
  }

  async function refreshCounts() {
    try {
      const [t, pending, drafts] = await Promise.all([api.today(), api.approvals('pending', 500), pendingDrafts()]);
      setCount('today', list(get(t, 'needs_you')).length);
      setCount('approvals', list(pending).length);
      setCount('drafts', drafts.length);
    } catch (_) { /* counts are best-effort */ }
  }

  function boot() {
    for (const b of document.querySelectorAll('.nav')) {
      b.addEventListener('click', () => go(b.dataset.view));
    }
    document.getElementById('refresh').addEventListener('click', () => { render(); refreshCounts(); refreshRecent(); });
    window.addEventListener('hashchange', onHashChange);
    setupBar();
    setupMic();
    render();
    refreshCounts();
    refreshRecent();
    // view_workspaces.js registers this to populate the sidebar's
    // Workspaces disclosure; it is loaded by the time boot runs (see the
    // file header comment), but the guard keeps boot() safe even if a
    // future build ever ships without that file.
    if (typeof window.views._workspaceNav === 'function') window.views._workspaceNav();
    setInterval(() => { refreshCounts(); refreshRecent(); }, 60000);
    // For the native shell: open a view (a tapped notification, the HUD's
    // Edit), or refresh after a held-mic voice turn finished.
    window.water = Object.freeze({
      open: (view, param) => {
        if (!VIEWS.includes(view)) return;
        if (view === 'workspaces' && param) {
          const parts = String(param).split('/');
          goWorkspace(parts[0], parts[1] || DEFAULT_WORKSPACE_SUB);
          return;
        }
        go(view, param ? String(param) : '');
      },
      refresh: () => { barStatus(''); render(); refreshCounts(); refreshRecent(); },
    });
  }

  // window.views is the per-view render-function registry: each view file
  // (loaded after this one; see index.html) sets window.views[<hash view
  // name>] to its own async renderer(container, param, gen). Initialized
  // here (not left for the first view file to create) so render() above can
  // always safely read from it.
  window.views = window.views || {};

  // window.appShared is every helper more than one view file needs. Every
  // view file reads it, never the reverse: nothing in this file calls into
  // window.views except through render()'s dispatch and boot()'s
  // _workspaceNav hook above.
  window.appShared = {
    state, DEFAULT_WORKSPACE_SUB,
    toast, errText, errorBox, badge, armed, button, header, empty, setCount, current,
    isOutward, statusLabel, statusBadge, riskLabel, actionLabel, dueBadge, extGlyph, kindGlyph,
    decisionPriorityClass, deadlineDays, priorityClass, readinessLabel,
    go, goWorkspace, render, openThreadAbout, openExternal,
    payloadFields, fieldsOf, kindOf, payloadInputs, editForm, decisionResult,
    approvalRow, renderApprovalDetail, pendingDrafts,
    messageEl, approvalNotice, updateBarTarget, renderRecent,
    refreshCounts, refreshRecent,
  };

  // boot() needs every view file's window.views[...] entry to already be
  // registered. defer guarantees every script here runs before
  // DOMContentLoaded fires, so waiting for that event (rather than calling
  // boot() straight away, as this file used to when it held every view
  // itself) is what makes the load order in index.html safe.
  document.addEventListener('DOMContentLoaded', boot);
})();
