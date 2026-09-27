// view_dashboards.js: the Dashboards page (docs/slices/UI.md Phase 2).
// Registers window.views.dashboards. This phase only lists what
// twins/<id>/dashboards/*.yaml loaded (id, name, a human-readable source
// label, from api.dashboards()) — the actual metrics, breakdown and
// callout need Phase 4's compute registry, which does not exist yet, so
// there is deliberately no per-dashboard detail route in this phase.
'use strict';

(function () {
  const { h, replace, get, list } = window.dom;
  const api = window.api;

  async function viewDashboards(c, _param, gen) {
    const S = window.appShared;
    const items = list(await api.dashboards());
    if (!S.current(gen)) return;
    if (!items.length) {
      replace(c, S.header('Dashboards', ''), S.empty('No dashboards are configured for this twin.'));
      return;
    }
    const ul = h('ul', { class: 'rows' });
    for (const d of items) {
      ul.appendChild(h('li', null,
        h('div', { class: 'row row-static' },
          h('span', { class: 'row-main' },
            h('span', { class: 'row-title' }, get(d, 'name') || get(d, 'id')),
            h('span', { class: 'row-meta' }, h('span', { class: 'muted' }, get(d, 'source') || ''))))));
    }
    replace(c, S.header('Dashboards', 'The numbers behind each of these come in a later phase.'), h('div', { class: 'panel' }, ul));
  }

  window.views = window.views || {};
  window.views.dashboards = viewDashboards;
})();
