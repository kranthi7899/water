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
    approvals: (status, limit) => request('GET', '/v1/approvals?status=' + enc(status) + '&limit=' + enc(limit || 100)),
    approval: (id) => request('GET', '/v1/approvals/' + enc(id)),
    decideApproval: (id, payloadHash, reply) => request('POST', '/v1/approvals/' + enc(id) + '/decision', { payload_hash: payloadHash, reply }),
    threads: () => request('GET', '/v1/threads'),
    createThread: (title) => request('POST', '/v1/threads', { title }),
    anchorThread: (type, id) => request('POST', '/v1/threads/anchor', { anchor_type: type, anchor_id: id }),
    thread: (id) => request('GET', '/v1/threads/' + enc(id)),
    postThreadMessage: (id, text, onStart, onEvent) =>
      streamTurn('/v1/threads/' + enc(id) + '/messages', { text, channel: 'text-bar' }, onStart, onEvent),
    cancelTask: (id) => request('POST', '/v1/tasks/' + enc(id) + '/cancel'),
    meetings: (limit) => request('GET', '/v1/meetings?limit=' + enc(limit || 30)),
    meeting: (id) => request('GET', '/v1/meetings/' + enc(id)),
  };
})();
