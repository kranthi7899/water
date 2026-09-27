// dom.js: the only way this UI builds DOM. Every string becomes a text node;
// attributes go through a fixed allowlist of inert names; behaviour is
// attached with addEventListener. Server-provided text is attacker
// controlled (mail bodies, decision evidence, meeting recaps), so nothing
// here ever parses a string as markup.
'use strict';

(function () {
  // Attributes that can never carry script or a URL.
  const SAFE_ATTRS = new Set([
    'type', 'title', 'role', 'tabindex', 'placeholder', 'rows', 'cols', 'name',
    'for', 'id', 'lang', 'dir', 'maxlength', 'autocomplete', 'spellcheck', 'datetime',
  ]);
  const BOOL_PROPS = new Set(['disabled', 'hidden', 'checked', 'readOnly', 'required']);

  function setAttr(el, k, v) {
    if (v === undefined || v === null || v === false) return;
    if (k === 'class') { el.className = String(v); return; }
    if (k === 'value') { el.value = String(v); return; }
    if (BOOL_PROPS.has(k)) { el[k] = Boolean(v); return; }
    if (k === 'on') {
      for (const [ev, fn] of Object.entries(v)) el.addEventListener(ev, fn);
      return;
    }
    if (k === 'data') {
      for (const [dk, dv] of Object.entries(v)) el.dataset[dk] = String(dv);
      return;
    }
    if (k.startsWith('aria-') || SAFE_ATTRS.has(k)) {
      el.setAttribute(k, String(v));
      return;
    }
    throw new Error('dom: attribute not allowed: ' + k);
  }

  function append(el, child) {
    if (child === undefined || child === null || child === false) return;
    if (Array.isArray(child)) { for (const c of child) append(el, c); return; }
    if (child instanceof Node) { el.appendChild(child); return; }
    el.appendChild(document.createTextNode(String(child)));
  }

  // h('div', {class: 'x'}, 'text', otherNode, [more]) builds an element.
  // Strings are always text, never markup.
  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) setAttr(el, k, v);
    append(el, children);
    return el;
  }

  function clear(el) {
    while (el.firstChild) el.removeChild(el.firstChild);
  }

  function replace(el, ...children) {
    clear(el);
    append(el, children);
  }

  // Server records don't all use one key style: the decision card and
  // needs-you item encode Go field names ("ID", "Lead"), everything newer is
  // snake_case. get(o, 'ID', 'id') returns the first key present.
  function get(o, ...keys) {
    if (!o) return undefined;
    for (const k of keys) if (o[k] !== undefined && o[k] !== null) return o[k];
    return undefined;
  }

  function list(v) { return Array.isArray(v) ? v : []; }

  const dateFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
  const timeFmt = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' });
  const dayFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });

  function parseTime(s) {
    if (!s) return null;
    const d = new Date(s);
    if (isNaN(d.getTime()) || d.getFullYear() < 1971) return null;
    return d;
  }
  function fmtDate(s) { const d = parseTime(s); return d ? dateFmt.format(d) : ''; }
  function fmtTime(s) { const d = parseTime(s); return d ? timeFmt.format(d) : ''; }
  // fmtDay is fmtDate without the time, for a plain "Due Oct 15" chip
  // (docs/slices/UI.md Phase 0b): the exact minute a decision is due is
  // rarely the point, and dropping it reads as calmer, plainer language.
  function fmtDay(s) { const d = parseTime(s); return d ? dayFmt.format(d) : ''; }
  function fmtAgo(s) {
    const d = parseTime(s);
    if (!d) return '';
    const sec = Math.round((Date.now() - d.getTime()) / 1000);
    const abs = Math.abs(sec);
    const unit = abs < 60 ? [sec, 's'] : abs < 3600 ? [Math.round(sec / 60), 'm'] : abs < 86400 ? [Math.round(sec / 3600), 'h'] : [Math.round(sec / 86400), 'd'];
    return unit[0] >= 0 ? unit[0] + unit[1] + ' ago' : 'in ' + (-unit[0]) + unit[1];
  }

  // untrusted renders server text an outsider could have written, as plain
  // pre-wrapped text in a visibly distinct "quoted" block.
  function untrusted(label, text) {
    return h('figure', { class: 'untrusted' },
      h('figcaption', null, label),
      h('blockquote', { class: 'quoted' }, String(text || '')));
  }

  window.dom = { h, clear, replace, get, list, fmtDate, fmtTime, fmtDay, fmtAgo, untrusted };
})();
