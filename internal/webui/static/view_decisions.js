// view_decisions.js: the Decisions view (docs/slices/UI.md Phase 2 split
// app.js into one file per view). Registers window.views.decisions. Shared
// helpers (payload forms, decisionResult, refreshCounts, ...) come from
// window.appShared; see app.js's header comment.
'use strict';

(function () {
  const { h, replace, get, list } = window.dom;
  const api = window.api;

  async function viewDecisions(c, param, gen) {
    const S = window.appShared;
    const cards = list(await api.decisions());
    if (!S.current(gen)) return;
    S.setCount('decisions', cards.length);
    const body = h('div', { class: 'cards' });
    if (!cards.length) body.appendChild(S.empty('No open decisions.'));
    let focus = null;
    for (const card of cards) {
      const el = decisionCard(card, gen);
      if (param && get(card, 'ID', 'id') === param) { el.classList.add('focus'); focus = el; }
      body.appendChild(el);
    }
    replace(c, S.header('Decisions', 'Approving a card is two steps: stage it, read exactly what will happen, then confirm. Nothing is sent before you confirm.'), body);
    if (focus) focus.scrollIntoView({ block: 'start' });
  }

  function decisionCard(card, gen) {
    const S = window.appShared;
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

    const el = h('article', { class: 'card ' + S.decisionPriorityClass(sev, deadline) },
      h('header', { class: 'card-head' },
        h('h2', null, get(card, 'Lead', 'lead') || get(card, 'Question', 'question') || id),
        h('div', { class: 'row-meta' },
          awaiting ? S.badge('Staged, awaiting your yes', 'status-pending') : null,
          readiness && readiness !== 'ready' ? h('span', { class: 'muted' }, S.readinessLabel(readiness)) : null,
          isUntrusted ? S.extGlyph() : null,
          S.dueBadge(deadline))));

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
          actionRow.appendChild(S.button('Prepare ' + S.actionLabel(fn), () => {
            replace(stageArea, stageForm(id, a, status, gen));
          }, 'primary'));
        } else {
          actionRow.appendChild(h('span', { class: 'muted not-granted', title: 'The manifest does not grant this at level A yet' }, S.actionLabel(fn) + ' (not granted)'));
        }
      }
    }
    actionRow.appendChild(S.button('Open a thread about this', () => S.openThreadAbout('decision', id)));
    actionRow.appendChild(S.button('Reject', () => replace(stageArea, dismissForm(id, el, status)), 'ghost'));
    el.appendChild(actionRow);
    el.appendChild(stageArea);
    el.appendChild(status);

    if (awaiting) {
      // Staged earlier (this session or another): show that envelope's
      // read-back and the confirm step, never a second staging.
      replace(status, h('p', { class: 'muted' }, 'Loading the staged approval…'));
      api.approval(stagedID).then((env) => {
        if (S.current(gen)) replace(status, stagedPanel(env, status, gen, false));
      }).catch((err) => {
        if (S.current(gen)) replace(status, S.errorBox('Could not load the staged approval', err));
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
    const S = window.appShared;
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
    const yes = () => S.armed(S.button(S.isOutward(action) ? 'Yes, send it' : 'Yes, approve and run', () => decide('yes'), 'primary'));
    const edit = S.button('Edit', () => {
      replace(out, S.editForm(env, (next) => replace(status, stagedPanel(next, status, gen, true)), () => replace(out)));
    });
    const openBtn = S.button('Open in Approvals', () => S.go('approvals', id), 'ghost');
    function reset() {
      if (justStaged) replace(actions, yes(), edit, openBtn);
      else {
        const approve = S.button('Approve…', () => {
          replace(actions, h('span', { class: 'confirm-q' }, 'Run this now, exactly as read back above?'), yes(), S.button('Not yet', reset, 'ghost'));
        }, 'primary');
        replace(actions, approve, edit, openBtn);
      }
    }
    async function decide(reply) {
      for (const b of actions.querySelectorAll('button')) b.disabled = true;
      try {
        const res = await api.decideApproval(id, get(env, 'payload_hash'), reply);
        replace(actions);
        replace(out, S.decisionResult(res, action));
        S.refreshCounts();
      } catch (err) {
        if (err.status === 409) {
          S.toast('This approval changed since you opened it. Showing the current version.', 'warn');
          if (S.current(gen)) S.render();
          return;
        }
        for (const b of actions.querySelectorAll('button')) b.disabled = false;
        replace(out, S.errorBox('Could not record your answer', err));
      }
    }
    reset();
    return panel;
  }

  function stageForm(cardId, action, status, gen) {
    const S = window.appShared;
    const fn = get(action, 'Function', 'function') || '';
    const form = h('div', { class: 'form' }, h('h3', null, 'Prepare ' + S.actionLabel(fn)));
    const read = S.payloadInputs(form, S.payloadFields(action));
    const submit = S.button('Stage for approval', async () => {
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
        S.refreshCounts();
      } catch (err) {
        submit.disabled = false;
        replace(status, S.errorBox('Staging refused', err));
      }
    }, 'primary');
    form.appendChild(h('div', { class: 'actions' }, submit, S.button('Cancel', () => { replace(form); replace(status); }, 'ghost')));
    return form;
  }

  // dismissForm is Reject (D3): the card is dismissed, with an optional
  // reason. It never touches an envelope already staged from it.
  function dismissForm(cardId, cardEl, status) {
    const S = window.appShared;
    const input = h('input', { type: 'text', placeholder: 'Reason (optional)', maxlength: 1000, autocomplete: 'off' });
    const form = h('div', { class: 'form' }, h('label', null, 'Reject this decision?'), input);
    const confirm = S.button('Reject', async () => {
      confirm.disabled = true;
      try {
        await api.dismissDecision(cardId, input.value.trim());
        cardEl.classList.add('gone');
        replace(cardEl, h('p', { class: 'muted' }, 'Rejected.'));
        S.refreshCounts();
      } catch (err) {
        confirm.disabled = false;
        replace(status, S.errorBox('Could not reject', err));
      }
    }, 'danger');
    form.appendChild(h('div', { class: 'actions' }, confirm, S.button('Keep', () => replace(form), 'ghost')));
    return form;
  }

  window.views = window.views || {};
  window.views.decisions = viewDecisions;
})();
