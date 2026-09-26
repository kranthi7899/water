// app.js: the workspace UI's views (Today, Decisions, Approvals, Threads,
// Meetings). Rendering goes through dom.js only; every server string is a
// text node. Nothing here executes an outward action by itself: a decision
// card is staged into a PENDING approval envelope, and only the Approvals
// view's explicit two-step confirm answers that envelope "yes", through the
// same approval-decision endpoint every other client uses (api.js).
'use strict';

(function () {
  const { h, replace, get, list, fmtDate, fmtTime, fmtAgo, untrusted } = window.dom;
  const api = window.api;

  const main = document.getElementById('main');
  const toastEl = document.getElementById('toast');
  const VIEWS = ['today', 'decisions', 'approvals', 'threads', 'meetings'];

  const state = {
    view: 'today',
    param: '',
    gen: 0, // bumped on every render; a response for an older render is dropped
    approvalTab: 'pending',
    // approval_required notices raised while a thread turn streamed, kept
    // per thread so they survive the re-fetch on done.
    threadNotices: new Map(),
    streaming: null, // {threadId, taskId}
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

  function go(view, param) {
    const hash = '#' + view + (param ? '/' + encodeURIComponent(param) : '');
    if (location.hash === hash) render();
    else location.hash = hash;
  }

  function render() {
    const { view, param } = parseHash();
    state.view = view;
    state.param = param;
    const gen = ++state.gen;
    for (const b of document.querySelectorAll('.nav')) {
      const on = b.dataset.view === view;
      b.classList.toggle('active', on);
      if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
    }
    const container = h('div', { class: 'view view-' + view });
    replace(main, container);
    container.appendChild(h('p', { class: 'loading' }, 'Loading…'));
    const fn = { today: viewToday, decisions: viewDecisions, approvals: viewApprovals, threads: viewThreads, meetings: viewMeetings }[view];
    fn(container, param, gen).catch((err) => {
      if (current(gen)) replace(container, errorBox('Could not load ' + view, err));
    });
  }

  async function openThreadAbout(type, id) {
    try {
      const res = await api.anchorThread(type, id);
      go('threads', get(res && res.thread, 'id'));
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
    replace(c, header('Today', day), h('div', { class: 'grid-2' }, needs, sched));
  }

  function readinessLabel(r) {
    return { ready: 'Ready', missing_info: 'Missing info', blocked: 'Blocked' }[r] || String(r);
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
      const el = decisionCard(card);
      if (param && get(card, 'ID', 'id') === param) { el.classList.add('focus'); focus = el; }
      body.appendChild(el);
    }
    replace(c, header('Decisions', 'Staging a card creates a pending approval. Nothing is sent until you confirm it in Approvals.'), body);
    if (focus) focus.scrollIntoView({ block: 'start' });
  }

  function decisionCard(card) {
    const id = get(card, 'ID', 'id');
    const isUntrusted = Boolean(get(card, 'Untrusted', 'untrusted'));
    const sev = get(card, 'Severity', 'severity');
    const readiness = get(card, 'Readiness', 'readiness');
    const deadline = get(card, 'Deadline', 'deadline');
    const status = h('div', { class: 'card-status' });

    const el = h('article', { class: 'card' },
      h('header', { class: 'card-head' },
        h('h2', null, get(card, 'Lead', 'lead') || get(card, 'Question', 'question') || id),
        h('div', { class: 'row-meta' },
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
      const sec = h('section', { class: 'card-sec' + (isUntrusted ? ' untrusted-sec' : '') },
        h('h3', null, isUntrusted ? 'Evidence (includes external content, shown as text)' : 'Evidence'), ul);
      el.appendChild(sec);
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

    // Staged actions: each actionable one can be prepared and staged.
    const actions = list(get(card, 'StagedActions', 'staged_actions'));
    const stageArea = h('div', { class: 'stage-area' });
    const actionRow = h('div', { class: 'actions' });
    for (const a of actions) {
      const fn = get(a, 'Function', 'function') || '';
      if (get(a, 'Actionable', 'actionable')) {
        actionRow.appendChild(button('Prepare ' + fn, () => {
          replace(stageArea, stageForm(id, a, status));
        }, 'primary'));
      } else {
        actionRow.appendChild(h('span', { class: 'muted not-granted', title: 'The manifest does not grant this at level A yet' }, fn + ' (not granted)'));
      }
    }
    actionRow.appendChild(button('Open a thread about this', () => openThreadAbout('decision', id)));
    actionRow.appendChild(button('Dismiss', () => replace(stageArea, dismissForm(id, el, status)), 'ghost'));
    el.appendChild(actionRow);
    el.appendChild(stageArea);
    el.appendChild(status);
    return el;
  }

  // payloadFields decides the editable fields for one staged action: the
  // card's own payload keys when it carries one, the usual to/subject/body
  // for a mail action, or a raw JSON box otherwise.
  function payloadFields(action) {
    const p = get(action, 'Payload', 'payload');
    const fn = get(action, 'Function', 'function') || '';
    if (p && typeof p === 'object' && Object.keys(p).length) {
      return Object.keys(p).map((k) => ({ key: k, kind: kindOf(p[k]), value: p[k] }));
    }
    if (/\.(send_message|draft_message|draft_for_review)$/.test(fn)) {
      return [{ key: 'to', kind: 'list', value: [] }, { key: 'subject', kind: 'text', value: '' }, { key: 'body', kind: 'long', value: '' }];
    }
    return [{ key: '', kind: 'json', value: {} }];
  }

  function kindOf(v) {
    if (Array.isArray(v) && v.every((x) => typeof x === 'string')) return 'list';
    if (typeof v === 'string') return (v.length > 80 || v.includes('\n')) ? 'long' : 'text';
    return 'json';
  }

  function stageForm(cardId, action, status) {
    const fn = get(action, 'Function', 'function') || '';
    const fields = payloadFields(action);
    const inputs = [];
    const form = h('div', { class: 'form' }, h('h3', null, 'Prepare ' + fn));
    fields.forEach((f, i) => {
      const fid = 'f-' + cardId.replace(/[^a-zA-Z0-9_-]/g, '') + '-' + i;
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
    const submit = button('Stage for approval', async () => {
      let payload = {};
      try {
        for (const { f, input } of inputs) {
          let v;
          if (f.kind === 'list') v = input.value.split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
          else if (f.kind === 'json') v = JSON.parse(input.value || 'null');
          else v = input.value;
          if (f.key) payload[f.key] = v; else payload = v;
        }
      } catch (err) {
        replace(status, h('div', { class: 'error' }, 'The payload is not valid JSON: ' + err.message));
        return;
      }
      submit.disabled = true;
      try {
        const res = await api.stageDecision(cardId, fn, payload);
        const env = get(res, 'envelope') || {};
        replace(form);
        replace(status,
          h('div', { class: 'ok' },
            h('p', null, get(res, 'status') === 'already_staged'
              ? 'Already staged. This approval is still waiting for your confirmation.'
              : 'Staged. Nothing has been sent: confirm it in Approvals.'),
            h('div', { class: 'readback' }, get(env, 'read_back') || ''),
            button('Review and confirm', () => go('approvals', get(res, 'approval_id')), 'primary')));
        refreshCounts();
      } catch (err) {
        submit.disabled = false;
        replace(status, errorBox('Staging refused', err));
      }
    }, 'primary');
    form.appendChild(h('div', { class: 'actions' }, submit, button('Cancel', () => { replace(form); replace(status); }, 'ghost')));
    return form;
  }

  function dismissForm(cardId, cardEl, status) {
    const input = h('input', { type: 'text', placeholder: 'Reason (optional)', maxlength: 1000, autocomplete: 'off' });
    const form = h('div', { class: 'form' }, h('label', null, 'Dismiss this decision?'), input);
    const confirm = button('Dismiss', async () => {
      confirm.disabled = true;
      try {
        await api.dismissDecision(cardId, input.value.trim());
        cardEl.classList.add('gone');
        replace(cardEl, h('p', { class: 'muted' }, 'Dismissed.'));
        refreshCounts();
      } catch (err) {
        confirm.disabled = false;
        replace(status, errorBox('Could not dismiss', err));
      }
    }, 'danger');
    form.appendChild(h('div', { class: 'actions' }, confirm, button('Keep', () => replace(form), 'ghost')));
    return form;
  }

  // ---------- Approvals ----------

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
      param ? renderApprovalDetail(detail, param, gen) : Promise.resolve(replace(detail, empty('Select an approval to review it.'))),
    ]);
    if (!current(gen)) return;
    const items = list(envs);
    if (state.approvalTab === 'pending') setCount('approvals', items.length);
    const ul = h('ul', { class: 'rows' });
    if (!items.length) listPane.appendChild(empty(state.approvalTab === 'pending' ? 'No approvals waiting.' : 'Nothing decided yet.'));
    for (const e of items) {
      const id = get(e, 'id');
      ul.appendChild(h('li', null, h('button', {
        type: 'button', class: 'row' + (id === param ? ' selected' : ''), on: { click: () => go('approvals', id) },
      },
      h('span', { class: 'row-main' },
        h('span', { class: 'row-title' }, get(e, 'action') || id),
        h('span', { class: 'row-sub' }, get(e, 'summary') || ''),
        h('span', { class: 'row-meta' },
          badge(get(e, 'status') || '', 'status-' + get(e, 'status')),
          get(e, 'risk') ? badge('Risk ' + get(e, 'risk'), 'risk-' + get(e, 'risk')) : null,
          h('span', { class: 'muted' }, fmtAgo(get(e, 'created_at'))))))));
    }
    listPane.appendChild(ul);
  }

  async function renderApprovalDetail(pane, id, gen) {
    let env;
    try {
      env = await api.approval(id);
    } catch (err) {
      if (current(gen)) replace(pane, errorBox('Could not load approval', err));
      return;
    }
    if (!current(gen)) return;
    const status = get(env, 'status');
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
    if (pending) {
      const approve = button('Approve…', () => {
        replace(actions,
          h('span', { class: 'confirm-q' }, 'Run this now, exactly as read back above?'),
          button('Yes, approve and run', () => decide('yes'), 'primary'),
          button('Not yet', () => { replace(actions, approve, deny, threadBtn); }, 'ghost'));
      }, 'primary');
      const deny = button('Deny', () => decide('no'), 'danger');
      actions.appendChild(approve);
      actions.appendChild(deny);
    }
    const threadBtn = button('Open a thread about this', () => openThreadAbout('approval', id));
    actions.appendChild(threadBtn);

    async function decide(reply) {
      for (const b of actions.querySelectorAll('button')) b.disabled = true;
      try {
        const res = await api.decideApproval(id, get(env, 'payload_hash'), reply);
        const done = h('div', { class: get(res, 'error') ? 'error' : 'ok' });
        const answer = get(res, 'answer');
        if (answer === 'yes') {
          done.appendChild(h('p', null, get(res, 'executed') ? 'Approved and done.' : 'Approved, but it did not run.'));
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
        replace(out, done);
        replace(actions, threadBtn);
        refreshCounts();
      } catch (err) {
        if (err.status === 409) {
          toast('This approval changed since you opened it. Showing the current version.', 'warn');
          if (current(gen)) renderApprovalDetail(pane, id, gen);
          return;
        }
        for (const b of actions.querySelectorAll('button')) b.disabled = false;
        replace(out, errorBox('Could not record your answer', err));
      }
    }

    replace(pane,
      h('article', { class: 'approval' },
        h('header', { class: 'card-head' },
          h('h2', null, get(env, 'action') || id),
          h('div', { class: 'row-meta' },
            badge(status || '', 'status-' + status),
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
        } catch (err) { toast('Could not create a thread: ' + errText(err), 'bad'); }
      };
      input.addEventListener('keydown', (e) => { if (e.key === 'Enter') create(); });
      replace(newArea, h('div', { class: 'form inline' }, input, button('Create', create, 'primary'), button('Cancel', () => replace(newArea), 'ghost')));
      input.focus();
    }, 'primary');
    const newArea = h('div');
    replace(c, header('Threads', null, newBtn), newArea, h('div', { class: 'split' }, listPane, detail));

    const [threads] = await Promise.all([
      api.threads(),
      param ? renderThreadDetail(detail, param, gen) : Promise.resolve(replace(detail, empty('Select a thread, or open one from a decision, approval or meeting.'))),
    ]);
    if (!current(gen)) return;
    const items = list(threads);
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
      button('Review', () => go('approvals', n.id), 'primary'));
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
      log.appendChild(messageEl(get(m, 'role'), get(m, 'text'), fmtDate(get(m, 'created_at'))).el);
    }
    if (!msgs.length) log.appendChild(empty('No messages yet.'));
    const notices = h('div', { class: 'notices' });
    for (const n of state.threadNotices.get(id) || []) notices.appendChild(approvalNotice(n));

    const input = h('textarea', { rows: 3, placeholder: 'Ask your twin about this…', 'aria-label': 'Message' });
    const send = button('Send', () => submit(), 'primary');
    const cancel = button('Stop', async () => {
      if (state.streaming && state.streaming.taskId) {
        try { await api.cancelTask(state.streaming.taskId); } catch (_) { /* the turn may already be done */ }
      }
    }, 'ghost', { hidden: true });
    const statusLine = h('div', { class: 'muted small' });
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); submit(); }
    });

    async function submit() {
      const text = input.value.trim();
      if (!text || state.streaming) return;
      const emptyEl = log.querySelector('.empty');
      if (emptyEl) emptyEl.remove();
      log.appendChild(messageEl('ceo', text, 'now').el);
      const reply = messageEl('twin', '', 'thinking…');
      reply.el.classList.add('pending');
      log.appendChild(reply.el);
      reply.el.scrollIntoView({ block: 'end' });
      input.value = '';
      send.disabled = true;
      input.disabled = true;
      cancel.hidden = false;
      state.streaming = { threadId: id, taskId: '' };
      let failed = '';
      try {
        await api.postThreadMessage(id, text, (taskId) => { state.streaming.taskId = taskId; }, (ev) => {
          switch (ev.kind) {
            case 'queued':
              statusLine.textContent = 'Waiting for another turn to finish…';
              break;
            case 'delta':
              statusLine.textContent = '';
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
              break; // ack, sentence and anything newer: ignored
          }
        });
      } catch (err) {
        failed = errText(err);
      } finally {
        state.streaming = null;
      }
      if (failed) {
        reply.el.classList.remove('pending');
        reply.el.classList.add('failed');
        reply.body.textContent = (reply.body.textContent ? reply.body.textContent + '\n\n' : '') + 'Error: ' + failed;
        send.disabled = false;
        input.disabled = false;
        cancel.hidden = true;
        return;
      }
      // The daemon stored the reply before sending done; re-fetch so the
      // view shows exactly what was saved.
      if (state.view === 'threads' && state.param === id) render();
    }

    replace(pane, h('article', { class: 'thread' }, parts, log, notices,
      h('div', { class: 'composer' }, input, h('div', { class: 'actions' }, send, cancel, statusLine))));
    log.lastElementChild && log.lastElementChild.scrollIntoView({ block: 'end' });
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

  // ---------- counts, mic, boot ----------

  async function refreshCounts() {
    try {
      const [t, pending] = await Promise.all([api.today(), api.approvals('pending', 500)]);
      setCount('today', list(get(t, 'needs_you')).length);
      setCount('approvals', list(pending).length);
    } catch (_) { /* counts are best-effort */ }
  }

  function setupMic() {
    const mic = document.getElementById('mic');
    const wk = window.webkit;
    const handler = wk && wk.messageHandlers && wk.messageHandlers.water;
    if (!handler || typeof handler.postMessage !== 'function') {
      mic.hidden = true;
      return;
    }
    mic.hidden = false;
    mic.addEventListener('click', () => {
      try { handler.postMessage({ type: 'mic' }); } catch (err) { toast('Voice is unavailable: ' + errText(err), 'bad'); }
    });
  }

  function boot() {
    for (const b of document.querySelectorAll('.nav')) {
      b.addEventListener('click', () => go(b.dataset.view));
    }
    document.getElementById('refresh').addEventListener('click', () => { render(); refreshCounts(); });
    window.addEventListener('hashchange', render);
    setupMic();
    render();
    refreshCounts();
    setInterval(refreshCounts, 60000);
    // For the native shell (e.g. a tapped notification): open a view.
    window.water = Object.freeze({
      open: (view, param) => { if (VIEWS.includes(view)) go(view, param ? String(param) : ''); },
      refresh: () => { render(); refreshCounts(); },
    });
  }

  boot();
})();
