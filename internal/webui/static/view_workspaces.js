// view_workspaces.js: the Workspaces sidebar disclosure and its main-pane
// view (docs/slices/UI.md Phase 2). This phase only builds the routing
// shape and a placeholder render for one workspace's page — the six full
// views are Phase 3's job and a real per-workspace page (finance, a
// project, ...) is Phase 5's — so this file stays deliberately small:
// enough to list what twins/<id>/workspaces/*.yaml loaded and navigate,
// nothing about a workspace's actual content.
//
// The hash route is "workspaces" with an optional two-segment param,
// "<id>/<sub>" (e.g. "#workspaces/finance/overview", parsed by app.js's
// parseHash and built by goWorkspace). SUBS lists the sub-pages this phase
// knows about; DEFAULT_WORKSPACE_SUB (window.appShared, from app.js) is
// "overview", so a bare "#workspaces/finance" (or a click from the list/nav,
// which never itself sends a sub) lands on the same place a full "/overview"
// would.
'use strict';

(function () {
  const { h, replace, get, list } = window.dom;
  const api = window.api;

  // SUBS is every sub-page this phase's placeholder knows how to switch
  // between. There is exactly one today; a later phase (5) adds the real
  // ones (activity, people, ...) per template.
  const SUBS = [{ id: 'overview', label: 'Overview' }];

  function splitParam(param) {
    const s = String(param || '');
    const i = s.indexOf('/');
    return i < 0 ? { id: s, sub: '' } : { id: s.slice(0, i), sub: s.slice(i + 1) };
  }

  async function viewWorkspaces(c, param, gen) {
    const S = window.appShared;
    const items = list(await api.workspaces());
    if (!S.current(gen)) return;
    renderNavList(items);

    const { id, sub } = splitParam(param);
    if (!id) {
      replace(c, S.header('Workspaces', 'Grouped views over one project, client set or domain.'), workspacesList(items));
      return;
    }
    const spec = items.find((w) => get(w, 'id') === id);
    if (!spec) {
      replace(c, S.header('Workspaces', ''), S.errorBox('Could not open this workspace', new Error('No workspace named "' + id + '"')));
      return;
    }
    replace(c, S.header(get(spec, 'name') || id, workspaceSubtitle(spec)), workspaceDetail(spec, sub || S.DEFAULT_WORKSPACE_SUB));
  }

  function workspaceSubtitle(spec) {
    return 'Template: ' + (get(spec, 'template') || '—') + ' · Source: ' + (get(spec, 'source') || '—');
  }

  function workspacesList(items) {
    const S = window.appShared;
    if (!items.length) return S.empty('No workspaces are configured for this twin.');
    const ul = h('ul', { class: 'rows' });
    for (const w of items) {
      const id = get(w, 'id');
      ul.appendChild(h('li', null, h('button', {
        type: 'button', class: 'row', on: { click: () => S.goWorkspace(id, S.DEFAULT_WORKSPACE_SUB) },
      },
      h('span', { class: 'row-main' },
        h('span', { class: 'row-title' }, get(w, 'name') || id),
        h('span', { class: 'row-meta' },
          S.badge(get(w, 'template') || ''),
          h('span', { class: 'muted' }, get(w, 'source') || ''))))));
    }
    return h('div', { class: 'panel' }, ul);
  }

  function workspaceDetail(spec, sub) {
    const S = window.appShared;
    const id = get(spec, 'id');
    const tabs = h('div', { class: 'tabs', role: 'tablist' });
    for (const s of SUBS) {
      tabs.appendChild(h('button', {
        type: 'button', role: 'tab', class: 'tab' + (s.id === sub ? ' active' : ''),
        'aria-selected': s.id === sub ? 'true' : 'false',
        on: { click: () => S.goWorkspace(id, s.id) },
      }, s.label));
    }
    return h('div', { class: 'stack' },
      tabs,
      h('section', { class: 'panel' },
        S.empty('The full ' + (get(spec, 'template') || '') + ' workspace view is not built yet (docs/slices/UI.md Phase 5). This page only confirms routing and navigation.')));
  }

  // renderNavList fills the sidebar's Workspaces disclosure (index.html's
  // #workspaces-nav-list), highlighting the one open in the main pane, if
  // any. Called both at boot (window.views._workspaceNav) and on every
  // render of this view, so the highlight always matches what's on screen.
  function renderNavList(items) {
    const S = window.appShared;
    const el = document.getElementById('workspaces-nav-list');
    if (!el) return;
    const currentID = S.state.view === 'workspaces' ? splitParam(S.state.param).id : '';
    replace(el, list(items).map((w) => {
      const id = get(w, 'id');
      return h('li', null, h('button', {
        type: 'button', class: 'nav-sub-item' + (id === currentID ? ' active' : ''),
        title: get(w, 'name') || id,
        on: { click: () => S.goWorkspace(id, S.DEFAULT_WORKSPACE_SUB) },
      }, get(w, 'name') || id));
    }));
    if (!list(items).length) el.appendChild(h('li', { class: 'muted small' }, 'None yet'));
  }

  // _workspaceNav is boot's one-time, best-effort load of the sidebar
  // disclosure (workspaces rarely change while the daemon runs — they load
  // once from YAML at startup — so unlike Recent threads this is not on the
  // periodic refresh timer). It fails silently, exactly like refreshRecent.
  async function loadWorkspaceNav() {
    try { renderNavList(await api.workspaces()); } catch (_) { /* best-effort */ }
  }

  window.views = window.views || {};
  window.views.workspaces = viewWorkspaces;
  window.views._workspaceNav = loadWorkspaceNav;
})();
