// view_meetings.js: the Meetings view (docs/slices/UI.md Phase 2 split
// app.js into one file per view). Registers window.views.meetings.
'use strict';

(function () {
  const { h, replace, get, list, fmtDate, untrusted } = window.dom;
  const api = window.api;

  async function viewMeetings(c, param, gen) {
    const S = window.appShared;
    const listPane = h('div', { class: 'list-pane' });
    const detail = h('div', { class: 'detail-pane' });
    replace(c, S.header('Meetings', 'Recaps are phrased from meeting speech, so they are shown as quoted text.'), h('div', { class: 'split' }, listPane, detail));
    const [ms] = await Promise.all([
      api.meetings(30),
      param ? renderMeetingDetail(detail, param, gen) : Promise.resolve(replace(detail, S.empty('Select a meeting to see its recap.'))),
    ]);
    if (!S.current(gen)) return;
    const items = list(ms);
    if (!items.length) listPane.appendChild(S.empty('No meetings recorded yet.'));
    const ul = h('ul', { class: 'rows' });
    for (const m of items) {
      const id = get(m, 'session_id');
      ul.appendChild(h('li', null, h('button', {
        type: 'button', class: 'row' + (id === param ? ' selected' : ''), on: { click: () => S.go('meetings', id) },
      },
      h('span', { class: 'row-main' },
        h('span', { class: 'row-title' }, get(m, 'event_title') || 'Meeting'),
        h('span', { class: 'row-meta' },
          get(m, 'live') ? S.badge('Live', 'hot') : null,
          recapBadge(get(m, 'recap')),
          h('span', { class: 'muted' }, fmtDate(get(m, 'started_at'))))))));
    }
    listPane.appendChild(ul);
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
        body = untrusted('Recap (quoted, untrusted: phrased from what was said in the meeting; shown as plain text)', get(m, 'recap_text') || '');
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
    replace(pane, h('article', { class: 'meeting' },
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

  window.views = window.views || {};
  window.views.meetings = viewMeetings;
})();
