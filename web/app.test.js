const assert = require('assert');
const { test } = require('node:test');

const {
  parseQuery,
  buildWsUrl,
  buildShareUrl,
  encodeFrame,
  decodeFrame,
  getBackoffMs,
  applySessionsSync,
  applyCtrl,
  applyShift,
  transformInput,
} = require('./app');

test('parseQuery uses defaults', () => {
  const out = parseQuery('');
  assert.equal(out.mode, 'local');
  assert.equal(out.agent, 'agent1');
  assert.equal(out.session, 'ai');
  assert.equal(out.token, '');
});

test('parseQuery uses provided values', () => {
  const out = parseQuery('?mode=hub&agent=a1&session=s1&token=t1');
  assert.equal(out.mode, 'hub');
  assert.equal(out.agent, 'a1');
  assert.equal(out.session, 's1');
  assert.equal(out.token, 't1');
});

test('buildWsUrl local', () => {
  const url = buildWsUrl({
    mode: 'local',
    host: 'example.com',
    protocol: 'http:',
    token: 'x',
  });
  assert.equal(url, 'ws://example.com/ws?token=x');
});

test('buildWsUrl hub', () => {
  const url = buildWsUrl({
    mode: 'hub',
    host: 'example.com',
    protocol: 'https:',
    token: 'x',
  });
  assert.equal(url, 'wss://example.com/ws/client?token=x');
});

test('buildShareUrl includes mode/agent/session', () => {
  const url = buildShareUrl({
    origin: 'https://example.com',
    pathname: '/',
    search: '?mode=hub&agent=a1&session=s1',
  });
  assert.equal(url, 'https://example.com/?mode=hub&agent=a1&session=s1');
});

test('buildShareUrl includes token when present', () => {
  const url = buildShareUrl({
    origin: 'https://example.com',
    pathname: '/',
    search: '?mode=hub&agent=a1&session=s1&token=t1',
  });
  assert.equal(url, 'https://example.com/?mode=hub&agent=a1&session=s1&token=t1');
});

test('encode/decode frame', () => {
  const payload = new TextEncoder().encode('hi');
  const buf = encodeFrame('ai', payload);
  const decoded = decodeFrame(buf);
  assert.equal(decoded.sessionId, 'ai');
  assert.equal(new TextDecoder().decode(decoded.payload), 'hi');
});

test('getBackoffMs caps', () => {
  assert.equal(getBackoffMs(0, 500, 5000), 500);
  assert.equal(getBackoffMs(3, 500, 5000), 4000);
  assert.equal(getBackoffMs(4, 500, 5000), 5000);
});

test('applyCtrl maps letters', () => {
  assert.equal(applyCtrl('c'), '\x03');
  assert.equal(applyCtrl('C'), '\x03');
  assert.equal(applyCtrl('['), '\x1b');
});

test('applyShift maps letters and symbols', () => {
  assert.equal(applyShift('a'), 'A');
  assert.equal(applyShift('1'), '!');
  assert.equal(applyShift('-'), '_');
});

test('transformInput applies ctrl and resets', () => {
  const res = transformInput('c', true, false);
  assert.equal(res.data, '\x03');
  assert.equal(res.usedCtrl, true);
  assert.equal(res.usedShift, false);
});

test('transformInput applies shift and resets', () => {
  const res = transformInput('a', false, true);
  assert.equal(res.data, 'A');
  assert.equal(res.usedCtrl, false);
  assert.equal(res.usedShift, true);
});

test('applySessionsSync returns added and removed', () => {
  const res = applySessionsSync(['ai', 'dev'], ['ai', 'ops']);
  assert.deepEqual(res.added, ['ops']);
  assert.deepEqual(res.removed, ['dev']);
});
