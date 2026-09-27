// api.js: calls into the daemon. Every path is root-relative ("/v1/..."), so
// in the macOS app it resolves to the page's own water:// origin and goes
// through the native scheme handler, which adds the bearer token and
// enforces its path allowlist. This file never sees or sends a token.
'use strict';

(function () {
  class APIError extends Error {
    constructor(status, message) {
      super(message || ('HTTP ' + status));
      this.status = status;
    }
  }

  async function request(method, path, body) {
    const init = { method, headers: { 'Accept': 'application/json' }, credentials: 'same-origin', cache: 'no-store' };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    const resp = await fetch(path, init);
    if (!resp.ok) {
      let msg = '';
      try { msg = (await resp.text()).trim(); } catch (_) { /* ignore */ }
      throw new APIError(resp.status, msg || ('HTTP ' + resp.status));
    }
    const text = await resp.text();
    return text ? JSON.parse(text) : null;
  }

  const enc = encodeURIComponent;

  // streamTurn posts to an NDJSON turn endpoint and calls onEvent for each
  // event as it arrives (ack, queued, delta, sentence, approval_required,
  // done, error). onStart receives the task id (for cancel) as soon as the
  // headers are in. Resolves when the stream ends.
  async function streamTurn(path, body, onStart, onEvent) {
    const resp = await fetch(path, {
      method: 'POST', credentials: 'same-origin', cache: 'no-store',
      headers: { 'Content-Type': 'application/json', 'Accept': 'application/x-ndjson' },
      body: JSON.stringify(body),
    });
    if (!resp.ok) {
      let msg = '';
      try { msg = (await resp.text()).trim(); } catch (_) { /* ignore */ }
      throw new APIError(resp.status, msg || ('HTTP ' + resp.status));
    }
    if (onStart) onStart(resp.headers.get('X-Water-Task-Id') || '');
    const emitLine = (line) => {
      line = line.trim();
      if (!line) return;
      let ev;
      try { ev = JSON.parse(line); } catch (_) { return; }
      if (ev && typeof ev === 'object') onEvent(ev);
    };
    if (!resp.body || !resp.body.getReader) {
      for (const line of (await resp.text()).split('\n')) emitLine(line);
      return;
    }
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf('\n')) >= 0) {
        emitLine(buf.slice(0, i));
        buf = buf.slice(i + 1);
      }
    }
    buf += decoder.decode();
    emitLine(buf);
  }

  window.api = {
    APIError,
    today: () => request('GET', '/v1/today'),
    decisions: () => request('GET', '/v1/decisions'),
    stageDecision: (id, fn, payload) => request('POST', '/v1/decisions/' + enc(id) + '/stage', { function: fn, payload }),
    dismissDecision: (id, reason) => request('POST', '/v1/decisions/' + enc(id) + '/dismiss', { reason }),
    // Phase 3b: the per-action stage route (docs/slices/UI.md), keyed by a
    // decision card's own Suggestion.ID rather than a function name, so two
    // suggestions on the same card stage into two independent envelopes.
    stageDecisionAction: (id, actionId, payload) => request('POST', '/v1/decisions/' + enc(id) + '/actions/' + enc(actionId) + '/stage', { payload }),
    relatedDecision: (id) => request('GET', '/v1/decisions/' + enc(id) + '/related'),
    approvals: (status, limit, kind) => request('GET', '/v1/approvals?status=' + enc(status) + '&limit=' + enc(limit || 100) + (kind ? '&kind=' + enc(kind) : '')),
    approval: (id) => request('GET', '/v1/approvals/' + enc(id)),
    decideApproval: (id, payloadHash, reply) => request('POST', '/v1/approvals/' + enc(id) + '/decision', { payload_hash: payloadHash, reply }),
    // Edit voids the envelope and answers {voided, envelope}: the new,
    // pending one needs its own yes. Nothing runs by editing.
    editApproval: (id, payloadHash, payload) => request('POST', '/v1/approvals/' + enc(id) + '/edit', { payload_hash: payloadHash, payload }),
    // Phase 3a: a person-request approval's "Request changes" button. Denies
    // the original (reason "changes requested") and proposes a fresh
    // gmail.send_message reply to the requester; nothing is sent by this
    // call itself.
    requestChanges: (id, note) => request('POST', '/v1/approvals/' + enc(id) + '/request-changes', { note }),
    threads: () => request('GET', '/v1/threads'),
    createThread: (title) => request('POST', '/v1/threads', { title }),
    anchorThread: (type, id) => request('POST', '/v1/threads/anchor', { anchor_type: type, anchor_id: id }),
    thread: (id) => request('GET', '/v1/threads/' + enc(id)),
    postThreadMessage: (id, text, onStart, onEvent) =>
      streamTurn('/v1/threads/' + enc(id) + '/messages', { text, channel: 'text-bar' }, onStart, onEvent),
    cancelTask: (id) => request('POST', '/v1/tasks/' + enc(id) + '/cancel'),
    meetings: (limit) => request('GET', '/v1/meetings?limit=' + enc(limit || 30)),
    meeting: (id) => request('GET', '/v1/meetings/' + enc(id)),
    // Phase 3d: upcoming meetings come from the events table, not
    // meeting_sessions (docs/slices/UI.md Phase 3d) -- a separate query on
    // the same endpoint, never a new route.
    upcomingMeetings: (limit) => request('GET', '/v1/meetings?upcoming=1&limit=' + enc(limit || 10)),
    // Slice UI Phase 2: the sidebar's Dashboards page and Workspaces
    // disclosure ({id, name, template, source} / {id, name, source}).
    workspaces: () => request('GET', '/v1/workspaces'),
    // Phase 5a: one workspace's filtered existing sections plus its own
    // control-room tiles (internal/gateway/workspace_detail.go).
    workspace: (id) => request('GET', '/v1/workspaces/' + enc(id)),
    dashboards: () => request('GET', '/v1/dashboards'),
    // Phase 4: one dashboard's actual computed tiles.
    dashboard: (id) => request('GET', '/v1/dashboards/' + enc(id)),
    // Phase 3c: the Drafts editor (U10-A, a real drafts table -- see
    // view_drafts.js). Save persists to/subject/body only; submitDraft
    // always sends the editor's current values (never relies on a prior
    // save), which is what makes "send exactly what's on screen" hold even
    // with unsaved edits.
    drafts: () => request('GET', '/v1/drafts'),
    draft: (id) => request('GET', '/v1/drafts/' + enc(id)),
    saveDraft: (id, to, subject, body) => request('POST', '/v1/drafts/' + enc(id), { to, subject, body }),
    submitDraft: (id, to, subject, body) => request('POST', '/v1/drafts/' + enc(id) + '/submit', { to, subject, body }),
  };
})();
