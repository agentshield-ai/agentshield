#!/usr/bin/env python3
"""Provenance scan: find text in Weft that matches another product's code or docs.

Weft was first built against W&B Weave's feature set and API. This script
checks that Weft's code, prompts, UI copy and docs are its own expression and
not copies of Weave's source or documentation. See docs/weft/PROVENANCE.md
for the method, the results of the last run and what was done about them.

Two steps:

    # 1. Download the reference corpus (Weave source, old Weave UI, Weave docs)
    python3 scripts/weft_provenance_scan.py fetch --cache ~/.cache/weft-prov

    # 2. Compare Weft against it
    python3 scripts/weft_provenance_scan.py scan --cache ~/.cache/weft-prov
    python3 scripts/weft_provenance_scan.py scan --cache ~/.cache/weft-prov \
        --source /path/to/site-packages/weave --json out.json

``fetch`` needs git and network access. ``scan`` is offline and uses only the
standard library. Extra reference trees (an installed ``weave`` wheel, an
unpacked ``npm pack weave`` tarball) can be added with ``--source``.

How matching works
------------------
Every file is reduced to a stream of word tokens: identifiers are split at
camelCase and snake_case boundaries and lower-cased, and punctuation and
whitespace are dropped. So ``wrapOpenAI`` and ``wrap_openai`` are the same
tokens, and re-wrapping, re-indenting or re-quoting a text does not hide it.
Each run of K consecutive tokens (a shingle, K=8 by default) of a reference
file is hashed into an index. A Weft file is then walked shingle by shingle;
consecutive matching shingles are merged into a run, and runs of at least
``--min-tokens`` tokens are reported with the Weft file:line, the reference
file:line and the matched text.

A match is not necessarily a problem. Method names, JSON keys, route paths and
wire formats have to be the same for compatibility; they are interface facts,
not expression. Matches that were reviewed and accepted for that reason are
listed in ``ACCEPTED`` below (keyed by the first tokens of the matched text)
and are reported separately, so a re-run shows only what is new.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

# What is scanned on the Weft side (relative to the repo root).
WEFT_TARGETS = [
    "sdk/python/src/weft",
    "sdk/typescript/src",
    "internal/weft",
    "web/weft-ui/src",
    "plugins/weft-claude-code",
    "plugins/weft-codex",
    "plugins/weft-harness-common",
    "plugins/weft-openclaw",
    "plugins/weft-pi",
    "scripts",  # filtered to weft_* below
    "docs/weft",
    "sdk/python/README.md",
    "sdk/typescript/README.md",
]
SKIP_DIRS = {"node_modules", "dist", "static", "__pycache__", ".git", "build"}
TEXT_EXT = {
    ".py", ".ts", ".tsx", ".js", ".mjs", ".cjs", ".go", ".md", ".mdx", ".css",
    ".json", ".yaml", ".yml", ".sh", ".html", ".txt", ".toml", ".ipynb",
}

# Reference corpus fetched by `fetch`.
WEAVE_GIT = "https://github.com/wandb/weave.git"
# Weave's web UI (weave-js) was last published in the open repository at
# v0.51.56 (July 2025); later releases no longer contain it.
WEAVE_UI_TAG = "v0.51.56"
DOCS_INDEXES = [
    "https://docs.coreweave.com/_llms/en/forge/weights-and-biases.txt",
    "https://docs.coreweave.com/_llms/en/forge/weights-and-biases/reference.txt",
]
DOCS_PREFIX = "https://docs.coreweave.com/products/wandb/weave"

# Reviewed matches that are interface facts (names, keys, wire formats, the
# Apache licence text, ...). Key: the first 10 normalised tokens of the run.
ACCEPTED: dict[str, str] = {}

_WORD = re.compile(r"[A-Za-z][A-Za-z0-9]*|[0-9]+")
_SUB = re.compile(r"[A-Z]+(?=[A-Z][a-z])|[A-Z]?[a-z]+|[A-Z]+|[0-9]+")


def tokenize(text: str) -> tuple[list[str], list[int]]:
    """Normalised word tokens of ``text`` and the 1-based line of each."""
    toks: list[str] = []
    lines: list[int] = []
    line = 1
    pos = 0
    for m in _WORD.finditer(text):
        line += text.count("\n", pos, m.start())
        pos = m.start()
        for sub in _SUB.findall(m.group(0)):
            toks.append(sub.lower())
            lines.append(line)
    return toks, lines


def read_text(p: Path) -> str | None:
    try:
        if p.stat().st_size > 2_000_000:
            return None
        return p.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None


def walk(root: Path, scripts_filter: bool = False):
    if root.is_file():
        yield root
        return
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for fn in sorted(filenames):
            p = Path(dirpath) / fn
            if p.suffix.lower() not in TEXT_EXT:
                continue
            if scripts_filter and not fn.startswith("weft_"):
                continue
            if fn.endswith((".map", ".min.js", ".d.ts")) or fn in {"package-lock.json"}:
                continue
            yield p


class Index:
    def __init__(self, k: int):
        self.k = k
        self.files: list[Path] = []
        self.lines: list[list[int]] = []
        self.tokens: list[list[str]] = []
        self.table: dict[int, int] = {}  # shingle hash -> file_id << 32 | token pos

    def add(self, p: Path) -> None:
        text = read_text(p)
        if text is None:
            return
        toks, lines = tokenize(text)
        if len(toks) < self.k:
            return
        fid = len(self.files)
        self.files.append(p)
        self.lines.append(lines)
        self.tokens.append(toks)
        k = self.k
        tbl = self.table
        for i in range(len(toks) - k + 1):
            h = hash(tuple(toks[i : i + k]))
            if h not in tbl:
                tbl[h] = (fid << 32) | i


def scan_file(idx: Index, p: Path, min_tokens: int):
    text = read_text(p)
    if text is None:
        return []
    toks, lines = tokenize(text)
    k = idx.k
    hits = []
    for i in range(len(toks) - k + 1):
        v = idx.table.get(hash(tuple(toks[i : i + k])))
        hits.append(v)
    runs = []
    i = 0
    n = len(hits)
    while i < n:
        if hits[i] is None:
            i += 1
            continue
        j = i
        while j + 1 < n and hits[j + 1] is not None:
            j += 1
        length = (j - i) + k
        if length >= min_tokens:
            srcs = {}
            for h in hits[i : j + 1]:
                fid, pos = h >> 32, h & 0xFFFFFFFF
                srcs.setdefault(fid, pos)
            fid, pos = hits[i] >> 32, hits[i] & 0xFFFFFFFF
            runs.append(
                {
                    "file": str(p.relative_to(REPO)) if p.is_relative_to(REPO) else str(p),
                    "line": lines[i],
                    "end_line": lines[j + k - 1],
                    "tokens": length,
                    "text": " ".join(toks[i : j + k]),
                    "source": str(idx.files[fid]),
                    "source_line": idx.lines[fid][pos],
                    "other_sources": len(srcs) - 1,
                }
            )
        i = j + 1
    covered = sum(r["tokens"] for r in runs)
    for r in runs:
        r["file_tokens"] = len(toks)
        r["file_coverage"] = round(covered / max(len(toks), 1), 4)
    return runs


class FuzzyIndex:
    """n-gram -> reference files containing it, for near-verbatim detection.

    n-grams found in more than ``cap`` reference files are treated as common
    phrasing and ignored.
    """

    def __init__(self, idx: Index, n: int, cap: int):
        self.n = n
        self.idx = idx
        table: dict[int, list[int]] = {}
        for fid, toks in enumerate(idx.tokens):
            seen: set[int] = set()
            for i in range(len(toks) - n + 1):
                h = hash(tuple(toks[i : i + n]))
                if h in seen:
                    continue
                seen.add(h)
                lst = table.get(h)
                if lst is None:
                    table[h] = [fid]
                elif len(lst) <= cap:
                    lst.append(fid)
        self.table = {h: v for h, v in table.items() if len(v) <= cap}


def fuzzy_scan_file(fx: FuzzyIndex, p: Path, window: int, threshold: float, min_hits: int):
    """Windows of ``window`` tokens whose n-grams mostly occur in one reference file."""
    text = read_text(p)
    if text is None:
        return []
    toks, lines = tokenize(text)
    n = fx.n
    grams = [fx.table.get(hash(tuple(toks[i : i + n]))) for i in range(len(toks) - n + 1)]
    flagged: list[tuple[int, int, int, float]] = []  # start, end, fid, ratio
    step = max(1, window // 6)
    for s in range(0, max(1, len(grams) - window + 1), step):
        win = grams[s : s + window]
        if not win:
            break
        counts: dict[int, int] = {}
        for g in win:
            if g:
                for fid in g:
                    counts[fid] = counts.get(fid, 0) + 1
        if not counts:
            continue
        fid, c = max(counts.items(), key=lambda kv: kv[1])
        ratio = c / len(win)
        if ratio >= threshold and c >= min_hits:
            if flagged and s <= flagged[-1][1] and flagged[-1][2] == fid:
                st, _, _, r = flagged[-1]
                flagged[-1] = (st, s + len(win) + n - 1, fid, max(r, ratio))
            else:
                flagged.append((s, s + len(win) + n - 1, fid, ratio))
    out = []
    for st, en, fid, ratio in flagged:
        en = min(en, len(toks))
        # locate in the reference file: first matching n-gram of the window
        src_line = 0
        ref = fx.idx.tokens[fid]
        first = {hash(tuple(ref[i : i + n])): i for i in range(len(ref) - n, -1, -1)}
        for i in range(st, en - n + 1):
            j = first.get(hash(tuple(toks[i : i + n])))
            if j is not None:
                src_line = fx.idx.lines[fid][j]
                break
        out.append(
            {
                "file": str(p.relative_to(REPO)) if p.is_relative_to(REPO) else str(p),
                "line": lines[st],
                "end_line": lines[en - 1],
                "tokens": en - st,
                "similarity": round(ratio, 2),
                "text": " ".join(toks[st:en]),
                "source": str(fx.idx.files[fid]),
                "source_line": src_line,
                "mode": "fuzzy",
            }
        )
    return out


def cmd_scan(args) -> int:
    cache = Path(args.cache).expanduser() if args.cache else None
    sources: list[Path] = []
    if cache:
        for sub in ("weave-repo/weave", "weave-repo/sdks/node/src", "weave-ui", "weave-docs"):
            if (cache / sub).exists():
                sources.append(cache / sub)
    sources += [Path(s).expanduser() for s in args.source or []]
    if not sources:
        print("no reference sources: run `fetch` or pass --source", file=sys.stderr)
        return 2
    idx = Index(args.k)
    for root in sources:
        for p in walk(root):
            idx.add(p)
    print(f"indexed {len(idx.files)} reference files, {len(idx.table)} shingles", file=sys.stderr)

    findings = []
    targets: list[Path] = []
    for t in WEFT_TARGETS:
        root = REPO / t
        if not root.exists():
            continue
        for p in walk(root, scripts_filter=(t == "scripts")):
            if p.resolve() == Path(__file__).resolve():
                continue
            targets.append(p)
    for p in targets:
        findings += scan_file(idx, p, args.min_tokens)
    if args.fuzzy:
        fx = FuzzyIndex(idx, args.fuzzy_n, args.fuzzy_cap)
        exact = {(f["file"], ln) for f in findings for ln in range(f["line"], f["end_line"] + 1)}
        for p in targets:
            for f in fuzzy_scan_file(fx, p, args.fuzzy_window, args.fuzzy_threshold, args.fuzzy_min_hits):
                # windows already inside an exact run are not repeated
                if all((f["file"], ln) in exact for ln in range(f["line"], f["end_line"] + 1)):
                    continue
                findings.append(f)

    new, accepted = [], []
    for f in findings:
        key = " ".join(f["text"].split()[:10])
        reason = ACCEPTED.get(key)
        if reason:
            f["accepted"] = reason
            accepted.append(f)
        else:
            new.append(f)
    new.sort(key=lambda f: -f["tokens"])

    if args.json:
        Path(args.json).write_text(json.dumps({"new": new, "accepted": accepted}, indent=1))
    for f in new:
        sim = f" ~{f['similarity']:.0%}" if f.get("mode") == "fuzzy" else ""
        print(f"{f['file']}:{f['line']}-{f['end_line']}  {f['tokens']} tokens{sim}  <=  {f['source']}:{f['source_line']}")
        print(f"    {f['text'][:300]}")
    print(f"\n{len(new)} unreviewed match(es), {len(accepted)} accepted", file=sys.stderr)
    return 1 if (new and args.fail_on_new) else 0


def _git(args: list[str], cwd: Path) -> None:
    subprocess.run(["git", *args], cwd=cwd, check=True)


def _sparse_clone(dest: Path, ref: str, paths: list[str]) -> None:
    if dest.exists():
        return
    dest.mkdir(parents=True)
    _git(["init", "-q"], dest)
    _git(["remote", "add", "origin", WEAVE_GIT], dest)
    _git(["fetch", "-q", "--depth", "1", "--filter=blob:none", "origin", ref], dest)
    _git(["sparse-checkout", "init", "--no-cone"], dest)
    _git(["sparse-checkout", "set", *paths], dest)
    _git(["checkout", "-q", "FETCH_HEAD"], dest)


def _get(url: str) -> bytes:
    with urllib.request.urlopen(url, timeout=60) as r:
        return r.read()


def cmd_fetch(args) -> int:
    cache = Path(args.cache).expanduser()
    cache.mkdir(parents=True, exist_ok=True)
    _sparse_clone(cache / "weave-repo", args.weave_ref, ["/weave/", "/sdks/node/src/"])
    _sparse_clone(
        cache / "weave-ui-repo",
        f"refs/tags/{WEAVE_UI_TAG}",
        ["/weave-js/src/components/PagePanelComponents/Home/Browse3/"],
    )
    ui = cache / "weave-ui"
    if not ui.exists():
        ui.symlink_to(cache / "weave-ui-repo" / "weave-js" / "src" / "components" / "PagePanelComponents" / "Home" / "Browse3")
    docs = cache / "weave-docs"
    docs.mkdir(exist_ok=True)
    urls: set[str] = set()
    for idx in DOCS_INDEXES:
        body = _get(idx).decode("utf-8", "replace")
        urls.update(re.findall(re.escape(DOCS_PREFIX) + r"[^)\s]*\.md", body))

    def one(u: str) -> None:
        name = u[len(DOCS_PREFIX) :].strip("/").replace("/", "__") or "index.md"
        out = docs / name
        if out.exists():
            return
        try:
            out.write_bytes(_get(u))
        except Exception as e:  # noqa: BLE001 - report and carry on
            print(f"failed {u}: {e}", file=sys.stderr)

    with ThreadPoolExecutor(8) as ex:
        list(ex.map(one, sorted(urls)))
    print(f"fetched {len(urls)} doc pages into {docs}", file=sys.stderr)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("fetch", help="download the reference corpus")
    f.add_argument("--cache", required=True)
    f.add_argument("--weave-ref", default="master", help="Weave git ref for the SDK/server source")
    s = sub.add_parser("scan", help="compare Weft against the reference corpus")
    s.add_argument("--cache", help="directory written by `fetch`")
    s.add_argument("--source", action="append", help="extra reference tree (repeatable)")
    s.add_argument("-k", type=int, default=8, help="shingle size in tokens (default 8)")
    s.add_argument("--min-tokens", type=int, default=16, help="report runs of at least this many tokens")
    s.add_argument("--fuzzy", action="store_true",
                   help="also report near-verbatim windows (paraphrase with small edits)")
    s.add_argument("--fuzzy-n", type=int, default=4, help="n-gram size for --fuzzy (default 4)")
    s.add_argument("--fuzzy-window", type=int, default=24, help="window in n-grams (default 24)")
    s.add_argument("--fuzzy-threshold", type=float, default=0.5,
                   help="share of a window's n-grams found in one reference file (default 0.5)")
    s.add_argument("--fuzzy-min-hits", type=int, default=12, help="minimum matching n-grams per window")
    s.add_argument("--fuzzy-cap", type=int, default=25,
                   help="ignore n-grams found in more than this many reference files")
    s.add_argument("--json", help="write all findings to this file")
    s.add_argument("--fail-on-new", action="store_true", help="exit 1 if there are unreviewed matches")
    a = ap.parse_args()
    return cmd_fetch(a) if a.cmd == "fetch" else cmd_scan(a)


if __name__ == "__main__":
    sys.exit(main())
