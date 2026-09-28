// view_dashboards.js: the Dashboards page (docs/slices/UI.md Phase 2's
// list, Phase 4's detail; restyled to docs/design/design-reference.html's
// "Dashboard" section in Slice UI-polish, task 7). Registers
// window.views.dashboards. With no param it lists what twins/<id>/
// dashboards/*.yaml loaded (id, name, a human-readable source label); with
// a param (the dashboard id, from the URL hash) it also renders that one
// dashboard's actual computed tiles from api.dashboard(id)
// (internal/dashboards.Compute): three metrics, one breakdown and one
// callout, each carrying a tile state (ok/not_connected/unavailable/
// illustrative) alongside its value. No data/logic changes here -- every
// number still comes straight from api.dashboard(id); this file only
// changes how it's laid out and classed.
'use strict';

(function () {
  const { h, replace, get, list } = window.dom;
  const api = window.api;

  // stateBadge maps a tile's state to one of app.css's existing badge
  // classes, so a not-yet-connected or failed tile reads visually
  // differently from a real number without adding new CSS: "ok" needs no
  // badge at all (the number speaks for itself), "not_connected" reads
  // like an amber "needs attention" badge, "unavailable" like an existing
  // red status-denied badge, and "illustrative" like a plain neutral badge.
  function stateBadge(S, tileState) {
    switch (tileState) {
      case 'ok': return null;
      case 'not_connected': return S.badge('Not connected', 'warn');
      case 'unavailable': return S.badge('Unavailable', 'status-denied');
      case 'illustrative': return S.badge('Illustrative', '');
      default: return S.badge(String(tileState || ''), '');
    }
  }

  function fmtNumber(n) {
    if (typeof n !== 'number' || !isFinite(n)) return '—';
    // Round to at most 2 decimals, dropping trailing zeros (6.25 stays
    // 6.25, 250000 stays 250000, not 250000.00).
    const r = Math.round(n * 100) / 100;
    return String(r);
  }

  // labelize turns a snake_case id into a plain sentence-case label
  // ("spend_by_application" -> "Spend by application"), matching
  // design-reference.html's own labels (its metric captions and breakdown
  // header are sentence case, not title case -- "Monthly burn", "Spend by
  // application", never "Monthly Burn").
  function labelize(id) {
    const words = String(id || '').split('_').filter(Boolean);
    if (!words.length) return '';
    return words.map((w, i) => (i === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w)).join(' ');
  }

  // KIND_CALLOUT maps a callout's Kind (compute.go's CalloutTile.Kind:
  // "margin_gap"/"cost_spike" for finance, "blocker" for delivery,
  // "silent_account" for clients) to an icon name from app.js's own
  // ICON_GLYPHS set plus a text-color class -- the callout is never color
  // alone, always an icon paired with coloured text (docs/slices/UI-polish.md,
  // "Callouts get an icon + coloured text ... never color alone").
  const KIND_CALLOUT = {
    margin_gap: ['alert-triangle', 'warn-text'],
    cost_spike: ['alert-triangle', 'warn-text'],
    blocker: ['lock', 'warn-text'],
    silent_account: ['mood-sad', 'warn-text'],
  };

  function metricTile(S, m) {
    const id = get(m, 'id'), tileState = get(m, 'state'), value = get(m, 'value');
    const badge = stateBadge(S, tileState);
    return h('div', { class: 'metric' },
      h('p', { class: 'k' }, labelize(id)),
      h('p', { class: 'v' }, tileState === 'ok' ? fmtNumber(value) : '—'),
      badge);
  }

  // barRow is one ranked breakdown line, matching design-reference.html's
  // .bar (this app's app.css has that same component under the name
  // .dr-bar -- .bar itself already means the bottom message-input bar in
  // this app, see app.css's own comment above .dr-bar). The fill can't be
  // an inline-styled <b style="width:...">, the way the reference does it:
  // dom.js's SAFE_ATTRS deliberately excludes "style" for every element it
  // builds (pinned by TestSafeAttrsStillRefusesStyle), so proportional
  // width here reuses this app's own existing safe mechanism for the same
  // job -- a <meter> element, exactly as view_workspaces.js's progress and
  // load bars already do (view_workspaces.js:150,181,372). The top-ranked
  // row's value is wrapped in an accent badge and every other row's is
  // plain text, so the one thing that matters reads in accent blue and the
  // rest in grey (the brief's "emphasis over rainbow"), without needing bar
  // color at all.
  function barRow(S, it, maxVal, isTop) {
    const label = get(it, 'label');
    const value = Number(get(it, 'value')) || 0;
    return h('div', { class: 'dr-bar' },
      h('span', { class: 'n' }, String(label || '')),
      h('span', { class: 't' }, h('meter', { min: 0, max: maxVal > 0 ? maxVal : 1, value: Math.max(0, value) })),
      h('span', { class: 'x' }, isTop ? S.badge(fmtNumber(value), 'accent') : fmtNumber(value)));
  }

  function breakdownCard(S, b) {
    const tileState = get(b, 'state');
    const items = list(get(b, 'items'));
    const badge = stateBadge(S, tileState);
    const label = labelize(get(b, 'id')) || 'Breakdown';
    if (tileState !== 'ok' || !items.length) {
      return h('div', { class: 'card' }, h('p', { class: 'label' }, label), badge || S.empty('No breakdown yet.'));
    }
    const maxVal = items.reduce((max, it) => Math.max(max, Number(get(it, 'value')) || 0), 0);
    return h('div', { class: 'card' },
      h('p', { class: 'label' }, label),
      items.map((it, i) => barRow(S, it, maxVal, i === 0)));
  }

  function calloutCard(S, c) {
    const tileState = get(c, 'state');
    const badge = stateBadge(S, tileState);
    if (tileState !== 'ok') {
      return h('div', { class: 'card' }, h('p', { class: 'label' }, 'Callout'), badge || S.empty('No callout yet.'));
    }
    const [iconName, colorCls] = KIND_CALLOUT[get(c, 'kind')] || ['alert-triangle', 'warn-text'];
    const title = get(c, 'title');
    return h('div', { class: 'card' },
      h('p', { class: 'label' }, 'Callout'),
      h('p', null,
        S.icon(iconName, colorCls),
        ' ',
        h('span', { class: colorCls }, title ? title + ': ' : ''),
        get(c, 'detail') || ''));
  }

  async function renderDashboardDetail(container, id, gen) {
    const S = window.appShared;
    let view;
    try {
      view = await api.dashboard(id);
    } catch (err) {
      if (S.current(gen)) replace(container, S.errorBox('Could not load this dashboard', err));
      return;
    }
    if (!S.current(gen)) return;
    const metrics = list(get(view, 'metrics'));
    const metricsRow = h('div', { class: 'metrics' }, metrics.map((m) => metricTile(S, m)));
    // Bento row: one wide card (the ranked breakdown) and one narrow card
    // (the callout), 12px gaps, per the brief's "Bento layout" direction.
    // .dashboard-tiles is this app's existing flex-row-with-12px-gap
    // class (app.css); reused here for its layout rule, not for its
    // .dashboard-tile child sizing -- each card below sizes to its own
    // content, which is what makes the breakdown card (several bar rows)
    // read wider than the callout card (one short line) without an
    // inline-styled fixed ratio, which dom.js does not allow.
    const bentoRow = h('div', { class: 'dashboard-tiles' },
      breakdownCard(S, get(view, 'breakdown')),
      calloutCard(S, get(view, 'callout')));
    replace(container,
      S.header(get(view, 'name') || id, get(view, 'source') || ''),
      metricsRow,
      bentoRow);
  }

  async function viewDashboards(c, param, gen) {
    const S = window.appShared;
    if (param) {
      c.appendChild(h('p', { class: 'loading' }, 'Loading…'));
      await renderDashboardDetail(c, param, gen);
      return;
    }
    const items = list(await api.dashboards());
    if (!S.current(gen)) return;
    if (!items.length) {
      replace(c, S.header('Dashboards', ''), S.empty('No dashboards are configured for this twin.'));
      return;
    }
    // Dense rows over a big bordered box per dashboard (cross-cutting
    // rule): .list/.row is the same flush-row list used by the Approvals
    // queue and Meetings/Threads reference sections, not .panel/.rows.
    const listEl = h('div', { class: 'list' });
    for (const d of items) {
      const id = get(d, 'id');
      listEl.appendChild(h('button', {
        type: 'button', class: 'row', on: { click: () => S.go('dashboards', id) },
      },
      h('span', { class: 'body' },
        h('p', { class: 'title' }, get(d, 'name') || id),
        h('p', { class: 'meta' }, get(d, 'source') || ''))));
    }
    replace(c, S.header('Dashboards', ''), listEl);
  }

  window.views = window.views || {};
  window.views.dashboards = viewDashboards;
})();
