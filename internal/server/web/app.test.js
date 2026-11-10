const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const crypto = require('node:crypto');
const vm = require('node:vm');

const HTMX_PATH = 'internal/server/web/htmx.min.js';
const APP_PATH = 'internal/server/web/app.js';
const PIN = 'd6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717';

function sha256(p) {
  return crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
}

function makeEnv() {
  const listeners = {};
  const htmx = {
    on: (ev, cb) => { listeners[ev] = cb; }
  };
  const elements = {
    'investigation': { querySelector: (sel) => sel === 'form' ? makeForm() : null },
    'results-region': { innerHTML: '', hidden: false, setAttribute: () => {}, removeAttribute: () => {} },
    'status': { textContent: '', appendChild: () => {} },
    'detail-region': { innerHTML: '' },
    'detail-heading': { focus: () => {} },
    'results-heading': { focus: () => {} }
  };
  const form = makeForm();
  function makeForm() {
    return {
      tagName: 'FORM',
      querySelector: (sel) => {
        if (sel === 'input[name="csrf"]') return { value: '' };
        if (sel === 'input[name="source"]') return { value: '' };
        if (sel === 'input[name="from"]') return { value: '' };
        if (sel === 'input[name="to"]') return { value: '' };
        if (sel === 'input[name="severity"]') return { value: '' };
        if (sel === 'input[name="term"]') return { value: '' };
        if (sel === 'button[type=submit]') return { disabled: false, setAttribute: () => {}, removeAttribute: () => {} };
        return null;
      }
    };
  }
  const cursors = [];
  const doc = {
    getElementById: (id) => elements[id] || null,
    querySelector: (sel) => {
      if (sel === 'input[name="generation"]') return { value: '1' };
      if (sel === 'input[name="sequence"]') return { value: '1' };
      return null;
    },
    querySelectorAll: (sel) => sel === 'input[name="cursor"]' ? cursors : [],
    createElement: () => ({ href: '', textContent: '' }),
    addEventListener: () => {}
  };
  const win = {
    addEventListener: () => {},
    location: { origin: 'http://localhost:8080', reload: () => {} }
  };
  const sandbox = {
    document: doc,
    window: win,
    htmx: htmx,
    WeakMap: WeakMap,
    URL: URL,
    location: win.location,
    console: { log: () => {} }
  };
  sandbox.window.location = win.location;
  return { sandbox, listeners, elements, form, cursors };
}

function focusFixture() {
  const env = makeEnv();
  const fields = {
    csrf: { value: 'tok' }, source: { value: 'src_demo_api' },
    from: { value: '2026-01-02T03:00:00Z' }, to: { value: '2026-01-02T03:15:00Z' },
    severity: { value: '9' }, term: { value: 'synthetic' }
  };
  const form = {
    tagName: 'FORM',
    querySelector: (sel) => {
      if (sel === 'input[name="csrf"]') return fields.csrf;
      if (sel === 'input[name="source"]') return fields.source;
      if (sel === 'input[name="from"]') return fields.from;
      if (sel === 'input[name="to"]') return fields.to;
      if (sel === 'input[name="severity"]') return fields.severity;
      if (sel === 'input[name="term"]') return fields.term;
      if (sel === 'button[type=submit]') return { disabled: false, setAttribute: () => {}, removeAttribute: () => {} };
      return null;
    }
  };
  env.elements['investigation'] = { querySelector: (sel) => sel === 'form' ? form : null };
  env.elements['results-region'].innerHTML = '<div id="protected-results">data</div>';
  env.elements['detail-region'].innerHTML = '<div id="protected-detail">data</div>';
  env.elements['status'].focusCount = 0;
  env.elements['status'].link = null;
  env.elements['status'].focus = () => { env.elements['status'].focusCount++; };
  env.elements['status'].appendChild = (el) => { env.elements['status'].link = el; };
  vm.runInNewContext(fs.readFileSync(APP_PATH, 'utf8'), env.sandbox);
  const fixture = { sandbox: env.sandbox, listeners: env.listeners, elements: env.elements, fields, focusCount: () => env.elements['status'].focusCount, link: () => env.elements['status'].link };
  fixture.start = (status) => {
    const xhr = { status, aborts: 0, abort: () => { xhr.aborts++; }, responseText: 'untrusted', getResponseHeader: () => '', responseURL: 'http://localhost:8080/investigate/search' };
    env.listeners['htmx:configRequest']({ detail: { parameters: {} }, preventDefault: () => {} });
    env.listeners['htmx:beforeRequest']({ detail: { xhr, requestConfig: { parameters: {}, path: '/investigate/search' } } });
    return xhr;
  };
  return fixture;
}

test('htmx pin', () => {
  assert.strictEqual(sha256(HTMX_PATH), PIN);
});

test('app.js configRequest and beforeRequest', () => {
  const { sandbox, listeners } = makeEnv();
  vm.runInNewContext(fs.readFileSync(APP_PATH, 'utf8'), sandbox);

  // configRequest: htmx 2.0.11 passes e.detail.parameters directly
  const cfgEvent = { detail: { parameters: {} }, preventDefault: () => {} };
  listeners['htmx:configRequest'](cfgEvent);
  assert.strictEqual(cfgEvent.detail.parameters.generation, '2');
  assert.strictEqual(cfgEvent.detail.parameters.sequence, '2');
  const src = fs.readFileSync(HTMX_PATH, 'utf8');
  const m = src.match(/const\s+C\s*=\s*\{[^}]*\}/);
  assert.ok(m, 'pinned C declaration');
  assert.ok(m[0].includes('parameters:Mn(w)'), 'C uses parameters');
  assert.ok(!m[0].includes('requestConfig'), 'C has no requestConfig');

  // beforeRequest: actual shape {xhr, requestConfig:{parameters, path}}
  const xhr = { status: 200, getResponseHeader: (h) => {
    if (h === 'Content-Type') return 'text/html';
    if (h === 'X-Generation') return '2';
    if (h === 'X-Sequence') return '2';
    if (h === 'X-Fragment-Kind') return 'detail';
    if (h === 'X-Selection') return '100';
    if (h === 'X-Origin') return '100';
    return '';
  }, responseURL: 'http://localhost:8080/investigate/detail' };

  const brEvent = { detail: { xhr, requestConfig: { parameters: { id: '100' }, path: '/detail' } } };
  listeners['htmx:beforeRequest'](brEvent);
  // beforeSwap valid
  let swapPrevented = false;
  const bsEvent = { detail: { xhr }, preventDefault: () => { swapPrevented = true; } };
  listeners['htmx:beforeSwap'](bsEvent);
  assert.strictEqual(swapPrevented, false);

  // Stale sequence refused
  xhr.getResponseHeader = (h) => {
    if (h === 'Content-Type') return 'text/html';
    if (h === 'X-Generation') return '2';
    if (h === 'X-Sequence') return '3'; // stale
    if (h === 'X-Fragment-Kind') return 'detail';
    if (h === 'X-Selection') return '100';
    if (h === 'X-Origin') return '100';
    return '';
  };
  let stalePrevented = false;
  listeners['htmx:beforeSwap']({ detail: { xhr }, preventDefault: () => { stalePrevented = true; } });
  assert.strictEqual(stalePrevented, true);

  // Cross-origin refused
  xhr.getResponseHeader = (h) => {
    if (h === 'Content-Type') return 'text/html';
    if (h === 'X-Generation') return '2';
    if (h === 'X-Sequence') return '2';
    if (h === 'X-Fragment-Kind') return 'detail';
    if (h === 'X-Selection') return '999';
    if (h === 'X-Origin') return '100';
    return '';
  };
  let wrongSelPrevented = false;
  listeners['htmx:beforeSwap']({ detail: { xhr }, preventDefault: () => { wrongSelPrevented = true; } });
  assert.strictEqual(wrongSelPrevented, true);

  xhr.getResponseHeader = (h) => {
    if (h === 'Content-Type') return 'text/html';
    if (h === 'X-Generation') return '2';
    if (h === 'X-Sequence') return '2';
    if (h === 'X-Fragment-Kind') return 'detail';
    if (h === 'X-Selection') return '100';
    if (h === 'X-Origin') return '100';
    return '';
  };
  xhr.responseURL = 'http://evil.com/investigate/detail';
  let crossPrevented = false;
  listeners['htmx:beforeSwap']({ detail: { xhr }, preventDefault: () => { crossPrevented = true; } });
  assert.strictEqual(crossPrevented, true);
});

test('non-auth focus: current/stale/unknown', () => {
  const cases = [
    ['htmx:afterRequest', 500],
    ['htmx:timeout', 0],
    ['htmx:sendError', 0]
  ];
  for (const [ev, st] of cases) {
    const f = focusFixture();
    const A = f.start(st);
    const B = f.start(st);
    assert.strictEqual(A.aborts, 1);
    assert.strictEqual(B.aborts, 0);
    assert.strictEqual(f.focusCount(), 0);
    assert.strictEqual(f.elements['status'].textContent, 'Searching...');
    f.listeners[ev]({ detail: { xhr: A } });
    f.listeners[ev]({ detail: { xhr: { status: st, abort: () => {} } } });
    assert.strictEqual(f.focusCount(), 0);
    assert.strictEqual(B.aborts, 0);
    f.listeners[ev]({ detail: { xhr: B } });
    assert.strictEqual(f.focusCount(), 1);
    assert.strictEqual(B.aborts, 1);
    const want = ev === 'htmx:timeout'
      ? 'Query timed out. Narrow the time window, then run Search.'
      : 'Request failed. Retry manually.';
    assert.strictEqual(f.elements['status'].textContent, want);
    assert.strictEqual(f.elements['results-region'].innerHTML, '<div id="protected-results">data</div>');
    assert.strictEqual(f.elements['detail-region'].innerHTML, '<div id="protected-detail">data</div>');
  }
});

test('422, 410, and 504 tell the user what to do', () => {
  const cases = [
    [422, 'Query too broad. Narrow the time window, then run Search.'],
    [410, 'Results expired. Rerun query.'],
    [504, 'Query timed out. Narrow the time window, then run Search.']
  ];
  for (const [st, text] of cases) {
    const env = makeEnv();
    const cursor = { value: 'signed-cursor' };
    env.cursors.push(cursor);
    env.elements['status'].focus = () => {};
    vm.runInNewContext(fs.readFileSync(APP_PATH, 'utf8'), env.sandbox);
    const xhr = {
      status: st,
      responseText: '<script>alert(1)</script>',
      abort: () => {},
      getResponseHeader: () => '',
      responseURL: 'http://localhost:8080/investigate/search'
    };
    env.listeners['htmx:configRequest']({ detail: { parameters: {} }, preventDefault: () => {} });
    env.listeners['htmx:beforeRequest']({ detail: { xhr, requestConfig: { parameters: {}, path: '/investigate/search' } } });
    env.listeners['htmx:afterRequest']({ detail: { xhr } });
    assert.strictEqual(env.elements['status'].textContent, text);
    assert.ok(!String(env.elements['status'].textContent).includes('<script>'));
    assert.strictEqual(env.elements['results-region'].innerHTML, '');
    if (st === 410) {
      assert.strictEqual(cursor.value, '');
    } else {
      assert.strictEqual(cursor.value, 'signed-cursor');
      assert.strictEqual(env.elements['results-region'].getAttribute && env.elements['results-region'].stale, undefined);
    }
  }
});

test('auth denial: 401/403 fail closed, idempotent, no override', () => {
  for (const st of [401, 403]) {
    const f = focusFixture();
    const A = f.start(st);
    const B = f.start(500);
    assert.strictEqual(A.aborts, 1);
    f.listeners['htmx:afterRequest']({ detail: { xhr: A } });
    assert.strictEqual(f.focusCount(), 1);
    assert.strictEqual(f.elements['status'].textContent, 'Session expired. ');
    assert.strictEqual(f.link().href, '/login');
    assert.strictEqual(f.elements['results-region'].innerHTML, '');
    assert.strictEqual(f.elements['detail-region'].innerHTML, '');
    assert.strictEqual(f.fields.csrf.value, '');
    assert.strictEqual(f.fields.source.value, '');
    assert.strictEqual(f.fields.from.value, '');
    assert.strictEqual(f.fields.to.value, '');
    assert.strictEqual(f.fields.severity.value, '');
    assert.strictEqual(f.fields.term.value, '');
    let prevented = false;
    f.listeners['htmx:beforeSwap']({ detail: { xhr: B }, preventDefault: () => { prevented = true; } });
    assert.strictEqual(prevented, true);
    f.listeners['htmx:afterRequest']({ detail: { xhr: B } });
    f.listeners['htmx:timeout']({ detail: { xhr: B } });
    assert.strictEqual(f.focusCount(), 1);
    assert.strictEqual(B.aborts, 1);
    assert.strictEqual(f.elements['status'].textContent, 'Session expired. ');
    f.listeners['htmx:afterRequest']({ detail: { xhr: { status: 401, abort: () => {} } } });
    assert.strictEqual(f.focusCount(), 1);
  }
});

test('status notices hit the live node', () => {
  const env = makeEnv();
  env.elements['status'].focus = () => {};
  env.elements['status'].setAttribute = () => {};
  vm.runInNewContext(fs.readFileSync(APP_PATH, 'utf8'), env.sandbox);
  const live = {
    textContent: 'Results',
    focusCount: 0,
    focus() { this.focusCount++; },
    setAttribute() {},
    appendChild(el) { this.link = el; }
  };
  env.elements['status'] = live;
  function fire(status, notice) {
    const xhr = {
      status,
      responseText: '<script>alert(1)</script>',
      abort() {},
      getResponseHeader: (h) => (h === 'X-Notice' ? notice : ''),
      responseURL: 'http://localhost:8080/investigate/search'
    };
    env.listeners['htmx:configRequest']({ detail: { parameters: {} }, preventDefault() {} });
    env.listeners['htmx:beforeRequest']({ detail: { xhr, requestConfig: { parameters: {}, path: '/investigate/search' } } });
    let prevented = false;
    env.listeners['htmx:beforeSwap']({ detail: { xhr }, preventDefault() { prevented = true; } });
    env.listeners['htmx:afterRequest']({ detail: { xhr } });
    return prevented;
  }
  assert.strictEqual(fire(422, ''), true);
  assert.strictEqual(live.textContent, 'Query too broad. Narrow the time window, then run Search.');
  assert.ok(live.focusCount >= 1);
  assert.ok(!live.textContent.includes('<script>'));
  assert.strictEqual(fire(410, ''), true);
  assert.strictEqual(live.textContent, 'Results expired. Rerun query.');
  assert.strictEqual(fire(504, ''), true);
  assert.strictEqual(live.textContent, 'Query timed out. Narrow the time window, then run Search.');
  assert.strictEqual(fire(503, ''), true);
  assert.strictEqual(live.textContent, 'Service busy. Wait, then run Search again.');
  assert.strictEqual(fire(400, 'invalid_query'), true);
  assert.strictEqual(live.textContent, 'Query is invalid');
  assert.strictEqual(fire(400, 'unsupported_path'), true);
  assert.strictEqual(live.textContent, 'Attribute path not supported');
  assert.strictEqual(fire(404, 'row_expired'), true);
  assert.strictEqual(live.textContent, 'Row expired. Rerun query. This row is past the retention window.');
  assert.strictEqual(fire(404, 'not_found'), true);
  assert.strictEqual(live.textContent, 'Event not found');
  assert.strictEqual(fire(400, 'constructor'), true);
  assert.strictEqual(live.textContent, 'Request failed. Retry manually.');
  assert.strictEqual(fire(400, 'toString'), true);
  assert.strictEqual(live.textContent, 'Request failed. Retry manually.');
  const kept = live.textContent;
  env.listeners['htmx:afterRequest']({
    detail: {
      xhr: {
        status: 400,
        abort() {},
        getResponseHeader: () => 'invalid_query',
        responseURL: 'http://localhost:8080/investigate/search'
      }
    }
  });
  assert.strictEqual(live.textContent, kept);
  const net = {
    status: 0,
    abort() {},
    getResponseHeader: () => '',
    responseURL: 'http://localhost:8080/investigate/search'
  };
  env.listeners['htmx:configRequest']({ detail: { parameters: {} }, preventDefault() {} });
  env.listeners['htmx:beforeRequest']({ detail: { xhr: net, requestConfig: { parameters: {}, path: '/investigate/search' } } });
  env.listeners['htmx:sendError']({ detail: { xhr: net } });
  assert.strictEqual(live.textContent, 'Request failed. Retry manually.');
  assert.strictEqual(fire(401, ''), true);
  assert.strictEqual(live.textContent, 'Session expired. ');
  assert.strictEqual(live.link.href, '/login');
  assert.strictEqual(live.link.textContent, 'Sign in');
});

test('notice focus survives a late results settle', () => {
  const env = makeEnv();
  let focused = '';
  env.elements['status'].focus = () => { focused = 'status'; };
  env.elements['results-heading'].focus = () => { focused = 'results-heading'; };
  env.elements['detail-heading'].focus = () => { focused = 'detail-heading'; };
  env.elements['event-7-open'] = { focus() { focused = 'event-7-open'; } };
  vm.runInNewContext(fs.readFileSync(APP_PATH, 'utf8'), env.sandbox);
  function header(kind, origin) {
    return (h) => {
      if (h === 'X-Fragment-Kind') return kind;
      if (h === 'X-Origin') return origin;
      return '';
    };
  }
  env.listeners['htmx:afterSwap']({ detail: { xhr: { getResponseHeader: header('results', '') } } });
  assert.strictEqual(focused, 'results-heading');
  const xhr = {
    status: 422,
    abort() {},
    getResponseHeader: () => '',
    responseURL: 'http://localhost:8080/investigate/search'
  };
  env.listeners['htmx:configRequest']({ detail: { parameters: {} }, preventDefault() {} });
  env.listeners['htmx:beforeRequest']({ detail: { xhr, requestConfig: { parameters: {}, path: '/investigate/search' } } });
  env.listeners['htmx:afterRequest']({ detail: { xhr } });
  assert.strictEqual(focused, 'status');
  env.elements['results-heading'].focus();
  assert.strictEqual(focused, 'results-heading');
  env.listeners['htmx:afterSettle']({});
  assert.strictEqual(focused, 'status');
  env.listeners['htmx:afterSwap']({ detail: { xhr: { getResponseHeader: header('results', '7') } } });
  assert.strictEqual(focused, 'event-7-open');
  env.listeners['htmx:afterSettle']({});
  assert.strictEqual(focused, 'event-7-open');
});

test('submit disables the submitter after the turn', () => {
  const env = makeEnv();
  const docListeners = {};
  env.sandbox.document.addEventListener = (ev, cb) => { docListeners[ev] = cb; };
  const timers = [];
  env.sandbox.setTimeout = (fn) => { timers.push(fn); };
  vm.runInNewContext(fs.readFileSync(APP_PATH, 'utf8'), env.sandbox);
  const first = { tagName: 'BUTTON', disabled: false, setAttribute() {}, isConnected: true };
  const clicked = { tagName: 'BUTTON', disabled: false, busy: false, setAttribute() { this.busy = true; }, isConnected: true };
  docListeners.submit({
    target: { tagName: 'FORM', querySelector: () => first },
    submitter: clicked
  });
  assert.strictEqual(clicked.disabled, false);
  assert.strictEqual(first.disabled, false);
  assert.strictEqual(timers.length, 1);
  timers[0]();
  assert.strictEqual(clicked.disabled, true);
  assert.strictEqual(clicked.busy, true);
  assert.strictEqual(first.disabled, false);
});

function liveEnv() {
  const elements = {};
  const listeners = { capture: {}, bubble: {} };
  const htmxListeners = {};
  const processed = [];
  function walk(n, fn) {
    for (const c of n.children || []) {
      fn(c);
      walk(c, fn);
    }
  }
  function match(n, sel) {
    if (sel === 'li') return n.tag === 'li';
    if (sel === '#live-batch') return n.id === 'live-batch';
    if (sel === '.live-filter') return n.className === 'live-filter';
    const idm = /^li\[data-ingest-id="([0-9]+)"\]$/.exec(sel);
    if (idm) return n.tag === 'li' && n.attrs['data-ingest-id'] === idm[1];
    const named = /^input\[name=(.+)\]$/.exec(sel);
    if (named) return n.tag === 'input' && n.name === named[1];
    const selected = /^select\[name=(.+)\]$/.exec(sel);
    if (selected) return n.tag === 'select' && n.name === selected[1];
    return false;
  }
  function make(tag, id) {
    const node = {
      tag, id: id || '', hidden: false, value: '', textContent: '', className: '',
      type: '', name: '', scrollTop: 0, children: [], parent: null, attrs: {}, options: [],
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
      removeAttribute(k) { delete this.attrs[k]; },
      appendChild(child) {
        if (child.parent) child.parent.removeChild(child);
        child.parent = this;
        this.children.push(child);
        if (child.id) elements[child.id] = child;
        return child;
      },
      insertBefore(child, ref) {
        if (child.parent) child.parent.removeChild(child);
        child.parent = this;
        const at = ref ? this.children.indexOf(ref) : 0;
        this.children.splice(at < 0 ? this.children.length : at, 0, child);
        if (child.id) elements[child.id] = child;
        return child;
      },
      removeChild(child) {
        const at = this.children.indexOf(child);
        if (at >= 0) this.children.splice(at, 1);
        if (child.parent === this) child.parent = null;
        return child;
      },
      remove() {
        if (this.parent) this.parent.removeChild(this);
        if (this.id && elements[this.id] === this) delete elements[this.id];
      },
      querySelector(sel) { return this.querySelectorAll(sel)[0] || null; },
      querySelectorAll(sel) {
        const out = [];
        walk(this, (n) => { if (match(n, sel)) out.push(n); });
        return out;
      },
      focus() { this.focused = (this.focused || 0) + 1; }
    };
    Object.defineProperty(node, 'firstChild', { get() { return this.children[0] || null; } });
    Object.defineProperty(node, 'lastChild', { get() { return this.children[this.children.length - 1] || null; } });
    Object.defineProperty(node, 'innerHTML', {
      get() { return ''; },
      set(v) { if (v === '') this.children.length = 0; }
    });
    if (node.id) elements[node.id] = node;
    return node;
  }
  const panel = make('section', 'live-panel');
  const status = make('p', 'live-status');
  const rows = make('ul', 'live-rows');
  const incoming = make('div', 'live-incoming');
  const form = make('form', 'live-form');
  const filters = make('form', 'filters');
  const client = make('input', 'live-client');
  const gen = make('input', 'live-gen');
  gen.value = '1';
  make('input', 'live-cursor-received').value = '0';
  make('input', 'live-cursor-id').value = '0';
  const asOf = make('input', 'live-as-of');
  asOf.value = '0';
  make('input', 'live-severity').value = '1';
  const pause = make('button', 'btn-live-pause');
  pause.hidden = true;
  const resume = make('button', 'btn-live-resume');
  resume.hidden = true;
  make('button', 'btn-live-reconnect').hidden = true;
  make('button', 'btn-live-start');
  make('a', 'btn-search-gap').hidden = true;
  make('div', 'live-ack');
  panel.appendChild(status);
  panel.appendChild(form);
  panel.appendChild(rows);
  panel.appendChild(incoming);
  const doc = {
    visibilityState: 'visible',
    getElementById: (id) => elements[id] || null,
    querySelector: () => null,
    querySelectorAll: () => [],
    createElement: (tag) => make(tag, ''),
    addEventListener(ev, cb, capture) {
      const bag = capture ? listeners.capture : listeners.bubble;
      (bag[ev] = bag[ev] || []).push(cb);
    }
  };
  const kicks = [];
  const sandbox = {
    document: doc,
    window: { addEventListener() {}, location: { origin: 'http://localhost:8080' } },
    htmx: {
      on: (ev, cb) => { htmxListeners[ev] = cb; },
      process: (node) => { processed.push(node); },
      remove: (node) => { if (node && node.remove) node.remove(); },
      trigger: (el, name) => { kicks.push(name); }
    },
    WeakMap, WeakSet, URL, Uint8Array,
    location: { origin: 'http://localhost:8080' },
    console: { log() {} },
    crypto: { getRandomValues(buf) { for (let i = 0; i < buf.length; i++) buf[i] = i + 1; return buf; } },
    setTimeout(fn) { sandbox.timers.push(fn); return sandbox.timers.length; },
    clearTimeout() {}
  };
  sandbox.timers = [];
  vm.runInNewContext(fs.readFileSync(APP_PATH, 'utf8'), sandbox);
  function fire(ev, target) {
    const e = { target, preventDefault() { e.defaultPrevented = true; } };
    for (const cb of listeners.capture[ev] || []) cb(e);
    for (const cb of listeners.bubble[ev] || []) cb(e);
    return e;
  }
  function xhr(headers) {
    return {
      status: 200,
      responseURL: 'http://localhost:8080/investigate/live',
      headers: Object.assign({ 'Content-Type': 'text/html', 'X-Live-Gen': gen.value }, headers || {}),
      getResponseHeader(name) { return this.headers[name] || ''; },
      abort() { this.aborted = true; }
    };
  }
  function request(path, intent, req) {
    let prevented = false;
    htmxListeners['htmx:configRequest']({
      detail: { path, parameters: { intent } },
      preventDefault() { prevented = true; }
    });
    htmxListeners['htmx:beforeRequest']({
      detail: { xhr: req, requestConfig: { path, parameters: { intent } } },
      preventDefault() { prevented = true; }
    });
    return prevented;
  }
  function batch(ids, idle, cursor) {
    incoming.innerHTML = '';
    const root = make('div', 'live-batch');
    root.setAttribute('data-cursor-received', cursor.recv);
    root.setAttribute('data-cursor-id', cursor.id);
    root.setAttribute('data-idle', idle ? '1' : '0');
    const ul = make('ul', '');
    root.appendChild(ul);
    for (const id of ids) {
      const li = make('li', '');
      li.setAttribute('data-ingest-id', String(id));
      ul.appendChild(li);
    }
    incoming.appendChild(root);
  }
  return { elements, doc, htmxListeners, processed, fire, sandbox, rows, status, pause, resume, client, gen, incoming, filters, form, xhr, request, batch, kicks, asOf };
}

test('live polling backs off, pauses, dedups and prunes', () => {
  const env = liveEnv();
  assert.strictEqual(env.client.value.length, 16);
  env.fire('click', env.elements['btn-live-start']);
  const start = env.xhr();
  assert.strictEqual(env.request('/investigate/live', 'start', start), false);
  env.elements['live-ack'].textContent = 'Live: Receiving';
  env.elements['live-ack'].setAttribute('data-state', 'receiving');
  env.elements['live-ack'].setAttribute('data-cursor-received', '100');
  env.elements['live-ack'].setAttribute('data-cursor-id', '9');
  env.htmxListeners['htmx:afterSwap']({ detail: { xhr: start, target: { id: 'live-ack' } } });
  const trig = env.elements['live-trigger'];
  assert.ok(trig, 'trigger mounted');
  assert.strictEqual(trig.getAttribute('hx-trigger'), 'every 1000ms, live-now');
  assert.strictEqual(trig.getAttribute('hx-sync'), '#live-trigger:drop');
  assert.strictEqual(trig.getAttribute('hx-vals'), '{"intent":"poll"}');
  assert.strictEqual(env.status.textContent, 'Live: Receiving');

  env.batch([7, 7], false, { recv: '100', id: '11' });
  const one = env.xhr({ 'X-Live-Cursor-Received': '100', 'X-Live-Cursor-Id': '11', 'X-Live-Idle': '0' });
  assert.strictEqual(env.request('/investigate/live', 'poll', one), false);
  env.htmxListeners['htmx:afterSwap']({ detail: { xhr: one, target: { id: 'live-incoming' } } });
  assert.strictEqual(env.rows.children.length, 1);
  assert.strictEqual(env.elements['live-cursor-id'].value, '11');

  env.rows.children.length = 0;
  const ids = [];
  for (let n = 1; n <= 1005; n++) ids.push(n);
  env.batch(ids, false, { recv: '100', id: '2000' });
  const many = env.xhr({ 'X-Live-Cursor-Received': '100', 'X-Live-Cursor-Id': '2000', 'X-Live-Idle': '0' });
  env.request('/investigate/live', 'poll', many);
  env.htmxListeners['htmx:afterSwap']({ detail: { xhr: many, target: { id: 'live-incoming' } } });
  assert.strictEqual(env.rows.children.length, 1000);
  assert.strictEqual(env.rows.firstChild.getAttribute('data-ingest-id'), '1005');
  assert.strictEqual(env.rows.lastChild.getAttribute('data-ingest-id'), '6');

  env.batch([], true, { recv: '100', id: '2000' });
  const idle = env.xhr({ 'X-Live-Idle': '1', 'X-Live-Cursor-Received': '100', 'X-Live-Cursor-Id': '2000' });
  env.request('/investigate/live', 'poll', idle);
  env.htmxListeners['htmx:afterSwap']({ detail: { xhr: idle, target: { id: 'live-incoming' } } });
  assert.strictEqual(env.elements['live-trigger'].getAttribute('hx-trigger'), 'every 2000ms, live-now');
  const idleAgain = env.xhr({ 'X-Live-Idle': '1', 'X-Live-Caught': '1', 'X-Live-Cursor-Received': '100', 'X-Live-Cursor-Id': '2000' });
  env.request('/investigate/live', 'poll', idleAgain);
  env.htmxListeners['htmx:afterSwap']({ detail: { xhr: idleAgain, target: { id: 'live-incoming' } } });
  assert.strictEqual(env.elements['live-trigger'].getAttribute('data-every'), '2000');

  env.doc.visibilityState = 'hidden';
  env.fire('visibilitychange', env.doc);
  assert.strictEqual(env.elements['live-trigger'], undefined);
  assert.strictEqual(env.status.textContent, 'Live: Paused');
  env.doc.visibilityState = 'visible';
  env.fire('visibilitychange', env.doc);
  assert.ok(env.elements['live-trigger'].getAttribute('hx-vals').includes('resume'));
  env.batch([1006], false, { recv: '100', id: '2001' });
  const back = env.xhr({ 'X-Live-Idle': '0', 'X-Live-Cursor-Received': '100', 'X-Live-Cursor-Id': '2001' });
  env.request('/investigate/live', 'resume', back);
  env.htmxListeners['htmx:afterSwap']({ detail: { xhr: back, target: { id: 'live-incoming' } } });
  assert.ok(env.elements['live-trigger'].getAttribute('hx-vals').includes('resume'));

  env.rows.scrollTop = 40;
  env.fire('scroll', env.rows);
  assert.strictEqual(env.elements['live-trigger'], undefined);
  assert.strictEqual(env.status.textContent, 'Live: Paused');
  env.rows.scrollTop = 0;
  env.fire('scroll', env.rows);
  assert.ok(env.elements['live-trigger']);
  env.fire('click', env.elements['btn-live-pause']);
  assert.strictEqual(env.status.textContent, 'Live: Paused');
  assert.strictEqual(env.pause.hidden, true);
  assert.strictEqual(env.resume.hidden, false);
  assert.strictEqual(env.elements['live-trigger'], undefined);
});

test('a short page polls again and a finished request does not stick', () => {
  const env = liveEnv();
  env.fire('click', env.elements['btn-live-start']);
  env.batch([8], false, { recv: '40', id: '8' });
  const behind = env.xhr({
    'X-Live-Idle': '0',
    'X-Live-Caught': '0',
    'X-Live-As-Of': '4242',
    'X-Live-Cursor-Received': '40',
    'X-Live-Cursor-Id': '8'
  });
  behind.readyState = 1;
  assert.strictEqual(env.request('/investigate/live', 'poll', behind), false);
  env.htmxListeners['htmx:afterSwap']({ detail: { xhr: behind, target: { id: 'live-incoming' } } });
  assert.ok(env.kicks.includes('live-now'));
  assert.strictEqual(env.asOf.value, '4242');
  assert.strictEqual(env.elements['live-trigger'].getAttribute('data-every'), '1000');

  const stuck = env.xhr({});
  stuck.readyState = 1;
  assert.strictEqual(env.request('/investigate/live', 'poll', stuck), false);
  stuck.readyState = 4;
  const next = env.xhr({});
  next.readyState = 1;
  assert.strictEqual(env.request('/investigate/live', 'poll', next), false);

  env.fire('click', env.elements['btn-live-pause']);
  env.htmxListeners['htmx:sendError']({ detail: { xhr: next } });
  assert.strictEqual(env.status.textContent, 'Live: Paused');
  env.fire('click', env.elements['btn-live-resume']);
  const resumed = env.xhr({});
  resumed.readyState = 1;
  assert.strictEqual(env.request('/investigate/live', 'resume', resumed), false);
});