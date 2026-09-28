// view_meetings.js: the Meetings view (docs/slices/UI.md Phase 2 split
// app.js into one file per view). Registers window.views.meetings.
'use strict';

(function () {
  const { h, replace, get, list, fmtDate, untrusted } = window.dom;
  const api = window.api;

  // meetingsTab remembers which list ("recent" or "upcoming") is showing,
  // across re-renders of this view within one page load.
  let meetingsTab = 'recent';

  async function viewMeetings(c, param, gen) {
    const S = window.appShared;
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    const tabs = h('div', { class: 'head-actions' },
      S.button('Recent', () => { meetingsTab = 'recent'; viewMeetings(c, param, gen); }, meetingsTab === 'recent' ? 'primary' : 'secondary'),
      S.button('Upcoming', () => { meetingsTab = 'upcoming'; viewMeetings(c, param, gen); }, meetingsTab === 'upcoming' ? 'primary' : 'secondary'));
    replace(c, S.header('Meetings', 'What was said and decided in your recent meetings.', tabs), h('div', { class: 'split' }, listPane, detail));

    if (meetingsTab === 'upcoming') {
      let events;
      try {
        events = list(await api.upcomingMeetings(20));
      } catch (err) {
        if (S.current(gen)) replace(listPane, S.errorBox('Could not load upcoming meetings', err));
        return;
      }
      if (!S.current(gen)) return;
      replace(detail, S.empty('Upcoming meetings come from your calendar; nothing to recap yet.'));
      if (!events.length) listPane.appendChild(S.empty('Nothing upcoming.'));
      const box = h('div', { class: 'list' });
      for (const e of events) {
        const meta = [get(e, 'location'), fmtDate(get(e, 'start_at'))].filter(Boolean).join(' · ');
        box.appendChild(h('div', { class: 'row row-static' },
          S.icon('microphone'),
          h('div', { class: 'body' },
            h('div', { class: 'title' }, get(e, 'title') || 'Meeting'),
            h('div', { class: 'meta' }, meta))));
      }
      listPane.appendChild(box);
      return;
    }

    const [ms] = await Promise.all([
      api.meetings(30),
      param ? renderMeetingDetail(detail, param, gen) : Promise.resolve(replace(detail, S.empty('Select a meeting to see its recap.'))),
    ]);
    if (!S.current(gen)) return;
    const items = list(ms);
    if (!items.length) listPane.appendChild(S.empty('No meetings recorded yet.'));
    const box = h('div', { class: 'list' });
    for (const m of items) {
      const id = get(m, 'session_id');
      const guess = get(m, 'project_guess');
      const metaBits = [fmtDate(get(m, 'started_at'))];
      if (guess && get(guess, 'available')) metaBits.push(get(guess, 'label'));
      const trailing = [];
      if (get(m, 'live')) trailing.push(S.badge('Live', 'hot'));
      const rb = recapBadge(get(m, 'recap'));
      if (rb) trailing.push(rb);
      box.appendChild(h('button', {
        type: 'button', class: 'row' + (id === param ? ' sel' : ''), on: { click: () => S.go('meetings', id) },
      },
      S.icon('microphone'),
      h('div', { class: 'body' },
        h('div', { class: 'title' }, get(m, 'event_title') || 'Meeting'),
        h('div', { class: 'meta' }, metaBits.join(' · '))),
      trailing.length ? h('span', null, trailing) : null));
    }
    listPane.appendChild(box);
  }

  function recapBadge(r) {
    const S = window.appShared;
    const label = { ready: 'Recap ready', running: 'Recap running', skipped: 'No recap', failed: 'Recap failed', none: '' }[r];
    return label ? S.badge(label, 'recap-' + r) : null;
  }

  function duration(a, b) {
    const s = Date.parse(a), e = Date.parse(b);
    if (isNaN(s) || isNaN(e) || e < s) return '';
    const min = Math.round((e - s) / 60000);
    return min < 60 ? min + ' min' : Math.floor(min / 60) + ' h ' + (min % 60) + ' min';
  }

  async function renderMeetingDetail(pane, id, gen) {
    const S = window.appShared;
    let m;
    try {
      m = await api.meeting(id);
    } catch (err) {
      if (S.current(gen)) replace(pane, S.errorBox('Could not load meeting', err));
      return;
    }
    if (!S.current(gen)) return;
    const recap = get(m, 'recap');
    const ended = get(m, 'ended_at');
    let body;
    switch (recap) {
      case 'ready':
        body = h('div', null,
          untrusted('Recap (quoted, untrusted: phrased from what was said in the meeting; shown as plain text)', get(m, 'recap_text') || ''),
          recapSignalsSections(m));
        break;
      case 'running':
        body = h('p', { class: 'muted' }, 'The recap is being written…');
        // Poll while this exact view is still showing.
        setTimeout(() => { if (S.current(gen)) renderMeetingDetail(pane, id, gen); }, 4000);
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
    replace(pane, h('article', { class: 'card' },
      h('header', { class: 'card-head' },
        h('h2', null, get(m, 'event_title') || 'Meeting'),
        h('div', { class: 'row-meta' },
          get(m, 'live') ? S.badge('Live', 'hot') : null,
          recapBadge(recap),
          h('span', { class: 'muted' }, 'Started ' + fmtDate(get(m, 'started_at'))),
          ended ? h('span', { class: 'muted' }, duration(get(m, 'started_at'), ended)) : null)),
      body,
      h('div', { class: 'actions' }, S.button('Open a thread about this', () => S.openThreadAbout('meeting', id)))));
  }

  // recapSignalsSections renders the recap's four sections (docs/slices/
  // UI.md Phase 3d: Decisions made, Action items, Open questions, FYI) from
  // recap_signals -- structured JSON the daemon extracted from the
  // transcript by code, not parsed out of recap_text prose here -- plus the
  // project guess's own label, always shown as a guess (Label already
  // reads "likely: <name> (<bucket>)" or "Project match: unavailable";
  // this view never restates or recomputes it). null when the session
  // predates migration 0022 or its recap produced no signals.
  function recapSignalsSections(m) {
    const sig = get(m, 'recap_signals');
    if (!sig) return null;
    const guess = get(m, 'project_guess');
    return h('div', { class: 'recap-signals' },
      guess && get(guess, 'available') ? h('p', { class: 'muted' }, 'Project: ' + get(guess, 'label')) : null,
      recapSection('Decisions made', list(get(sig, 'decisions'))),
      recapActionItemsSection(list(get(sig, 'action_items'))),
      recapSection('Open questions', list(get(sig, 'open_questions'))),
      recapSection('FYI', list(get(sig, 'fyi'))));
  }

  function recapSection(title, items) {
    return h('div', { class: 'recap-section' },
      h('p', { class: 'label' }, title),
      items.length ? h('ul', { class: 'plain' }, items.map((it) => h('li', null, get(it, 'text')))) : h('p', { class: 'muted' }, 'None.'));
  }

  // recapActionItemsSection shows each action item's owner with an avatar
  // only when the daemon resolved one (an exact roster name match, never a
  // fuzzy guess, docs/slices/UI.md Phase 3d) -- a name with no match still
  // shows as plain text, with no avatar.
  function recapActionItemsSection(items) {
    return h('div', { class: 'recap-section' },
      h('p', { class: 'label' }, 'Action items'),
      items.length ? h('ul', { class: 'plain' }, items.map((it) => {
        const owner = get(it, 'owner');
        const initials = get(it, 'owner_initials');
        return h('li', { class: 'row-line' },
          owner ? (initials ? h('span', { class: 'avatar', title: owner }, initials) : h('span', { class: 'muted' }, owner + ':')) : null,
          h('span', null, get(it, 'text')));
      })) : h('p', { class: 'muted' }, 'None.'));
  }

  window.views = window.views || {};
  window.views.meetings = viewMeetings;
})();
