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
  const doc = {
    getElementById: (id) => elements[id] || null,
    querySelector: (sel) => {
      if (sel === 'input[name="generation"]') return { value: '1' };
      if (sel === 'input[name="sequence"]') return { value: '1' };
      return null;
    },
    querySelectorAll: () => [],
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
  return { sandbox, listeners, elements, form };
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
    assert.strictEqual(f.elements['status'].textContent, '');
    f.listeners[ev]({ detail: { xhr: A } });
    f.listeners[ev]({ detail: { xhr: { status: st, abort: () => {} } } });
    assert.strictEqual(f.focusCount(), 0);
    assert.strictEqual(B.aborts, 0);
    f.listeners[ev]({ detail: { xhr: B } });
    assert.strictEqual(f.focusCount(), 1);
    assert.strictEqual(B.aborts, 1);
    assert.strictEqual(f.elements['status'].textContent, 'Request failed. Retry manually.');
    assert.strictEqual(f.elements['results-region'].innerHTML, '<div id="protected-results">data</div>');
    assert.strictEqual(f.elements['detail-region'].innerHTML, '<div id="protected-detail">data</div>');
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