// Local digests (src/canonical.ts, DESIGN.md §10.3) equal the server's, byte for byte.
// Offline: the vectors in sdk/testdata/digest_vectors.json (shared with the Python
// SDK). With WEFT_TEST_SERVER_URL: every vector saved as an object, a table row and
// a file on a live server, and a traced function's ref.
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { canonicalJson, digest, fileDigest, tableDigest } from '../src/canonical.js';

const here = dirname(fileURLToPath(import.meta.url));
const VECTORS = JSON.parse(readFileSync(join(here, '..', '..', 'testdata', 'digest_vectors.json'), 'utf8'));
const URL = process.env.WEFT_TEST_SERVER_URL;

describe('digest vectors', () => {
  for (const v of VECTORS.values) {
    it(v.name, () => {
      expect(canonicalJson(v.value)).toBe(v.canonical);
      expect(digest(v.value)).toBe(v.digest);
    });
  }
  it('tables and files', () => {
    expect(tableDigest(VECTORS.table.row_digests)).toBe(VECTORS.table.digest);
    for (const f of VECTORS.files) expect(fileDigest(Buffer.from(f.text, 'utf8'))).toBe(f.digest);
  });
});

describe.skipIf(!URL)('digests against a live server', () => {
  it('objects, tables, files and functions', async () => {
    const client = await weft.init(`digest-check/ts-${randomUUID().slice(0, 8)}`, { serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, settings: { printCallLink: false } });
    try {
      // plus numbers whose text is JavaScript's own (Python sends 1.0 for 1.0)
      const values = [...VECTORS.values.map((v: any) => v.value), { f: 1.0, g: 1e-7, h: 1e21, k: 2 ** 60, m: -0 }];
      for (const [i, v] of values.entries()) {
        const res = await client.http.post('/api/v1/objects/save', { project_id: client.projectId, name: `v${i}`, value: v });
        expect(res.digest).toBe(digest(v));
      }
      const t = await client.http.post('/api/v1/tables/create', { project_id: client.projectId, rows: values });
      expect(t.row_digests).toEqual(values.map((v: unknown) => digest(v)));
      expect(t.digest).toBe(tableDigest(t.row_digests));
      for (const f of VECTORS.files) {
        const up = await client.http.postFile('/api/v1/files/upload', { project_id: client.projectId }, Buffer.from(f.text, 'utf8'), 'f.txt', 'text/plain');
        expect(up.digest).toBe(fileDigest(Buffer.from(f.text, 'utf8')));
      }
      const traced = weft.trace(function traced(x: number) {
        return x * 1.5;
      });
      const ref = await client.save(traced);
      await client.flush();
      const obj = await client.readObject(client.projectId, ref.name);
      expect(obj).toMatchObject({ digest: ref.digest, kind: 'function', ref: ref.uri() });
    } finally {
      await weft.finish();
    }
  });
});
