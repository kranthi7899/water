// view_dashboards.js: the Dashboards page (docs/slices/UI.md Phase 2's
// list, Phase 4's detail). Registers window.views.dashboards. With no
// param it lists what twins/<id>/dashboards/*.yaml loaded (id, name, a
// human-readable source label); with a param (the dashboard id, from the
// URL hash) it also renders that one dashboard's actual computed tiles
// from api.dashboard(id) (internal/dashboards.Compute): three
// metrics, one breakdown and one callout, each carrying a tile state
// (ok/not_connected/unavailable/illustrative) alongside its value.
'use strict';

(function () {
  const { h, replace, get, list } = window.dom;
  const api = window.api;

  // stateBadgeKind maps a tile's state to one of app.css's existing badge
  // classes, so a not-yet-connected or failed tile reads visually
  // differently from a real number without adding new CSS: "ok" needs no
  // badge at all (the number speaks for itself), "not_connected" reads
  // like an amber "needs attention" badge, "unavailable" like an existing
  // red status-denied badge, and "illustrative" (unused by any tile this
  // phase computes, but the type exists for a later phase) like a plain
  // neutral badge.
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

  function metricLabel(id) {
    const words = String(id || '').split('_');
    return words.map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join(' ');
  }

  function metricTile(S, m) {
    const id = get(m, 'id'), tileState = get(m, 'state'), value = get(m, 'value');
    const badge = stateBadge(S, tileState);
    return h('div', { class: 'panel dashboard-tile' },
      h('div', { class: 'row-meta' }, metricLabel(id)),
      h('div', { class: 'dashboard-tile-value' }, tileState === 'ok' ? fmtNumber(value) : '—'),
      badge);
  }

  function breakdownPanel(S, b) {
    const tileState = get(b, 'state');
    const items = list(get(b, 'items'));
    const badge = stateBadge(S, tileState);
    if (tileState !== 'ok' || !items.length) {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Breakdown'), badge || S.empty('No breakdown yet.'));
    }
    const ul = h('ul', { class: 'rows' });
    for (const it of items) {
      ul.appendChild(h('li', null, h('div', { class: 'row row-static' },
        h('span', { class: 'row-main' }, h('span', { class: 'row-title' }, get(it, 'label'))),
        h('span', { class: 'row-meta' }, fmtNumber(get(it, 'value'))))));
    }
    return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Breakdown'), ul);
  }

  function calloutPanel(S, c) {
    const tileState = get(c, 'state');
    const badge = stateBadge(S, tileState);
    if (tileState !== 'ok') {
      return h('div', { class: 'panel' }, h('div', { class: 'row-meta' }, 'Callout'), badge || S.empty('No callout yet.'));
    }
    return h('div', { class: 'panel' },
      h('div', { class: 'row-meta' }, 'Callout'),
      h('div', { class: 'row-title' }, get(c, 'title') || ''),
      h('p', null, get(c, 'detail') || ''));
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
    const metricsRow = h('div', { class: 'dashboard-tiles' }, metrics.map((m) => metricTile(S, m)));
    replace(container,
      S.header(get(view, 'name') || id, get(view, 'source') || ''),
      metricsRow,
      breakdownPanel(S, get(view, 'breakdown')),
      calloutPanel(S, get(view, 'callout')));
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
    const ul = h('ul', { class: 'rows' });
    for (const d of items) {
      const id = get(d, 'id');
      ul.appendChild(h('li', null, h('button', {
        type: 'button', class: 'row', on: { click: () => S.go('dashboards', id) },
      },
      h('span', { class: 'row-main' },
        h('span', { class: 'row-title' }, get(d, 'name') || id),
        h('span', { class: 'row-meta' }, h('span', { class: 'muted' }, get(d, 'source') || ''))))));
    }
    replace(c, S.header('Dashboards', ''), h('div', { class: 'panel' }, ul));
  }

  window.views = window.views || {};
  window.views.dashboards = viewDashboards;
})();
