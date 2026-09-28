// view_decisions.js: the Decisions view (docs/slices/UI.md Phase 2 split
// app.js into one file per view; Phase 3b redesigned the card itself).
// Registers window.views.decisions. Shared helpers (payload forms,
// decisionResult, refreshCounts, ...) come from window.appShared; see
// app.js's header comment.
'use strict';

(function () {
  const { h, replace, get, list, fmtDay } = window.dom;
  const api = window.api;

  // KIND_ICONS maps decisions.EvidenceKind's closed set (docs/slices/UI.md
  // Phase 1c) to the plain Unicode glyph shown next to an evidence line
  // (Phase 3b). decisions.Suggestion.Icon (Phase 1c's build.go) deliberately
  // reuses the same closed set of strings for a suggestion's own icon, so
  // this one map covers both the left column's evidence lines and the
  // suggestion rows below. "" (no icon) maps to no glyph at all.
  //
  //   money    $  a figure (cash, spend, an invoice amount)
  //   customer ◎  an account or client
  //   issue    ⚑  a tracked ticket (Linear, GitHub)
  //   mail     ✉  an email
  //   calendar ▦  a calendar event or meeting
  //   research ⌕  a research run or finding
  const KIND_ICONS = { money: '$', customer: '◎', issue: '⚑', mail: '✉', calendar: '▦', research: '⌕' };
  function kindIcon(kind) { return KIND_ICONS[kind] || ''; }

  // initials mirrors internal/gateway.Initials (docs/slices/UI.md Phase
  // 3a) client-side, the same way decisionPriorityClass already mirrors
  // needsyou.DecisionPriority in app.js: a computed card's TeamSignal
  // (Phase 1c) has no server-resolved initials field of its own, unlike an
  // approval's requester, so the Team signal box computes them the same
  // way the server does for that other case. Two or more words: first
  // letter of the first and last ("Lee Chen" -> "LC"). One word: its first
  // two letters, or its only letter if it has just one. Empty: "?".
  function initials(name) {
    name = String(name || '').trim();
    if (!name) return '?';
    const words = name.split(/\s+/);
    if (words.length === 1) {
      const w = words[0];
      return w.length === 1 ? w.toUpperCase() : w.slice(0, 2).toUpperCase();
    }
    return (words[0][0] + words[words.length - 1][0]).toUpperCase();
  }

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
    replace(c, S.header('Decisions'), body);
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
    const actionStates = get(card, 'action_states') || {};

    // Header: title + due badge, question underneath (docs/slices/UI.md
    // Phase 3b; badge.warn deadline chip per docs/design/design-reference.html,
    // Slice UI-polish -- shown only when the card actually has a deadline).
    const dueDay = deadline ? fmtDay(deadline) : '';
    const el = h('article', { class: 'card ' + S.decisionPriorityClass(sev, deadline) },
      h('header', { class: 'card-head' },
        h('h2', null, get(card, 'Lead', 'lead') || get(card, 'Question', 'question') || id),
        h('div', { class: 'row-meta' },
          awaiting ? S.badge('Staged, awaiting your yes', 'status-pending') : null,
          readiness && readiness !== 'ready' ? h('span', { class: 'muted' }, S.readinessLabel(readiness)) : null,
          isUntrusted ? S.extGlyph() : null,
          dueDay ? S.badge('Due ' + dueDay, 'warn') : null)));

    const question = get(card, 'Question', 'question');
    if (question) el.appendChild(h('p', { class: 'question' }, question));

    // Left column: up to 3 evidence lines, one summarised line each with a
    // small source icon, no raw source text/addresses inline (the source is
    // reachable only through "View related data" below) -- .evidence div
    // structure per docs/design/design-reference.html, Slice UI-polish. The
    // "includes external content" banner is gone; isUntrusted now shows only
    // as a small icon+tooltip next to the "Evidence" heading (S.extGlyph,
    // the same mechanism app.js already uses elsewhere) -- the taint field
    // itself and its meaning are untouched, only this label changed.
    const evidence = list(get(card, 'Evidence', 'evidence'));
    const evWrap = h('div', { class: 'evidence' });
    if (!evidence.length) evWrap.appendChild(h('div', { class: 'muted' }, 'None.'));
    for (const ev of evidence.slice(0, 3)) {
      const kind = get(ev, 'Kind', 'kind') || '';
      const icon = kindIcon(kind);
      evWrap.appendChild(h('div', null,
        icon ? h('i', { title: kind }, icon) : null,
        get(ev, 'Text', 'text') || ''));
    }
    const evidenceCol = h('div', { class: 'evidence-col' + (isUntrusted ? ' untrusted-sec' : '') },
      h('h3', null, 'Evidence', isUntrusted ? S.extGlyph() : null), evWrap);

    // Right column: a small .panel with Team signal, initials + status,
    // "(simulated)" appended only when Signal.Simulated is true -- hidden
    // entirely (no panel, no grid) when the card has no signals, per
    // docs/slices/UI-polish.md.
    const signals = list(get(card, 'TeamSignal', 'team_signal'));
    if (signals.length) {
      const teamUl = h('ul', { class: 'team-signal' });
      for (const sig of signals) {
        const person = get(sig, 'Person', 'person') || '';
        const sigStatus = get(sig, 'Status', 'status') || '';
        const simulated = Boolean(get(sig, 'Simulated', 'simulated'));
        teamUl.appendChild(h('li', null,
          h('span', { class: 'avatar', title: person }, initials(person)),
          h('span', null, sigStatus + (simulated ? ' (simulated)' : ''))));
      }
      const teamCol = h('div', { class: 'panel' }, h('p', { class: 'label' }, 'Team signal'), teamUl);
      el.appendChild(h('div', { class: 'cols' }, evidenceCol, teamCol));
    } else {
      el.appendChild(evidenceCol);
    }

    // Options stay in the data (build.go / the API response) but are no
    // longer rendered on the card, per docs/slices/UI-polish.md.

    // "Recommendation:" one line. When the server sent no recommendation,
    // show "No recommendation yet: <what is missing>" (docs/slices/UI-polish.md)
    // built from the card's own already-computed Gaps -- the validation that
    // decides whether a recommendation exists is server-side and untouched,
    // this only changes the displayed string. The separate "Missing info: ..."
    // line's own gating (U12, Phase 3b -- shown whenever gaps exist, a
    // locked-in owner decision this slice doesn't reopen) is unchanged: it
    // can read as repeating the no-recommendation sentence in the one case
    // where both fire, which is an acceptable, pre-existing tradeoff U12
    // already accepted, not something to silently change here.
    const gaps = list(get(card, 'Gaps', 'gaps'));
    const rec = get(card, 'Recommendation', 'recommendation');
    const noRecText = 'No recommendation yet: ' + (gaps.length ? gaps.map(String).join('; ') : 'more information is needed') + '.';
    el.appendChild(h('p', { class: 'recommendation' },
      h('strong', null, 'Recommendation: '),
      rec || noRecText));
    if (gaps.length) {
      el.appendChild(h('p', { class: 'muted missing-info' }, 'Missing info: ' + gaps.map(String).join('; ')));
    }

    // Suggestion rows: icon + sentence + Review. Review opens the per-action
    // stage form for that specific action id; the recipient/payload never
    // appear before Review is clicked.
    const suggestions = list(get(card, 'ActionSuggestions', 'action_suggestions'));
    if (suggestions.length) {
      const sugWrap = h('section', { class: 'card-sec suggestions' }, h('h3', null, 'Suggested actions'));
      for (const sug of suggestions) sugWrap.appendChild(suggestionRow(id, sug, actionStates, gen));
      el.appendChild(sugWrap);
    }

    // "View related data (N)", full width: N = len(SourceItemIDs) + the
    // number of evidence sources (len(Evidence)), matching exactly what
    // api.relatedDecision lists (docs/slices/UI.md Phase 3b).
    const sourceCount = list(get(card, 'SourceItemIDs', 'source_item_ids')).length + evidence.length;
    const relatedArea = h('div', { class: 'related-area' });
    el.appendChild(S.button('View related data (' + sourceCount + ')', () => toggleRelated(id, relatedArea, gen), 'btn block'));
    el.appendChild(relatedArea);

    const stageArea = h('div', { class: 'stage-area' });
    const actionRow = h('div', { class: 'actions' });
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

  // suggestionRow builds one ActionSuggestion's row: icon + sentence +
  // Review (or "already staged"/"not granted"), matching the whole card's
  // own awaiting/staged handling but scoped to this one action id, backed
  // by card_action_states (Phase 1c) through the per-action stage endpoint
  // (Phase 3b) instead of the whole-card card_states row. Markup is the
  // reference's .action row (docs/design/design-reference.html, Slice
  // UI-polish): icon, one-line sentence, "Review" button.
  function suggestionRow(cardId, sug, actionStates, gen) {
    const S = window.appShared;
    const actionId = get(sug, 'ID', 'id');
    const icon = kindIcon(get(sug, 'Icon', 'icon') || '');
    const sentence = get(sug, 'Sentence', 'sentence') || '';
    const actionable = get(sug, 'Actionable', 'actionable');
    const state = actionStates[actionId];
    const approvalID = state ? get(state, 'approval_id') : '';
    const approvalStatus = state ? get(state, 'approval_status') : '';
    const awaiting = state && get(state, 'status') === 'staged' && approvalStatus === 'pending';

    const status = h('div', { class: 'card-status' });
    const stageArea = h('div', { class: 'stage-area' });
    const line = h('div', { class: 'action' },
      icon ? h('i', null, icon) : null,
      h('p', null, sentence));
    if (!awaiting) {
      if (actionable) {
        line.appendChild(S.button('Review', () => {
          replace(stageArea, stageActionForm(cardId, sug, status, gen));
        }, 'btn'));
      } else {
        line.appendChild(h('span', { class: 'muted not-granted', title: 'The manifest does not grant this at level A yet' }, '(not granted)'));
      }
    }
    const row = h('div', { class: 'suggestion-row' }, line, stageArea, status);

    if (awaiting) {
      replace(status, h('p', { class: 'muted' }, 'Loading the staged approval…'));
      api.approval(approvalID).then((env) => {
        if (S.current(gen)) replace(status, stagedPanel(env, status, gen, false));
      }).catch((err) => {
        if (S.current(gen)) replace(status, S.errorBox('Could not load the staged approval', err));
      });
    } else if (state && get(state, 'status')) {
      status.appendChild(h('p', { class: 'muted small' },
        approvalStatus === 'executed'
          ? 'Staged earlier, approved and done. You can stage it again.'
          : 'Staged earlier; you can stage it again.'));
    }
    return row;
  }

  // toggleRelated shows/hides "View related data"'s panel, fetched fresh
  // each time it opens (api.relatedDecision): the card's own sources only,
  // an evidence-line source as plain text, and a source that resolved to a
  // real external record as an Open button (U16) -- never a link the page
  // itself navigates through.
  function toggleRelated(cardId, area, gen) {
    const S = window.appShared;
    if (area.dataset.open === '1') {
      area.dataset.open = '';
      replace(area);
      return;
    }
    area.dataset.open = '1';
    replace(area, h('p', { class: 'muted' }, 'Loading…'));
    api.relatedDecision(cardId).then((res) => {
      if (!S.current(gen)) return;
      const sources = list(get(res, 'sources'));
      if (!sources.length) { replace(area, h('p', { class: 'muted' }, 'No related data.')); return; }
      const ul = h('ul', { class: 'related-list' });
      for (const src of sources) {
        const label = get(src, 'label') || get(src, 'ref') || '';
        const url = get(src, 'url');
        const source = get(src, 'source') || '';
        const srcID = get(src, 'id') || '';
        ul.appendChild(h('li', null,
          h('span', null, label),
          url ? S.button('Open ↗', () => S.openExternal(source, srcID, url), 'ghost small') : null));
      }
      replace(area, ul);
    }).catch((err) => {
      if (S.current(gen)) replace(area, S.errorBox('Could not load related data', err));
    });
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

  // stageActionForm is the per-action sibling of the old, function-keyed
  // stage form (docs/slices/UI.md Phase 3b): it calls
  // api.stageDecisionAction, keyed by this one ActionSuggestion's own id,
  // so staging it can never disturb another suggestion's state on the same
  // card. The read-back/confirm flow itself (stagedPanel above) is
  // unchanged.
  function stageActionForm(cardId, action, status, gen) {
    const S = window.appShared;
    const actionId = get(action, 'ID', 'id');
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
        const res = await api.stageDecisionAction(cardId, actionId, payload);
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
