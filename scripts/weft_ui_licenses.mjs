#!/usr/bin/env node
// Write the third-party licence file for the embedded Weft UI.
//
// The Vite build bundles and minifies the UI's npm dependencies into
// internal/weft/webui/static, and the minifier drops their licence comments.
// MIT, ISC and BSD licences require the copyright notice and licence text to
// travel with every copy, so this script collects them from node_modules into
// internal/weft/webui/static/THIRD_PARTY_LICENSES.txt, which is embedded in
// the weft binary next to the bundle and served at /THIRD_PARTY_LICENSES.txt.
//
// The package list is every non-dev package in web/weft-ui/package-lock.json
// (the runtime dependency closure), minus @types/* (type declarations, never
// bundled). Tree-shaking may leave some of them out of the bundle; listing a
// package that ended up unused is harmless, missing one is not.
//
// Usage (run after `vite build`, which empties the output directory;
// `make weft-ui` does both):
//
//   node scripts/weft_ui_licenses.mjs            # write the file
//   node scripts/weft_ui_licenses.mjs --check    # exit 1 if it is missing or stale
//
// Needs `npm ci` in web/weft-ui first (licence texts come from node_modules).

import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const uiDir = join(repo, 'web', 'weft-ui');
const outFile = join(repo, 'internal', 'weft', 'webui', 'static', 'THIRD_PARTY_LICENSES.txt');
const check = process.argv.includes('--check');

const lock = JSON.parse(readFileSync(join(uiDir, 'package-lock.json'), 'utf8'));
const LICENCE_FILE = /^(licen[cs]e|copying)([.-].*)?$/i;

// Packages that ship no licence file of their own: the text from their
// upstream repository (checked when the package version changes).
const UPSTREAM_TEXT = {
  // https://github.com/FormidableLabs/victory/blob/main/LICENSE.txt
  'victory-vendor': `The MIT License (MIT)

Copyright (c) 2015-2026 Nearform.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`,
};

/** Licence files of libraries a package vendors (e.g. victory-vendor/lib-vendor/d3-array/LICENSE). */
function vendoredLicences(dir) {
  const out = [];
  for (const vendor of ['lib-vendor', 'vendor']) {
    const vdir = join(dir, vendor);
    if (!existsSync(vdir)) continue;
    for (const lib of readdirSync(vdir).sort()) {
      const ldir = join(vdir, lib);
      let files = [];
      try {
        files = readdirSync(ldir).filter((f) => LICENCE_FILE.test(f)).sort();
      } catch {
        continue; // not a directory
      }
      for (const f of files) out.push(`[vendored ${lib}]\n` + readFileSync(join(ldir, f), 'utf8').replace(/\r\n/g, '\n').trimEnd());
    }
  }
  return out;
}

const entries = new Map();
const problems = [];
for (const [path, meta] of Object.entries(lock.packages ?? {})) {
  if (!path || meta.dev || meta.devOptional || meta.link) continue;
  const name = meta.name ?? path.slice(path.lastIndexOf('node_modules/') + 'node_modules/'.length);
  if (name.startsWith('@types/')) continue;
  const dir = join(uiDir, path);
  if (!existsSync(dir)) {
    problems.push(`${name}: not installed (run npm ci in web/weft-ui)`);
    continue;
  }
  const pkg = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
  const version = pkg.version ?? meta.version ?? '';
  const key = `${name}@${version}`;
  if (entries.has(key)) continue;
  const licence = meta.license || (typeof pkg.license === 'string' ? pkg.license : pkg.license?.type) || 'UNKNOWN';
  const files = readdirSync(dir).filter((f) => LICENCE_FILE.test(f)).sort();
  const texts = files.map((f) => readFileSync(join(dir, f), 'utf8').replace(/\r\n/g, '\n').trimEnd());
  if (!texts.length && UPSTREAM_TEXT[name]) texts.push(`[from the upstream repository; the package ships no licence file]\n${UPSTREAM_TEXT[name]}`);
  if (!texts.length) problems.push(`${key}: no licence file in the package (add its upstream text to UPSTREAM_TEXT)`);
  texts.push(...vendoredLicences(dir));
  const homepage = pkg.homepage ?? (typeof pkg.repository === 'string' ? pkg.repository : pkg.repository?.url) ?? '';
  entries.set(key, { name, version, licence, homepage, texts });
}

if (problems.length) {
  console.error('weft_ui_licenses: cannot complete the licence file:\n  ' + problems.join('\n  '));
  process.exit(2);
}

const rule = '-'.repeat(78);
const sorted = [...entries.values()].sort((a, b) => (a.name === b.name ? a.version.localeCompare(b.version) : a.name.localeCompare(b.name)));
let out = `Third-party software in the Weft web UI

The Weft UI (internal/weft/webui/static) bundles the npm packages listed
below. Each is used under its own licence, reproduced here as shipped in the
package. Generated by scripts/weft_ui_licenses.mjs from
web/weft-ui/package-lock.json; do not edit by hand.

${sorted.map((e) => `  ${e.name} ${e.version} (${e.licence})`).join('\n')}
`;
for (const e of sorted) {
  out += `\n${rule}\n${e.name} ${e.version}\nLicence: ${e.licence}\n${e.homepage ? `Source: ${e.homepage}\n` : ''}\n${e.texts.join('\n\n')}\n`;
}

if (check) {
  const current = existsSync(outFile) ? readFileSync(outFile, 'utf8') : '';
  if (current !== out) {
    console.error(`weft_ui_licenses: ${outFile} is missing or out of date; run node scripts/weft_ui_licenses.mjs`);
    process.exit(1);
  }
  console.log(`weft_ui_licenses: ${sorted.length} packages, up to date`);
} else {
  writeFileSync(outFile, out);
  console.log(`weft_ui_licenses: wrote ${sorted.length} packages to ${outFile}`);
}
