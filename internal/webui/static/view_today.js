// view_today.js: the Today view (docs/slices/UI.md Phase 2 split app.js into
// one file per view). Registers window.views.today. Every helper used here
// beyond dom.js/api.js comes from window.appShared (set up by app.js, loaded
// before this file; see index.html and app.js's own header comment).
'use strict';

(function () {
  const { h, replace, get, list, fmtTime, fmtAgo } = window.dom;
  const api = window.api;

  // statTile: one of the reference's .metrics/.metric stat tiles (label +
  // one large number). Every value here comes straight from the same
  // needs_you/schedule arrays already fetched below -- no new data, no
  // invented numbers.
  function statTile(label, value) {
    return h('div', { class: 'metric' }, h('p', { class: 'k' }, label), h('p', { class: 'v' }, String(value)));
  }

  async function viewToday(c, _param, gen) {
    const S = window.appShared;
    const t = await api.today();
    if (!S.current(gen)) return;
    const items = list(get(t, 'needs_you'));
    const schedule = list(get(t, 'schedule'));
    S.setCount('today', items.length);

    const now = Date.now();
    let overdue = 0;
    for (const it of items) {
      const d = get(it, 'Deadline', 'deadline');
      const ts = d ? Date.parse(d) : NaN;
      if (!isNaN(ts) && ts < now) overdue++;
    }
    const metrics = h('div', { class: 'metrics' },
      statTile('Needs you', items.length),
      statTile('Overdue', overdue),
      statTile("Today's events", schedule.length));

    const needs = h('section', { class: 'panel' }, h('h2', null, 'Needs you'));
    if (!items.length) {
      needs.appendChild(S.empty('Nothing needs you right now.'));
    } else {
      const listEl = h('div', { class: 'list' });
      for (const it of items) {
        const kind = get(it, 'Kind', 'kind');
        const id = get(it, 'ID', 'id');
        const deadline = get(it, 'Deadline', 'deadline');
        const readiness = get(it, 'Readiness', 'readiness');
        const origin = get(it, 'Origin', 'origin');
        listEl.appendChild(h('button', {
          type: 'button', class: 'row ' + S.priorityClass(it),
          on: { click: () => S.go(kind === 'approval' ? 'approvals' : 'decisions', id) },
        },
        h('span', { class: 'ico', title: kind === 'approval' ? 'Approval' : 'Decision' }, S.kindGlyph(it)),
        h('div', { class: 'body' },
          h('p', { class: 'title' }, get(it, 'Title', 'title') || id),
          origin ? h('p', { class: 'meta' }, origin) : null,
          h('p', { class: 'row-meta' },
            S.badge(kind === 'approval' ? 'Approval' : 'Decision', kind),
            readiness && readiness !== 'ready' ? h('span', { class: 'muted' }, S.readinessLabel(readiness)) : null,
            get(it, 'Untrusted', 'untrusted') ? S.extGlyph() : null,
            S.dueBadge(deadline),
            kind === 'approval' ? h('span', { class: 'muted' }, 'Waiting ' + fmtAgo(get(it, 'CreatedAt', 'created_at')).replace(' ago', '')) : null))));
      }
      needs.appendChild(listEl);
    }

    const sched = h('section', { class: 'panel' }, h('h2', null, 'Schedule'));
    if (!schedule.length) sched.appendChild(S.empty('No events today.'));
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
    replace(c, S.header('Today', day), h('div', { class: 'stack' }, metrics, needs, sched));
  }

  window.views = window.views || {};
  window.views.today = viewToday;
})();
