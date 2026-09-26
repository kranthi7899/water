// app.js: the workspace UI's views (Today, Decisions, Drafts, Approvals,
// Threads, Meetings) and the bottom bar. Rendering goes through dom.js only;
// every server string is a text node. Nothing here executes an outward
// action by itself: a decision card is staged into a PENDING approval
// envelope, and only an explicit, separate confirm click answers that
// envelope "yes", through the same approval-decision endpoint every other
// client uses (api.js). Editing an envelope voids it and stages a new one,
// which again needs its own yes.
'use strict';

(function () {
  const { h, replace, get, list, fmtDate, fmtTime, fmtAgo, untrusted } = window.dom;
  const api = window.api;

  const main = document.getElementById('main');
  const toastEl = document.getElementById('toast');
  const VIEWS = ['today', 'decisions', 'drafts', 'approvals', 'threads', 'meetings'];
  // Outward-message actions (docs/slices/V.md D2): what Drafts lists, and
  // the actions whose "executed" reads as "sent".
  const OUTWARD = ['gmail.send_message', 'twinlink.send_message'];
  // The shape the store mints and the native mic handler accepts.
  const THREAD_ID = /^thr_[0-9a-f]+$/;

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

  // ---------- routing ----------

  function parseHash() {
    const raw = location.hash.replace(/^#/, '');
    const i = raw.indexOf('/');
    let view = i < 0 ? raw : raw.slice(0, i);
    let param = '';
    if (i >= 0) {
      try { param = decodeURIComponent(raw.slice(i + 1)); } catch (_) { param = ''; }
    }
    if (!VIEWS.includes(view)) { view = 'today'; param = ''; }
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

  function onHashChange() {
    if (state.expectHash && location.hash === state.expectHash) {
      state.expectHash = null;
      return;
    }
    state.expectHash = null;
    render();
  }

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
    const fn = { today: viewToday, decisions: viewDecisions, drafts: viewDrafts, approvals: viewApprovals, threads: viewThreads, meetings: viewMeetings }[view];
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

  // ---------- Today ----------

  async function viewToday(c, _param, gen) {
    const t = await api.today();
    if (!current(gen)) return;
    const items = list(get(t, 'needs_you'));
    const schedule = list(get(t, 'schedule'));
    setCount('today', items.length);

    const needs = h('section', { class: 'panel' }, h('h2', null, 'Needs you'));
    if (!items.length) needs.appendChild(empty('Nothing needs you right now.'));
    const ul = h('ul', { class: 'rows' });
    for (const it of items) {
      const kind = get(it, 'Kind', 'kind');
      const id = get(it, 'ID', 'id');
      const sev = get(it, 'Severity', 'severity');
      const deadline = get(it, 'Deadline', 'deadline');
      const readiness = get(it, 'Readiness', 'readiness');
      ul.appendChild(h('li', null, h('button', {
        type: 'button', class: 'row',
        on: { click: () => go(kind === 'approval' ? 'approvals' : 'decisions', id) },
      },
      h('span', { class: 'row-main' },
        h('span', { class: 'row-title' }, get(it, 'Title', 'title') || id),
        h('span', { class: 'row-meta' },
          badge(kind === 'approval' ? 'Approval' : 'Decision', kind),
          sev ? badge('Severity ' + sev, sev >= 3 ? 'hot' : '') : null,
          readiness ? badge(readinessLabel(readiness), 'ready-' + readiness) : null,
          get(it, 'Untrusted', 'untrusted') ? badge('External content', 'warn') : null,
          deadline ? h('span', { class: 'muted' }, 'Due ' + fmtDate(deadline)) : null,
          kind === 'approval' ? h('span', { class: 'muted' }, 'Waiting ' + fmtAgo(get(it, 'CreatedAt', 'created_at')).replace(' ago', '')) : null)))));
    }
    needs.appendChild(ul);

    const sched = h('section', { class: 'panel' }, h('h2', null, 'Schedule'));
    if (!schedule.length) sched.appendChild(empty('No events today.'));
    const sl = h('ul', { class: 'schedule' });
    for (const e of schedule) {
      const when = get(e, 'all_day') ? 'All day' : fmtTime(get(e, 'start_at')) + ' – ' + fmtTime(get(e, 'end_at'));
      sl.appendChild(h('li', null,
        h('span', { class: 'when' }, when),
        h('span', { class: 'what' }, get(e, 'title') || '(untitled)',
          get(e, 'location') ? h('span', { class: 'muted where' }, get(e, 'location')) : null)));
    }
    sched.appendChild(sl);

    const day = new Intl.DateTimeFormat(undefined, { weekday: 'long', month: 'long', day: 'numeric' }).format(new Date());
    replace(c, header('Today', day), h('div', { class: 'stack' }, needs, sched));
  }

  function readinessLabel(r) {
    return { ready: 'Ready', missing_info: 'Missing info', blocked: 'Blocked' }[r] || String(r);
  }

  // ---------- payload forms (staging and editing) ----------

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

  // ---------- Decisions ----------

  async function viewDecisions(c, param, gen) {
    const cards = list(await api.decisions());
    if (!current(gen)) return;
    setCount('decisions', cards.length);
    const body = h('div', { class: 'cards' });
    if (!cards.length) body.appendChild(empty('No open decisions.'));
    let focus = null;
    for (const card of cards) {
      const el = decisionCard(card, gen);
      if (param && get(card, 'ID', 'id') === param) { el.classList.add('focus'); focus = el; }
      body.appendChild(el);
    }
    replace(c, header('Decisions', 'Approving a card is two steps: stage it, read exactly what will happen, then confirm. Nothing is sent before you confirm.'), body);
    if (focus) focus.scrollIntoView({ block: 'start' });
  }

  function decisionCard(card, gen) {
    const id = get(card, 'ID', 'id');
    const isUntrusted = Boolean(get(card, 'Untrusted', 'untrusted'));
    const sev = get(card, 'Severity', 'severity');
    const readiness = get(card, 'Readiness', 'readiness');
    const deadline = get(card, 'Deadline', 'deadline');
    const cs = get(card, 'card_state');
    const stagedID = cs && get(cs, 'status') === 'staged' ? get(cs, 'approval_id') : '';
    const stagedStatus = stagedID ? get(cs, 'approval_status') : '';
    const awaiting = stagedID && stagedStatus === 'pending';
    const status = h('div', { class: 'card-status' });

    const el = h('article', { class: 'card' },
      h('header', { class: 'card-head' },
        h('h2', null, get(card, 'Lead', 'lead') || get(card, 'Question', 'question') || id),
        h('div', { class: 'row-meta' },
          awaiting ? badge('Staged, awaiting your yes', 'status-pending') : null,
          sev ? badge('Severity ' + sev, sev >= 3 ? 'hot' : '') : null,
          readiness ? badge(readinessLabel(readiness), 'ready-' + readiness) : null,
          isUntrusted ? badge('External content', 'warn') : null,
          deadline ? h('span', { class: 'muted' }, 'Due ' + fmtDate(deadline)) : null)));

    const question = get(card, 'Question', 'question');
    if (question) el.appendChild(h('p', { class: 'question' }, question));

    const evidence = list(get(card, 'Evidence', 'evidence'));
    if (evidence.length) {
      const ul = h('ul', { class: 'evidence' });
      for (const ev of evidence) {
        ul.appendChild(h('li', null,
          h('span', { class: 'ev-text' }, get(ev, 'Text', 'text') || ''),
          h('span', { class: 'ev-source' }, get(ev, 'Source', 'source') || '')));
      }
      el.appendChild(h('section', { class: 'card-sec' + (isUntrusted ? ' untrusted-sec' : '') },
        h('h3', null, isUntrusted ? 'Evidence (includes external content, shown as text)' : 'Evidence'), ul));
    }

    const options = list(get(card, 'Options', 'options'));
    if (options.length) {
      const ul = h('ul', { class: 'options' });
      for (const o of options) {
        ul.appendChild(h('li', null, h('strong', null, get(o, 'Label', 'label') || ''),
          get(o, 'Consequences', 'consequences') ? h('span', { class: 'muted' }, ' — ' + get(o, 'Consequences', 'consequences')) : null));
      }
      el.appendChild(h('section', { class: 'card-sec' }, h('h3', null, 'Options'), ul));
    }

    const gaps = list(get(card, 'Gaps', 'gaps'));
    if (gaps.length) {
      const ul = h('ul', { class: 'gaps' });
      for (const g of gaps) ul.appendChild(h('li', null, String(g)));
      el.appendChild(h('section', { class: 'card-sec' }, h('h3', null, 'Gaps'), ul));
    }

    const rec = get(card, 'Recommendation', 'recommendation');
    el.appendChild(h('section', { class: 'card-sec' }, h('h3', null, 'Recommendation'),
      h('p', { class: rec ? '' : 'muted' }, rec || 'None: the evidence does not support one.')));

    const stageArea = h('div', { class: 'stage-area' });
    const actionRow = h('div', { class: 'actions' });
    if (!awaiting) {
      // Staged actions: each actionable one can be prepared and staged.
      for (const a of list(get(card, 'StagedActions', 'staged_actions'))) {
        const fn = get(a, 'Function', 'function') || '';
        if (get(a, 'Actionable', 'actionable')) {
          actionRow.appendChild(button('Prepare ' + fn, () => {
            replace(stageArea, stageForm(id, a, status, gen));
          }, 'primary'));
        } else {
          actionRow.appendChild(h('span', { class: 'muted not-granted', title: 'The manifest does not grant this at level A yet' }, fn + ' (not granted)'));
        }
      }
    }
    actionRow.appendChild(button('Open a thread about this', () => openThreadAbout('decision', id)));
    actionRow.appendChild(button('Reject', () => replace(stageArea, dismissForm(id, el, status)), 'ghost'));
    el.appendChild(actionRow);
    el.appendChild(stageArea);
    el.appendChild(status);

    if (awaiting) {
      // Staged earlier (this session or another): show that envelope's
      // read-back and the confirm step, never a second staging.
      replace(status, h('p', { class: 'muted' }, 'Loading the staged approval…'));
      api.approval(stagedID).then((env) => {
        if (current(gen)) replace(status, stagedPanel(env, status, gen, false));
      }).catch((err) => {
        if (current(gen)) replace(status, errorBox('Could not load the staged approval', err));
      });
    } else if (stagedID && stagedStatus) {
      status.appendChild(h('p', { class: 'muted small' },
        stagedStatus === 'executed'
          ? 'Staged earlier, approved and done. You can stage it again.'
          : 'Staged earlier; that approval was ' + stagedStatus + '. You can stage it again.'));
    }
    return el;
  }

  // stagedPanel is the second half of a card's approve (D3): the staged
  // envelope's code-built read-back and the confirm. justStaged means the
  // CEO clicked Stage a moment ago, so one more deliberate click confirms;
  // a card found already staged on load asks "Approve…" then "Yes".
  function stagedPanel(env, status, gen, justStaged) {
    const id = get(env, 'id');
    const action = get(env, 'action');
    const out = h('div');
    const actions = h('div', { class: 'actions' });
    const panel = h('div', { class: 'ok staged' },
      h('p', null, justStaged
        ? 'Staged. Nothing has been sent. Read exactly what will happen, then confirm.'
        : 'Staged, awaiting your yes. Nothing has been sent.'),
      h('div', { class: 'readback' }, get(env, 'read_back') || ''),
      actions, out);

    // The confirm button starts disabled briefly so a double-click on the
    // button it replaced can't land on it: approval stays two deliberate clicks.
    const yes = () => armed(button(isOutward(action) ? 'Yes, send it' : 'Yes, approve and run', () => decide('yes'), 'primary'));
    const edit = button('Edit', () => {
      replace(out, editForm(env, (next) => replace(status, stagedPanel(next, status, gen, true)), () => replace(out)));
    });
    const openBtn = button('Open in Approvals', () => go('approvals', id), 'ghost');
    function reset() {
      if (justStaged) replace(actions, yes(), edit, openBtn);
      else {
        const approve = button('Approve…', () => {
          replace(actions, h('span', { class: 'confirm-q' }, 'Run this now, exactly as read back above?'), yes(), button('Not yet', reset, 'ghost'));
        }, 'primary');
        replace(actions, approve, edit, openBtn);
      }
    }
    async function decide(reply) {
      for (const b of actions.querySelectorAll('button')) b.disabled = true;
      try {
        const res = await api.decideApproval(id, get(env, 'payload_hash'), reply);
        replace(actions);
        replace(out, decisionResult(res, action));
        refreshCounts();
      } catch (err) {
        if (err.status === 409) {
          toast('This approval changed since you opened it. Showing the current version.', 'warn');
          if (current(gen)) render();
          return;
        }
        for (const b of actions.querySelectorAll('button')) b.disabled = false;
        replace(out, errorBox('Could not record your answer', err));
      }
    }
    reset();
    return panel;
  }

  function stageForm(cardId, action, status, gen) {
    const fn = get(action, 'Function', 'function') || '';
    const form = h('div', { class: 'form' }, h('h3', null, 'Prepare ' + fn));
    const read = payloadInputs(form, payloadFields(action));
    const submit = button('Stage for approval', async () => {
      let payload;
      try { payload = read(); } catch (err) {
        replace(status, h('div', { class: 'error' }, 'The payload is not valid JSON: ' + err.message));
        return;
      }
      submit.disabled = true;
      try {
        const res = await api.stageDecision(cardId, fn, payload);
        replace(form);
        replace(status, stagedPanel(get(res, 'envelope') || {}, status, gen, get(res, 'status') !== 'already_staged'));
        refreshCounts();
      } catch (err) {
        submit.disabled = false;
        replace(status, errorBox('Staging refused', err));
      }
    }, 'primary');
    form.appendChild(h('div', { class: 'actions' }, submit, button('Cancel', () => { replace(form); replace(status); }, 'ghost')));
    return form;
  }

  // dismissForm is Reject (D3): the card is dismissed, with an optional
  // reason. It never touches an envelope already staged from it.
  function dismissForm(cardId, cardEl, status) {
    const input = h('input', { type: 'text', placeholder: 'Reason (optional)', maxlength: 1000, autocomplete: 'off' });
    const form = h('div', { class: 'form' }, h('label', null, 'Reject this decision?'), input);
    const confirm = button('Reject', async () => {
      confirm.disabled = true;
      try {
        await api.dismissDecision(cardId, input.value.trim());
        cardEl.classList.add('gone');
        replace(cardEl, h('p', { class: 'muted' }, 'Rejected.'));
        refreshCounts();
      } catch (err) {
        confirm.disabled = false;
        replace(status, errorBox('Could not reject', err));
      }
    }, 'danger');
    form.appendChild(h('div', { class: 'actions' }, confirm, button('Keep', () => replace(form), 'ghost')));
    return form;
  }

  // ---------- Approvals and Drafts ----------

  function approvalRow(e, selected, view) {
    const id = get(e, 'id');
    return h('li', null, h('button', {
      type: 'button', class: 'row' + (selected ? ' selected' : ''), on: { click: () => go(view, id) },
    },
    h('span', { class: 'row-main' },
      h('span', { class: 'row-title' }, get(e, 'action') || id),
      h('span', { class: 'row-sub' }, get(e, 'summary') || ''),
      h('span', { class: 'row-meta' },
        statusBadge(e),
        get(e, 'risk') ? badge('Risk ' + get(e, 'risk'), 'risk-' + get(e, 'risk')) : null,
        h('span', { class: 'muted' }, fmtAgo(get(e, 'created_at')))))));
  }

  async function viewApprovals(c, param, gen) {
    const tabs = h('div', { class: 'tabs', role: 'tablist' });
    for (const [key, label] of [['pending', 'Pending'], ['decided', 'Decided']]) {
      tabs.appendChild(h('button', {
        type: 'button', role: 'tab', class: 'tab' + (state.approvalTab === key ? ' active' : ''),
        'aria-selected': state.approvalTab === key ? 'true' : 'false',
        on: { click: () => { state.approvalTab = key; render(); } },
      }, label));
    }
    const listPane = h('div', { class: 'list-pane' }, tabs);
    const detail = h('div', { class: 'detail-pane' });
    replace(c, header('Approvals', 'Every outward action waits here for your yes.'), h('div', { class: 'split' }, listPane, detail));

    const [envs] = await Promise.all([
      api.approvals(state.approvalTab, 100),
      param ? renderApprovalDetail(detail, param, gen, 'approvals') : Promise.resolve(replace(detail, empty('Select an approval to review it.'))),
    ]);
    if (!current(gen)) return;
    const items = list(envs);
    if (state.approvalTab === 'pending') setCount('approvals', items.length);
    const ul = h('ul', { class: 'rows' });
    if (!items.length) listPane.appendChild(empty(state.approvalTab === 'pending' ? 'No approvals waiting.' : 'Nothing decided yet.'));
    for (const e of items) ul.appendChild(approvalRow(e, get(e, 'id') === param, 'approvals'));
    listPane.appendChild(ul);
  }

  // pendingDrafts is D2-A: the pending envelopes whose action is an outward
  // message, one ?kind= call per action, merged oldest first.
  async function pendingDrafts() {
    const lists = await Promise.all(OUTWARD.map((k) => api.approvals('pending', 100, k)));
    const all = [].concat(...lists.map(list));
    all.sort((a, b) => String(get(a, 'created_at')).localeCompare(String(get(b, 'created_at'))));
    return all;
  }

  async function viewDrafts(c, param, gen) {
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    replace(c, header('Drafts', 'Messages Water has written for you, waiting for your yes before they are sent. Gmail drafts saved straight to Gmail stay there and are not listed.'),
      h('div', { class: 'split' }, listPane, detail));
    const [items] = await Promise.all([
      pendingDrafts(),
      param ? renderApprovalDetail(detail, param, gen, 'drafts') : Promise.resolve(replace(detail, empty('Select a draft to read it before it is sent.'))),
    ]);
    if (!current(gen)) return;
    setCount('drafts', items.length);
    if (!items.length) listPane.appendChild(empty('No drafts waiting to be sent.'));
    const ul = h('ul', { class: 'rows' });
    for (const e of items) ul.appendChild(approvalRow(e, get(e, 'id') === param, 'drafts'));
    listPane.appendChild(ul);
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
      const edit = button('Edit', () => {
        replace(out, editForm(env, (next) => go(view, get(next, 'id')), () => replace(out)));
      });
      const deny = button('Deny', () => decide('no'), 'danger');
      reset = () => replace(actions, approve, edit, deny, threadBtn);
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

    replace(pane,
      h('article', { class: 'approval' },
        h('header', { class: 'card-head' },
          h('h2', null, action || id),
          h('div', { class: 'row-meta' },
            statusBadge(env),
            get(env, 'risk') ? badge('Risk ' + get(env, 'risk'), 'risk-' + get(env, 'risk')) : null,
            get(env, 'origin') ? badge('Origin ' + get(env, 'origin')) : null,
            h('span', { class: 'muted' }, 'Created ' + fmtDate(get(env, 'created_at'))),
            pending && expires ? h('span', { class: 'muted' }, 'Expires ' + fmtAgo(expires)) : null)),
        h('section', { class: 'card-sec' },
          h('h3', null, 'Read-back'),
          h('p', { class: 'muted small' }, 'Written by Water from the exact payload this approval is bound to.'),
          h('div', { class: 'readback' }, get(env, 'read_back') || '')),
        get(env, 'reason') ? h('p', { class: 'muted' }, 'Reason: ' + get(env, 'reason')) : null,
        actions,
        out,
        h('details', { class: 'card-sec' }, h('summary', null, 'Payload and evidence'),
          h('figure', { class: 'untrusted' },
            h('figcaption', null, 'Payload (may be drafted from external content; shown as text)'), pl),
          refs.length ? h('ul', { class: 'refs' }, refs.map((r) => h('li', { class: 'mono' }, String(r)))) : null)));
  }

  // ---------- Threads ----------

  async function viewThreads(c, param, gen) {
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    const newBtn = button('New thread', () => {
      const input = h('input', { type: 'text', placeholder: 'Title (optional)', maxlength: 200, autocomplete: 'off' });
      const create = async () => {
        try {
          const t = await api.createThread(input.value.trim());
          go('threads', get(t, 'id'));
          refreshRecent();
        } catch (err) { toast('Could not create a thread: ' + errText(err), 'bad'); }
      };
      input.addEventListener('keydown', (e) => { if (e.key === 'Enter') create(); });
      replace(newArea, h('div', { class: 'form inline' }, input, button('Create', create, 'primary'), button('Cancel', () => replace(newArea), 'ghost')));
      input.focus();
    }, 'primary');
    const newArea = h('div');
    replace(c, header('Threads', 'Type in the bar below, or hold the mic, to talk in the open thread.', newBtn), newArea, h('div', { class: 'split' }, listPane, detail));

    const [threads] = await Promise.all([
      api.threads(),
      param ? renderThreadDetail(detail, param, gen) : Promise.resolve(replace(detail, empty('Select a thread, or open one from a decision, approval or meeting. A message typed below with no thread open starts a new one.'))),
    ]);
    if (!current(gen)) return;
    const items = list(threads);
    renderRecent(items);
    if (!items.length) listPane.appendChild(empty('No threads yet.'));
    const ul = h('ul', { class: 'rows' });
    for (const t of items) {
      const id = get(t, 'id');
      const at = get(t, 'anchor_type');
      ul.appendChild(h('li', null, h('button', {
        type: 'button', class: 'row' + (id === param ? ' selected' : ''), on: { click: () => go('threads', id) },
      },
      h('span', { class: 'row-main' },
        h('span', { class: 'row-title' }, get(t, 'title') || 'Untitled'),
        h('span', { class: 'row-meta' },
          at ? badge(at, get(t, 'anchor_untrusted') ? 'warn' : '') : badge('free'),
          h('span', { class: 'muted' }, fmtAgo(get(t, 'updated_at'))))))));
    }
    listPane.appendChild(ul);
  }

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

  async function renderThreadDetail(pane, id, gen) {
    let res;
    try {
      res = await api.thread(id);
    } catch (err) {
      if (current(gen)) replace(pane, errorBox('Could not load thread', err));
      return;
    }
    if (!current(gen)) return;
    const t = get(res, 'thread') || {};
    const msgs = list(get(res, 'messages'));
    const at = get(t, 'anchor_type');
    const ctx = get(t, 'anchor_context');

    const parts = [h('header', { class: 'card-head' },
      h('h2', null, get(t, 'title') || 'Untitled'),
      h('div', { class: 'row-meta' }, at ? badge('About a ' + at) : null, h('span', { class: 'muted' }, 'Started ' + fmtDate(get(t, 'created_at')))))];
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
      log.appendChild(messageEl(get(m, 'role'), get(m, 'text'), meta).el);
    }
    if (!msgs.length) log.appendChild(empty('No messages yet. Type below, or hold the mic.'));
    const notices = h('div', { class: 'notices' });
    for (const n of state.threadNotices.get(id) || []) notices.appendChild(approvalNotice(n));

    replace(pane, h('article', { class: 'thread' }, parts, log, notices));
    state.threadUI = { id: get(t, 'id') || id, title: get(t, 'title') || 'Untitled', log, notices };
    updateBarTarget();
    if (log.lastElementChild) log.lastElementChild.scrollIntoView({ block: 'end' });
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

  // ---------- Meetings ----------

  async function viewMeetings(c, param, gen) {
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    replace(c, header('Meetings', 'Recaps are phrased from meeting speech, so they are shown as quoted text.'), h('div', { class: 'split' }, listPane, detail));
    const [ms] = await Promise.all([
      api.meetings(30),
      param ? renderMeetingDetail(detail, param, gen) : Promise.resolve(replace(detail, empty('Select a meeting to see its recap.'))),
    ]);
    if (!current(gen)) return;
    const items = list(ms);
    if (!items.length) listPane.appendChild(empty('No meetings recorded yet.'));
    const ul = h('ul', { class: 'rows' });
    for (const m of items) {
      const id = get(m, 'session_id');
      ul.appendChild(h('li', null, h('button', {
        type: 'button', class: 'row' + (id === param ? ' selected' : ''), on: { click: () => go('meetings', id) },
      },
      h('span', { class: 'row-main' },
        h('span', { class: 'row-title' }, get(m, 'event_title') || 'Meeting'),
        h('span', { class: 'row-meta' },
          get(m, 'live') ? badge('Live', 'hot') : null,
          recapBadge(get(m, 'recap')),
          h('span', { class: 'muted' }, fmtDate(get(m, 'started_at'))))))));
    }
    listPane.appendChild(ul);
  }

  function recapBadge(r) {
    const label = { ready: 'Recap ready', running: 'Recap running', skipped: 'No recap', failed: 'Recap failed', none: '' }[r];
    return label ? badge(label, 'recap-' + r) : null;
  }

  function duration(a, b) {
    const s = Date.parse(a), e = Date.parse(b);
    if (isNaN(s) || isNaN(e) || e < s) return '';
    const min = Math.round((e - s) / 60000);
    return min < 60 ? min + ' min' : Math.floor(min / 60) + ' h ' + (min % 60) + ' min';
  }

  async function renderMeetingDetail(pane, id, gen) {
    let m;
    try {
      m = await api.meeting(id);
    } catch (err) {
      if (current(gen)) replace(pane, errorBox('Could not load meeting', err));
      return;
    }
    if (!current(gen)) return;
    const recap = get(m, 'recap');
    const ended = get(m, 'ended_at');
    let body;
    switch (recap) {
      case 'ready':
        body = untrusted('Recap (quoted, untrusted: phrased from what was said in the meeting; shown as plain text)', get(m, 'recap_text') || '');
        break;
      case 'running':
        body = h('p', { class: 'muted' }, 'The recap is being written…');
        // Poll while this exact view is still showing.
        setTimeout(() => { if (current(gen)) renderMeetingDetail(pane, id, gen); }, 4000);
        break;
      case 'failed':
        body = h('div', { class: 'error' }, 'The recap failed: ' + (get(m, 'recap_error') || 'unknown error'));
        break;
      case 'skipped':
        body = h('p', { class: 'muted' }, 'No recap: ' + (get(m, 'recap_error') || 'nothing to recap'));
        break;
      default:
        body = h('p', { class: 'muted' }, get(m, 'live') ? 'This meeting is still in progress.' : 'No recap for this meeting.');
    }
    replace(pane, h('article', { class: 'meeting' },
      h('header', { class: 'card-head' },
        h('h2', null, get(m, 'event_title') || 'Meeting'),
        h('div', { class: 'row-meta' },
          get(m, 'live') ? badge('Live', 'hot') : null,
          recapBadge(recap),
          h('span', { class: 'muted' }, 'Started ' + fmtDate(get(m, 'started_at'))),
          ended ? h('span', { class: 'muted' }, duration(get(m, 'started_at'), ended)) : null)),
      body,
      h('div', { class: 'actions' }, button('Open a thread about this', () => openThreadAbout('meeting', id)))));
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
    setInterval(() => { refreshCounts(); refreshRecent(); }, 60000);
    // For the native shell: open a view (a tapped notification, the HUD's
    // Edit), or refresh after a held-mic voice turn finished.
    window.water = Object.freeze({
      open: (view, param) => { if (VIEWS.includes(view)) go(view, param ? String(param) : ''); },
      refresh: () => { barStatus(''); render(); refreshCounts(); refreshRecent(); },
    });
  }

  boot();
})();
