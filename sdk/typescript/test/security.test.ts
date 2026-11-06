import { chmodSync, mkdirSync, mkdtempSync, statSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, expect, it, vi } from 'vitest';
import * as weft from '../src/index.js';
import { HttpClient, WeftHTTPError, warnPlainHttpKey } from '../src/http.js';
import { REDACTED, Redactor, isSecretKey, redactSecretValues } from '../src/redact.js';
import { FakeWeft } from './fakeServer.js';
import { WriteAheadLog } from '../src/sender.js';

interface Seen {
  method: string;
  path: string;
  auth?: string;
  body: string;
}

async function recorder(status?: number, location?: string): Promise<{ url: string; seen: Seen[]; close: () => Promise<void> }> {
  const seen: Seen[] = [];
  const srv: Server = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      seen.push({ method: req.method ?? '', path: req.url ?? '', auth: req.headers.authorization, body });
      if (status) {
        res.writeHead(status, { Location: location + (req.url ?? ''), 'Content-Length': '0' });
        res.end();
        return;
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end('{}');
    });
  });
  await new Promise<void>((r) => srv.listen(0, '127.0.0.1', r));
  const url = `http://127.0.0.1:${(srv.address() as AddressInfo).port}`;
  return { url, seen, close: () => new Promise<void>((r) => srv.close(() => r())) };
}

const closers: Array<() => Promise<void>> = [];
afterEach(async () => {
  while (closers.length) await closers.pop()!();
  delete process.env.WEFT_REDACT_KEYS;
});

describe('redirects (F4)', () => {
  for (const status of [301, 302, 303, 307, 308]) {
    it(`refuses a ${status} and the target sees nothing`, async () => {
      const target = await recorder();
      const redir = await recorder(status, target.url);
      closers.push(target.close, redir.close);
      const http = new HttpClient(redir.url, 'SECRET-KEY');
      const err = await http.post('/api/v1/spans/batch', { batch: [{ secret: 'body' }] }).catch((e) => e);
      expect(err).toBeInstanceOf(WeftHTTPError);
      expect(err.status).toBe(status);
      expect(err.reason).toMatch(/redirect .* refused/);
      expect(err.retryable).toBe(false);
      await expect(http.get('/api/v1/server')).rejects.toThrow(/refused/);
      expect(redir.seen).toHaveLength(2);
      expect(target.seen).toEqual([]);
    });
  }

  it('through the client: spans never reach the redirect target', async () => {
    const target = await recorder();
    const redir = await recorder(307, target.url);
    closers.push(target.close, redir.close);
    const c = await weft.init('e/p', { serverUrl: redir.url, apiKey: 'SECRET-KEY', sender: { flushIntervalMs: 1 } });
    const f = weft.trace(function f(x: string) {
      return x;
    });
    f('payload');
    await c.flush().catch(() => undefined);
    await weft.finish();
    expect(redir.seen.length).toBeGreaterThan(0);
    expect(target.seen).toEqual([]);
  });
});

describe('plain http with an API key', () => {
  it('warns once for a non-loopback host, never for https or loopback', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    try {
      expect(warnPlainHttpKey('http://weft.example.org:6345', 'KEY-VALUE-123')).toBe(true);
      expect(warnPlainHttpKey('http://weft.example.org:6345', 'KEY-VALUE-123')).toBe(true);
      expect(warnPlainHttpKey('https://weft.example.org', 'k')).toBe(false);
      expect(warnPlainHttpKey('http://127.0.0.1:6345', 'k')).toBe(false);
      expect(warnPlainHttpKey('http://localhost:6345', 'k')).toBe(false);
      expect(warnPlainHttpKey('http://[::1]:6345', 'k')).toBe(false);
      expect(warnPlainHttpKey('http://weft.example.org:6345')).toBe(false);
      const msgs = warn.mock.calls.map((c) => String(c[0])).filter((m) => m.includes('plain http'));
      expect(msgs).toHaveLength(1);
      expect(msgs[0]).toContain('weft.example.org');
      expect(msgs[0]).not.toContain('KEY-VALUE-123');
    } finally {
      warn.mockRestore();
    }
  });
});

// The same table as sdk/python/tests/test_security.py.
const SECRET_KEYS = [
  'api_key', 'API_KEY', 'apiKey', 'api-key', 'X-Api-Key', 'x_api_key', 'openai_api_key', 'OPENAI_API_KEY',
  'anthropicApiKey', 'client_secret', 'clientSecret', 'secret', 'aws_secret_access_key', 'refresh_token',
  'access_token', 'id_token', 'bearer_token', 'github-token', 'token', 'private_key', 'privateKey', 'passwd',
  'password', 'db_password', 'pwd', 'credentials', 'credential', 'Cookie', 'Set-Cookie', 'Authorization',
  'Proxy-Authorization', 'auth', 'x-auth', 'secret_key', 'signing_key', 'session_key',
];
const NOT_SECRET_KEYS = [
  'max_tokens', 'prompt_tokens', 'total_tokens', 'tokens', 'tokenizer', 'key', 'sort_key', 'primary_key',
  'public_key_id', 'author', 'name', 'monkey', 'model', 'keyword', 'access_key_id',
];
const SECRET_VALUES: Array<[string, string]> = [
  ['Authorization: Bearer abcdefgh12345678', `Authorization: Bearer ${REDACTED}`],
  ['bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig', `bearer ${REDACTED}`],
  ['Basic dXNlcjpwYXNzd29yZA==', `Basic ${REDACTED}`],
  ['key sk-proj-ABCDEFGHIJKLMNOPQRSTUVWX0123 end', `key ${REDACTED} end`],
  ['sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ', REDACTED],
  ['id AKIAIOSFODNN7EXAMPLE.', `id ${REDACTED}.`],
  ['ghp_' + 'a'.repeat(36), REDACTED],
  ['xoxb-1234567890-abcdefghij', REDACTED],
  ['DATABASE_URL=postgres://user:pw-OOO@db/x', `DATABASE_URL=postgres://user:${REDACTED}@db/x`],
  ['-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----', REDACTED],
];

describe('secret redaction (F6)', () => {
  it.each(SECRET_KEYS)('redacts key %s', (k) => {
    expect(isSecretKey(k)).toBe(true);
    expect(new Redactor().apply({ [k]: 'v', other: 1 })).toEqual({ [k]: REDACTED, other: 1 });
  });

  it.each(NOT_SECRET_KEYS)('keeps key %s', (k) => {
    expect(isSecretKey(k)).toBe(false);
    expect(new Redactor().apply({ [k]: 'v' })).toEqual({ [k]: 'v' });
  });

  it.each(SECRET_VALUES)('redacts value %s', (raw, want) => {
    expect(redactSecretValues(raw)).toBe(want);
    expect(new Redactor().apply({ text: raw })).toEqual({ text: want });
  });

  it.each(['a normal sentence', 'sk-short', 'https://example.com/a:b@c', 'Bearer x', 'token count 12'])('keeps value %s', (raw) => {
    expect(redactSecretValues(raw)).toBe(raw);
  });

  it('WEFT_REDACT_KEYS=0 turns it off; a custom list is exact (normalised)', () => {
    process.env.WEFT_REDACT_KEYS = '0';
    const off = new Redactor();
    expect(off.active).toBe(false);
    expect(off.apply({ client_secret: 'x', t: 'Bearer abcdefgh12345678' })).toEqual({ client_secret: 'x', t: 'Bearer abcdefgh12345678' });
    delete process.env.WEFT_REDACT_KEYS;
    const custom = new Redactor({ redactKeys: ['SSN'] });
    expect(custom.apply({ ssn: 1, 's-s-n': 2, client_secret: 3 })).toEqual({ ssn: REDACTED, 's-s-n': REDACTED, client_secret: 3 });
  });

  it("the reviewer's repro: no planted secret reaches the server", async () => {
    const fake = await new FakeWeft().start();
    closers.push(() => fake.close());
    const c = await weft.init('e/p', { serverUrl: fake.url, sender: { flushIntervalMs: 1 } });
    const callApi = weft.trace(function call_api(config: any, headers: any, env: string) {
      return { ok: true, echo_headers: headers, env };
    });
    callApi(
      { openai_api_key: 'sk-openai-AAA', client_secret: 'cs-BBB', refresh_token: 'rt-CCC', aws_secret_access_key: 'aws-DDD', private_key: '-----BEGIN KEY-----EEE', passwd: 'FFF', credentials: 'GGG', 'api-key': 'HHH', apiKey: 'III', bearer_token: 'JJJ' },
      { 'X-Api-Key': 'xk-KKK', Cookie: 'session=LLL', 'Proxy-Authorization': 'Basic MMM', Authorization: 'Bearer NNN' },
      'DATABASE_URL=postgres://u:pw-OOO@db/x',
    );
    await c.flush();
    await weft.finish();
    const blob = JSON.stringify(fake.requests.map((r) => r.body));
    expect(blob).toContain('call_api');
    for (const planted of ['AAA', 'BBB', 'CCC', 'DDD', 'EEE', 'FFF', 'GGG', 'HHH', 'III', 'JJJ', 'KKK', 'LLL', 'MMM', 'NNN', 'OOO']) expect(blob).not.toContain(planted);
  });
});

describe('WAL file modes', () => {
  it.skipIf(process.platform === 'win32')('the WAL is 0600 in a 0700 directory, older directories are tightened', () => {
    const base = mkdtempSync(join(tmpdir(), 'weft-wal-'));
    const d = join(base, 'wal');
    mkdirSync(d, { mode: 0o755 });
    chmodSync(d, 0o755);
    new WriteAheadLog(join(d, 'x.jsonl')).append([{ kind: 'post', path: '/p', payload: { value: 'secret' } }]);
    expect(statSync(d).mode & 0o777).toBe(0o700);
    expect(statSync(join(d, 'x.jsonl')).mode & 0o777).toBe(0o600);
  });
});
