'use strict';

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const BASE = process.env.BASE || '';
const TOKEN = process.env.READ_TOKEN || '';
const CHROME = process.env.CHROME || '';
const AGING_FROM = process.env.AGING_FROM || '';
const AGING_TO = process.env.AGING_TO || '';
const SHOT_DIR = process.env.E2E_SHOT_DIR || '';

function say(s) { fs.writeSync(1, s + '\n'); }

const pendingLines = [];
let lineWait = null;
process.stdin.setEncoding('utf8');
let stdinBuf = '';
process.stdin.on('data', (chunk) => {
  stdinBuf += chunk;
  let nl;
  while ((nl = stdinBuf.indexOf('\n')) >= 0) {
    const line = stdinBuf.slice(0, nl);
    stdinBuf = stdinBuf.slice(nl + 1);
    if (lineWait) {
      const w = lineWait;
      lineWait = null;
      w(line);
    } else {
      pendingLines.push(line);
    }
  }
});
function nextLine() {
  if (pendingLines.length) return Promise.resolve(pendingLines.shift());
  return new Promise((resolve) => { lineWait = resolve; });
}

function sleep(ms) { return new Promise((r) => setTimeout(r, ms)); }

function redact(s) {
  return String(s)
    .replace(/token=[^&\s"']+/gi, 'token=REDACTED')
    .replace(/csrf=[^&\s"']+/gi, 'csrf=REDACTED')
    .replace(/hl_session=[^;\s"']+/gi, 'hl_session=REDACTED');
}

function oneLine(s, n) {
  return redact(s).replace(/\s+/g, ' ').slice(0, n || 500);
}

function headerValue(headers, name) {
  if (!headers) return '';
  const want = name.toLowerCase();
  for (const key of Object.keys(headers)) {
    if (key.toLowerCase() === want) return headers[key];
  }
  return '';
}

const noJS = new Set();
const lastNet = new Map();

function noteNet(msg) {
  const sid = msg.sessionId || '';
  const p = msg.params || {};
  if (msg.method === 'Network.requestWillBeSent') {
    const url = (p.request && p.request.url) || '';
    if (url.indexOf('/investigate') === -1 && url.indexOf('/login') === -1) return;
    lastNet.set(sid, {
      id: p.requestId,
      method: p.request.method,
      url: url,
      post: p.request.postData || '',
      status: '',
      headers: {},
      done: false
    });
    return;
  }
  const rec = lastNet.get(sid);
  if (!rec || !p.requestId || rec.id !== p.requestId) return;
  if (msg.method === 'Network.responseReceived') {
    rec.status = p.response ? p.response.status : '';
    rec.headers = (p.response && p.response.headers) || {};
  } else if (msg.method === 'Network.loadingFinished' || msg.method === 'Network.loadingFailed') {
    rec.done = true;
    if (p.errorText) rec.error = p.errorText;
  }
}

let listeners = [];
function waitEvent(sessionId, method, ms) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error('timeout waiting for ' + method));
    }, ms || 8000);
    function on(msg) {
      if (msg.method !== method) return;
      if (sessionId && msg.sessionId !== sessionId) return;
      cleanup();
      resolve(msg.params || {});
    }
    function cleanup() {
      clearTimeout(timer);
      const i = listeners.indexOf(on);
      if (i >= 0) listeners.splice(i, 1);
    }
    listeners.push(on);
  });
}

function connect(url) {
  const ws = new WebSocket(url);
  let seq = 0;
  const pending = new Map();
  const opened = new Promise((resolve, reject) => {
    ws.addEventListener('open', resolve);
    ws.addEventListener('error', () => reject(new Error('devtools socket failed')));
  });
  ws.addEventListener('message', (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.method) {
      for (const fn of listeners.slice()) fn(msg);
    }
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      if (msg.error) reject(new Error(msg.error.message || JSON.stringify(msg.error)));
      else resolve(msg.result || {});
    }
  });
  function send(method, params, sessionId) {
    const id = ++seq;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new Error('devtools timeout ' + method));
      }, 20000);
      pending.set(id, {
        resolve(v) { clearTimeout(timer); resolve(v); },
        reject(e) { clearTimeout(timer); reject(e); }
      });
      const payload = { id, method, params: params || {} };
      if (sessionId) payload.sessionId = sessionId;
      ws.send(JSON.stringify(payload));
    });
  }
  return { opened, send };
}

const csp = [];
function noteCSP(text) {
  if (text && /Content Security Policy|Refused to apply inline style|Refused to execute inline script/.test(text)) {
    csp.push(text);
  }
}

async function launchChrome() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'hl-e2e-'));
  const proc = spawn(CHROME, [
    '--headless=new',
    '--disable-gpu',
    '--no-sandbox',
    '--disable-dev-shm-usage',
    '--remote-debugging-port=0',
    '--remote-allow-origins=*',
    '--user-data-dir=' + dir,
    '--no-first-run',
    '--no-default-browser-check',
    'about:blank'
  ], { stdio: ['ignore', 'pipe', 'pipe'] });
  let buf = '';
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('chrome did not open devtools\n' + buf)), 20000);
    const onExit = (code) => {
      clearTimeout(timer);
      reject(new Error('chrome exited ' + code + '\n' + buf));
    };
    proc.on('exit', onExit);
    const take = (c) => {
      buf += c.toString();
      const m = buf.match(/DevTools listening on (ws:\/\/\S+)/);
      if (m) {
        clearTimeout(timer);
        proc.off('exit', onExit);
        resolve(m[1]);
      }
    };
    proc.stdout.on('data', take);
    proc.stderr.on('data', take);
  });
  proc.stdout.on('data', () => {});
  proc.stderr.on('data', () => {});
  return { proc, url, dir };
}

async function newSession(send, opts) {
  const created = await send('Target.createTarget', { url: 'about:blank' });
  const attached = await send('Target.attachToTarget', { targetId: created.targetId, flatten: true });
  const session = attached.sessionId;
  await send('Page.enable', {}, session);
  await send('Runtime.enable', {}, session);
  await send('DOM.enable', {}, session);
  await send('Log.enable', {}, session);
  await send('Network.enable', {}, session);
  if (opts && opts.disableJS) {
    noJS.add(session);
    await send('Emulation.setScriptExecutionDisabled', { value: true }, session);
  }
  await metrics(send, session, 1280, 900, false);
  return session;
}

function metrics(send, session, width, height, mobile) {
  return send('Emulation.setDeviceMetricsOverride', {
    width, height, deviceScaleFactor: 1, mobile
  }, session);
}

async function ev(send, session, expression) {
  const r = await send('Runtime.evaluate', {
    expression,
    returnByValue: true,
    awaitPromise: true
  }, session);
  if (r.exceptionDetails) {
    const d = r.exceptionDetails;
    throw new Error(d.text + (d.exception && d.exception.description ? ': ' + d.exception.description : ''));
  }
  return r.result ? r.result.value : undefined;
}

async function waitFn(send, session, expression, ms) {
  const start = Date.now();
  let last = '';
  let err = '';
  while (Date.now() - start < (ms || 8000)) {
    try {
      const v = await ev(send, session, expression);
      if (v) return v;
      last = JSON.stringify(v);
    } catch (e) {
      err = String(e.message || e);
    }
    await sleep(40);
  }
  let debug = '';
  try {
    debug = await diag(send, session);
  } catch (e) {
    debug = String(e.message || e);
  }
  throw new Error('timeout ' + expression + ' last=' + last + ' err=' + err + ' ' + debug);
}

async function diag(send, session) {
  let page = '';
  try {
    page = await ev(send, session, `(() => {
      const a = document.activeElement;
      const gens = [...document.querySelectorAll('input[name=generation]')].map((el) => el.value);
      const seqs = [...document.querySelectorAll('input[name=sequence]')].map((el) => el.value);
      return JSON.stringify({
        path: location.pathname,
        term: (document.getElementById('msg-term-1') || {}).value || '',
        preset: (document.getElementById('time-select') || {}).value || '',
        from: (document.getElementById('f-from') || {}).value || '',
        status: (document.getElementById('status') || {}).textContent || '',
        live: (document.getElementById('live-status') || {}).textContent || '',
        liveRows: document.querySelectorAll('#live-rows li').length,
        liveTrigger: !!document.getElementById('live-trigger'),
        generation: gens,
        sequence: seqs,
        activeHTML: a ? a.outerHTML : ''
      });
    })()`);
  } catch (e) {
    page = String(e.message || e);
  }
  const rec = lastNet.get(session) || {};
  let body = '';
  if (rec.id && rec.done) {
    try {
      const r = await send('Network.getResponseBody', { requestId: rec.id }, session);
      body = r.base64Encoded ? '[base64]' : (r.body || '');
    } catch (e) {
      body = 'unavailable';
    }
  }
  const req = {
    method: rec.method || '',
    url: rec.url || '',
    status: rec.status || '',
    error: rec.error || '',
    post: oneLine(rec.post || '', 180),
    notice: headerValue(rec.headers, 'x-notice'),
    xGeneration: headerValue(rec.headers, 'x-generation'),
    xSequence: headerValue(rec.headers, 'x-sequence'),
    body: oneLine(body, 180)
  };
  return 'debug=' + oneLine(page, 700) + ' lastReq=' + oneLine(JSON.stringify(req), 900);
}

async function waitIdle(send, session) {
  if (noJS.has(session)) return;
  await waitFn(send, session, `document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling') ? '' : 'idle'`, 20000);
}

async function snap(send, session) {
  try {
    return await ev(send, session, `JSON.stringify({url:location.pathname,status:(document.getElementById('status')||{}).textContent,focus:(document.activeElement||{}).id})`);
  } catch (e) {
    return String(e.message || e);
  }
}

async function clickId(send, session, id) {
  await waitIdle(send, session);
  const ok = await ev(send, session, `(() => { const el = document.getElementById(${JSON.stringify(id)}); if (!el) return false; el.click(); return true; })()`);
  if (!ok) throw new Error('missing #' + id);
}

async function shot(send, session, name) {
  await shotOf(send, session, name, '#status');
}

async function shotOf(send, session, name, sel) {
  if (!SHOT_DIR) return;
  await ev(send, session, `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (el && el.scrollIntoView) el.scrollIntoView({block:'center'}); })()`);
  const r = await send('Page.captureScreenshot', { format: 'png' }, session);
  fs.writeFileSync(path.join(SHOT_DIR, name), Buffer.from(r.data, 'base64'));
}

async function boxWidth(send, session, width) {
  await metrics(send, session, width, 800, width < 500);
  const raw = await ev(send, session, `(() => {
    const d = document.documentElement;
    const b = document.body;
    return JSON.stringify({width:${width}, client:d.clientWidth, scroll:Math.max(d.scrollWidth, b.scrollWidth)});
  })()`);
  const box = JSON.parse(raw);
  if (box.scroll > box.client + 1) throw new Error('sideways scroll at ' + width + ' ' + raw);
  return box;
}

async function login(send, session) {
  await send('Page.navigate', { url: BASE + '/login' }, session);
  await waitFn(send, session, `document.querySelector('input[name=token]') ? 'yes' : ''`, 10000);
  await ev(send, session, `document.querySelector('input[name=token]').value = ${JSON.stringify(TOKEN)}`);
  await waitIdle(send, session);
  await ev(send, session, `document.querySelector('button[type=submit]').click()`);
  await waitFn(send, session, `location.pathname === '/investigate' && document.getElementById('btn-search-implicit') ? 'yes' : ''`, 10000);
}

async function jsOffHTML(send, session) {
  const doc = await send('DOM.getDocument', { depth: 0 }, session);
  const html = await send('DOM.getOuterHTML', { nodeId: doc.root.nodeId }, session);
  return html.outerHTML;
}

async function waitHTML(send, session, needle, ms) {
  const start = Date.now();
  let html = '';
  while (Date.now() - start < (ms || 10000)) {
    try { html = await jsOffHTML(send, session); } catch (e) { html = ''; }
    if (html.includes(needle)) return html;
    await sleep(50);
  }
  throw new Error('html missing ' + needle);
}

async function domQuery(send, session, selector) {
  const doc = await send('DOM.getDocument', { depth: 0 }, session);
  const q = await send('DOM.querySelector', { nodeId: doc.root.nodeId, selector }, session);
  if (!q.nodeId) throw new Error('missing ' + selector);
  return q.nodeId;
}

async function domClick(send, session, selector) {
  await waitIdle(send, session);
  const nodeId = await domQuery(send, session, selector);
  try { await send('DOM.scrollIntoViewIfNeeded', { nodeId }, session); } catch (e) {}
  const box = await send('DOM.getBoxModel', { nodeId }, session);
  const q = box.model.content;
  const x = (q[0] + q[2]) / 2;
  const y = (q[1] + q[5]) / 2;
  await send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 }, session);
  await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 }, session);
}

async function pressEnter(send, session, selector) {
  await waitIdle(send, session);
  const nodeId = await domQuery(send, session, selector);
  await send('DOM.focus', { nodeId }, session);
  await send('Input.dispatchKeyEvent', {
    type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13, text: '\r'
  }, session);
  await send('Input.dispatchKeyEvent', {
    type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13
  }, session);
}

async function runLatency(send, session, say) {
  await clickId(send, session, 'btn-live-start');
  await waitFn(send, session, `document.getElementById('live-status') && document.getElementById('live-status').textContent === 'Live: Receiving' ? 'yes' : ''`);
  const samples = [];
  for (let i = 0; i < 20; i++) {
    await waitFn(send, session, `(() => {
      const el = document.getElementById('live-trigger');
      return el && el.getAttribute('data-every') === '2000' ? 'yes' : '';
    })()`, 15000);
    await sleep(Math.floor(Math.random() * 1500));
    const label = 'synthetic latency ' + String(i).padStart(2, '0');
    const t0 = Date.now();
    say('NEED_LATENCY ' + label);
    await nextLine();
    await waitFn(send, session, `document.getElementById('live-rows') && document.getElementById('live-rows').textContent.includes(${JSON.stringify(label)}) ? 'yes' : ''`, 8000);
    samples.push(Date.now() - t0);
  }
  samples.sort((a, b) => a - b);
  const p95 = samples[Math.ceil(samples.length * 0.95) - 1];
  say('LATENCY_P95_MS ' + p95);
  say('LATENCY_SAMPLES ' + samples.join(','));
  if (p95 >= 2000) throw new Error('first row p95 ' + p95 + ' samples ' + samples.join(','));
}

async function main() {
  if (!BASE || !TOKEN || !CHROME || !AGING_FROM || !AGING_TO) {
    throw new Error('missing BASE, READ_TOKEN, CHROME, or aging window');
  }
  const chrome = await launchChrome();
  let code = 0;
  try {
    const cdp = connect(chrome.url);
    await cdp.opened;
    const send = cdp.send;
    listeners.push((msg) => {
      noteNet(msg);
      if (msg.method === 'Runtime.consoleAPICalled') {
        noteCSP((msg.params.args || []).map((a) => a.value || a.description || '').join(' '));
      } else if (msg.method === 'Runtime.exceptionThrown') {
        noteCSP(JSON.stringify(msg.params.exceptionDetails || {}));
      } else if (msg.method === 'Log.entryAdded') {
        noteCSP((msg.params.entry && msg.params.entry.text) || '');
      }
    });

    const session = await newSession(send, {});
    say('STEP login');
    await login(send, session);
    if (process.env.E2E_LATENCY === '1') {
      await runLatency(send, session, say);
    } else {
    say('STEP narrow');
    await metrics(send, session, 390, 844, true);
    const narrow = await ev(send, session, `document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1 ? 'ok' : 'wide'`);
    if (narrow !== 'ok') throw new Error('390px page scrolls sideways');
    await metrics(send, session, 1280, 900, false);

    say('STEP remove-term');
    await clickId(send, session, 'btn-add-term');
    await waitFn(send, session, `document.getElementById('msg-term-2') ? 'yes' : ''`);
    await ev(send, session, `document.getElementById('msg-term-1').value = 'alpha'; document.getElementById('msg-term-2').value = 'beta';`);
    await clickId(send, session, 'btn-remove-term-1');
    await waitFn(send, session, `(() => {
      const el = document.getElementById('msg-term-1');
      const n = document.querySelectorAll('#filters input[name=term]').length;
      const st = document.getElementById('status');
      return el && el.value === 'beta' && n === 1 && st && !st.textContent.includes('Query is invalid') ? 'yes' : '';
    })()`);

    say('STEP enter-search');
    await ev(send, session, `document.getElementById('msg-term-1').value = 'checkout'; window.__stay = 'enter';`);
    await pressEnter(send, session, '#msg-term-1');
    await waitFn(send, session, `(() => {
      const region = document.getElementById('results-region');
      const term = document.getElementById('msg-term-1');
      if (!region || !term || window.__stay !== 'enter') return '';
      if (term.value !== 'checkout') return '';
      return region.textContent.includes('synthetic checkout accepted') ? 'yes' : '';
    })()`);

    say('STEP save-open');
    await waitIdle(send, session);
    await ev(send, session, `document.getElementById('view-name').value = 'morning'`);
    await clickId(send, session, 'btn-save-view');
    await waitFn(send, session, `document.getElementById('status') && document.getElementById('status').textContent.includes('View saved') ? 'yes' : ''`);
    await waitIdle(send, session);
    const opened = await ev(send, session, `(() => { const b = [...document.querySelectorAll('button')].find((el) => el.textContent === 'Open morning'); if (!b) return false; b.click(); return true; })()`);
    if (!opened) throw new Error('Open morning missing');
    await waitFn(send, session, `(() => {
      const st = document.getElementById('status');
      const region = document.getElementById('results-region');
      if (!st || !region) return '';
      if (st.textContent.includes('Query is invalid')) return '';
      return region.textContent.includes('synthetic checkout accepted') ? 'yes' : '';
    })()`);

    say('STEP back-focus');
    const openId = await ev(send, session, `(() => { const b = document.querySelector('button[id$="-open"]'); return b ? b.id : ''; })()`);
    if (!openId) throw new Error('no open button');
    await clickId(send, session, openId);
    await waitFn(send, session, `document.getElementById('detail-heading') ? 'yes' : ''`);
    await clickId(send, session, 'btn-back');
    await waitFn(send, session, `document.activeElement && document.activeElement.id === ${JSON.stringify(openId)} ? 'yes' : ''`);

    say('STEP filter');
    await clickId(send, session, openId);
    await waitFn(send, session, `document.getElementById('detail-heading') ? 'yes' : ''`);
    await waitIdle(send, session);
    const filtered = await ev(send, session, `(() => {
      const forms = [...document.querySelectorAll('form')];
      const f = forms.find((form) => [...form.querySelectorAll('input[name=path]')].some((el) => el.value === 'task.attempt'));
      if (!f) return 'missing';
      const btn = f.querySelector('button[type=submit]');
      if (!btn) return 'no-button';
      btn.click();
      return 'ok';
    })()`);
    if (filtered !== 'ok') throw new Error('filter control ' + filtered);
    await waitFn(send, session, `(() => {
      const el = document.querySelector('#results-region input[name=path]');
      const region = document.getElementById('results-region');
      return el && el.value === 'task.attempt' && region.textContent.includes('synthetic checkout accepted') ? 'yes' : '';
    })()`);

    say('STEP hostile');
    await waitIdle(send, session);
    await ev(send, session, `(() => {
      document.getElementById('time-select').value = '15m';
      document.getElementById('severity-select').value = '1';
      const src = document.getElementById('source-select');
      for (const opt of src.options) opt.selected = opt.value === 'src_demo_api';
      document.getElementById('pred-1').value = '';
      document.querySelector('#filters input[name=pval]').value = '';
      document.getElementById('msg-term-1').value = 'hostile';
      document.body.addEventListener('htmx:afterSwap', function onSwap(ev) {
        const info = ev.detail && ev.detail.pathInfo;
        const path = (info && info.requestPath) || '';
        if (path.indexOf('/search') === -1) return;
        document.body.removeEventListener('htmx:afterSwap', onSwap);
        document.getElementById('msg-term-1').value = 'commonwide';
        document.getElementById('pred-1').value = 'task.attempt';
        document.querySelector('#filters select[name=op]').value = 'eq';
        document.querySelector('#filters select[name=ptype]').value = 'integer';
        document.querySelector('#filters input[name=pval]').value = '999';
        document.getElementById('btn-search').click();
      });
      document.getElementById('btn-search').click();
    })()`);
    await waitFn(send, session, `(() => {
      const region = document.getElementById('results-region');
      return region && region.textContent.includes('<script>alert(1)</script>') ? 'yes' : '';
    })()`);
    const hostile = await ev(send, session, `document.querySelector('#results-region script, #results-region [hx-get], script:not([src])') ? 'bad' : 'ok'`);
    if (hostile !== 'ok') throw new Error('hostile markup became a node');

    say('STEP too-broad');
    await waitFn(send, session, `(() => {
      const el = document.getElementById('status');
      if (!el || !el.isConnected) return '';
      const region = document.getElementById('results-region');
      if (!region || !region.textContent.includes('synthetic hostile')) return '';
      if (document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')) return '';
      if (!document.activeElement || document.activeElement.id !== 'status') return '';
      return el.textContent === 'Query too broad. Narrow the time window, then run Search.' ? 'yes' : '';
    })()`, 20000);
    const broadFocus = await ev(send, session, `document.activeElement && document.activeElement.id`);
    if (broadFocus !== 'status') throw new Error('too-broad focus ' + broadFocus);
    await shot(send, session, 'notice-after-search-desktop.png');
    await metrics(send, session, 390, 844, true);
    await shot(send, session, 'notice-after-search-390.png');
    await metrics(send, session, 1280, 900, false);

    say('STEP gap0');
    await waitIdle(send, session);
    const gapArmed = await ev(send, session, `(() => {
      document.body.addEventListener('htmx:afterSwap', function onSwap(ev) {
        const info = ev.detail && ev.detail.pathInfo;
        const path = (info && info.requestPath) || '';
        if (path.indexOf('/search') === -1) return;
        document.body.removeEventListener('htmx:afterSwap', onSwap);
        document.getElementById('time-select').value = 'custom';
        document.getElementById('f-from').value = 'not-a-time';
        document.getElementById('f-to').value = 'not-a-time';
        document.getElementById('btn-search').click();
      });
      document.getElementById('time-select').value = '15m';
      document.getElementById('pred-1').value = '';
      document.querySelector('#filters input[name=pval]').value = '';
      document.getElementById('msg-term-1').value = 'checkout';
      document.getElementById('btn-search').click();
      return 'armed';
    })()`);
    if (gapArmed !== 'armed') throw new Error('gap0 arm ' + gapArmed);
    await waitFn(send, session, `(() => {
      const el = document.getElementById('status');
      if (!el || el.textContent !== 'Query is invalid') return '';
      if (document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')) return '';
      return document.activeElement && document.activeElement.id === 'status' ? 'yes' : '';
    })()`);
    const gapFocus = await ev(send, session, `document.activeElement && document.activeElement.id`);
    if (gapFocus !== 'status') throw new Error('gap0 focus ' + gapFocus);

    say('STEP row-expired');
    await waitIdle(send, session);
    await ev(send, session, `(() => {
      document.getElementById('time-select').value = 'custom';
      document.getElementById('f-from').value = ${JSON.stringify(AGING_FROM)};
      document.getElementById('f-to').value = ${JSON.stringify(AGING_TO)};
      document.getElementById('pred-1').value = '';
      document.querySelector('#filters input[name=pval]').value = '';
      document.getElementById('msg-term-1').value = 'aging';
    })()`);
    await clickId(send, session, 'btn-search');
    await waitFn(send, session, `document.getElementById('results-region') && document.getElementById('results-region').textContent.includes('synthetic aging row') ? 'yes' : ''`);
    await waitIdle(send, session);
    const agingId = await ev(send, session, `(() => { const b = document.querySelector('button[id$="-open"]'); return b ? b.id : ''; })()`);
    say('NEED_CLOCK row');
    await nextLine();
    await clickId(send, session, agingId);
    await waitFn(send, session, `(() => {
      const el = document.getElementById('status');
      if (!el || !el.isConnected) return '';
      if (el.textContent !== 'Row expired. Rerun query. This row is past the retention window.') return '';
      if (document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')) return '';
      return document.activeElement && document.activeElement.id === 'status' ? 'yes' : '';
    })()`);
    const rowFocus = await ev(send, session, `document.activeElement && document.activeElement.id`);
    if (rowFocus !== 'status') throw new Error('row-expired focus ' + rowFocus);

    say('STEP cursor-expired');
    await waitIdle(send, session);
    await ev(send, session, `(() => {
      document.getElementById('time-select').value = '15m';
      document.getElementById('msg-term-1').value = 'filler';
    })()`);
    await clickId(send, session, 'btn-search');
    await waitFn(send, session, `document.getElementById('btn-older') ? 'yes' : ''`);
    say('NEED_CLOCK cursor');
    await nextLine();
    await clickId(send, session, 'btn-older');
    await waitFn(send, session, `(() => {
      const el = document.getElementById('status');
      if (!el || !el.isConnected) return '';
      return el.textContent === 'Results expired. Rerun query.' ? 'yes' : '';
    })()`);

    say('NEED_CLOCK reset');
    await nextLine();

    say('STEP live');
    await waitIdle(send, session);
    await ev(send, session, `(() => {
      document.getElementById('time-select').value = '15m';
      document.getElementById('severity-select').value = '1';
      document.getElementById('msg-term-1').value = '';
      const pred = document.getElementById('pred-1');
      if (pred) pred.value = '';
      const pval = document.querySelector('#filters input[name=pval]');
      if (pval) pval.value = '';
      const src = document.getElementById('source-select');
      for (const opt of src.options) opt.selected = opt.value === 'src_demo_api';
    })()`);
    await clickId(send, session, 'btn-live-start');
    await waitFn(send, session, `(() => {
      const st = document.getElementById('live-status');
      if (!st || st.textContent !== 'Live: Receiving') return '';
      if (!document.getElementById('live-trigger')) return '';
      if (document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')) return '';
      return 'yes';
    })()`, 10000);
    say('NEED_LIVE');
    await nextLine();
    await waitFn(send, session, `document.querySelectorAll('#live-rows li').length >= 12 && document.getElementById('live-rows').textContent.includes('synthetic live arrived') ? 'yes' : ''`, 12000);
    const dupes = await ev(send, session, `(() => {
      const ids = [...document.querySelectorAll('#live-rows li')].map((el) => el.getAttribute('data-ingest-id'));
      return new Set(ids).size === ids.length ? 'ok' : 'dup';
    })()`);
    if (dupes !== 'ok') throw new Error('live rows duplicated');
    const scrollState = await ev(send, session, `(() => {
      const rows = document.getElementById('live-rows');
      if (!rows || rows.scrollHeight <= rows.clientHeight + 1) return 'short';
      rows.scrollTop = rows.scrollHeight;
      rows.dispatchEvent(new Event('scroll', { bubbles: false }));
      return document.getElementById('live-trigger') ? 'still' : 'paused';
    })()`);
    say('live-scroll ' + scrollState);
    if (scrollState === 'short') throw new Error('live rows did not overflow');
    if (scrollState === 'still') throw new Error('scroll did not pause polling');
    if (scrollState === 'paused') {
      await ev(send, session, `(() => {
        const rows = document.getElementById('live-rows');
        rows.scrollTop = 0;
        rows.dispatchEvent(new Event('scroll', { bubbles: false }));
      })()`);
      await waitFn(send, session, `document.getElementById('live-trigger') && document.getElementById('live-status').textContent === 'Live: Receiving' ? 'yes' : ''`);
    }
    if (SHOT_DIR) {
      await shotOf(send, session, 'live-receiving-desktop.png', '#live-panel');
      await metrics(send, session, 390, 844, true);
      await shotOf(send, session, 'live-receiving-390.png', '#live-panel');
      await metrics(send, session, 1280, 900, false);
    }
    await clickId(send, session, 'btn-live-pause');
    await waitFn(send, session, `(() => {
      const st = document.getElementById('live-status');
      if (!st || st.textContent !== 'Live: Paused') return '';
      if (document.getElementById('live-trigger')) return '';
      if (document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')) return '';
      return 'yes';
    })()`);
    if (SHOT_DIR) {
      await shotOf(send, session, 'live-paused-desktop.png', '#live-panel');
      await metrics(send, session, 390, 844, true);
      await shotOf(send, session, 'live-paused-390.png', '#live-panel');
      await metrics(send, session, 1280, 900, false);
    }
    say('NEED_LIVE_HOLD');
    await nextLine();
    await sleep(1200);
    const held = await ev(send, session, `document.getElementById('live-rows').textContent.includes('synthetic live while paused') ? 'shown' : 'hidden'`);
    if (held !== 'hidden') throw new Error('paused live showed a new row');
    await clickId(send, session, 'btn-live-resume');
    await waitFn(send, session, `document.getElementById('live-rows').textContent.includes('synthetic live while paused') ? 'yes' : ''`, 12000);
    await clickId(send, session, 'btn-live-pause');
    await waitFn(send, session, `(() => {
      const st = document.getElementById('live-status');
      if (!st || st.textContent !== 'Live: Paused') return '';
      if (document.getElementById('live-trigger')) return '';
      if (document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')) return '';
      return 'yes';
    })()`);
    if (SHOT_DIR) {
      say('NEED_GAP');
      await nextLine();
      await clickId(send, session, 'btn-live-resume');
      await waitFn(send, session, `document.getElementById('live-status') && document.getElementById('live-status').textContent === 'Live: Gap detected' ? 'yes' : ''`, 15000);
      await shotOf(send, session, 'live-gap-desktop.png', '#live-panel');
      await metrics(send, session, 390, 844, true);
      await shotOf(send, session, 'live-gap-390.png', '#live-panel');
      await metrics(send, session, 1280, 900, false);
      await ev(send, session, `document.getElementById('live-cursor-received').value = 'nope'`);
      await clickId(send, session, 'btn-live-resume');
      await waitFn(send, session, `document.getElementById('live-status') && document.getElementById('live-status').textContent === 'Live: Query invalid' ? 'yes' : ''`, 10000);
      await shotOf(send, session, 'live-error-desktop.png', '#live-panel');
      await metrics(send, session, 390, 844, true);
      await shotOf(send, session, 'live-error-390.png', '#live-panel');
      await metrics(send, session, 1280, 900, false);
    }
    const widths = [];
    widths.push(await boxWidth(send, session, 1280));
    widths.push(await boxWidth(send, session, 390));
    widths.push(await boxWidth(send, session, 360));
    await metrics(send, session, 1280, 900, false);
    if (SHOT_DIR) fs.writeFileSync(path.join(SHOT_DIR, 'scroll.json'), JSON.stringify(widths, null, 2));

    say('STEP no-js');
    const off = await newSession(send, { disableJS: true });
    await send('Page.navigate', { url: BASE + '/investigate' }, off);
    await waitHTML(send, off, 'id="btn-search-implicit"', 10000);
    const termNode = await domQuery(send, off, '#msg-term-1');
    await send('DOM.focus', { nodeId: termNode }, off);
    await send('Input.insertText', { text: 'checkout' }, off);
    await pressEnter(send, off, '#msg-term-1');
    const entered = await waitHTML(send, off, 'synthetic checkout accepted', 10000);
    if (!entered.includes('value="checkout"')) throw new Error('no-js enter dropped the term');
    if (entered.includes('Query is invalid')) throw new Error('no-js enter was not a search');
    const openMatch = entered.match(/id="(event-\d+-open)"/);
    if (!openMatch) throw new Error('no-js results have no open button');
    await domClick(send, off, '#' + openMatch[1]);
    await waitHTML(send, off, 'Event details', 10000);
    await domClick(send, off, '#btn-back');
    const back = await waitHTML(send, off, 'id="results-heading"', 10000);
    if (!back.includes('id="' + openMatch[1] + '" autofocus')) {
      throw new Error('no-js back did not autofocus ' + openMatch[1]);
    }
    let focused = '';
    const focusStart = Date.now();
    while (Date.now() - focusStart < 3000 && focused !== openMatch[1]) {
      try {
        focused = await ev(send, off, `document.activeElement && document.activeElement.id`) || '';
      } catch (e) {
        const tree = await send('Accessibility.getFullAXTree', {}, off);
        const node = (tree.nodes || []).find((n) => n.focused);
        if (node && node.backendDOMNodeId) {
          const desc = await send('DOM.describeNode', { backendNodeId: node.backendDOMNodeId }, off);
          const attrs = (desc.node && desc.node.attributes) || [];
          for (let i = 0; i < attrs.length; i += 2) {
            if (attrs[i] === 'id') focused = attrs[i + 1];
          }
        }
        break;
      }
      if (focused !== openMatch[1]) await sleep(50);
    }
    if (focused !== openMatch[1]) throw new Error('no-js focus ' + focused + ' wanted ' + openMatch[1]);

    say('STEP session-expired');
    await waitIdle(send, session);
    const second = await fetch(BASE + '/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', Origin: BASE },
      body: 'token=' + TOKEN,
      redirect: 'manual'
    });
    if (second.status !== 303) throw new Error('second login status ' + second.status);
    await clickId(send, session, 'btn-search');
    await waitFn(send, session, `(() => {
      const el = document.getElementById('status');
      if (!el || !el.isConnected) return '';
      const link = el.querySelector('a');
      return el.textContent.indexOf('Session expired.') === 0 && link && link.getAttribute('href') === '/login' && link.textContent === 'Sign in' ? 'yes' : '';
    })()`);

    }
    if (csp.length) throw new Error('CSP console errors: ' + csp.join(' | '));
    say('PASS');
  } catch (e) {
    say('FAIL ' + (e && e.stack ? e.stack : e));
    code = 1;
  } finally {
    try { chrome.proc.kill('SIGKILL'); } catch (e) {}
    try { fs.rmSync(chrome.dir, { recursive: true, force: true }); } catch (e) {}
  }
  process.exit(code);
}

main().catch((e) => {
  say('FAIL ' + (e && e.stack ? e.stack : e));
  process.exit(1);
});
