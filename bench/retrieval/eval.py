#!/usr/bin/env python3
"""Score raglit's search against a query set with known relevant files.

No agent and no sampling: each query goes through `raglit search` once per
mode, and the ranked documents are compared with the files that actually
answered it (fetch_gitlab.py). Reported per mode:

  success@k  share of queries with at least one relevant file in the top k
  recall@k   mean share of a query's relevant files in the top k
  MRR        mean of 1/rank of the first relevant file (0 past --limit)

`grep` is the baseline an agent would build with grep: files ranked by how
many distinct query words they contain, then by total matches, over the same
indexed files.

Usage:
  eval.py --snapshot DIR --queries FILE --out DIR --llm-url URL
          [--modes grep,bm25,vec,hybrid] [-n 20] [--raglit PATH] [--index-only]
          [--repos gitaly,cli] [--frag-window N --frag-stride N --frag-floor N]
"""
import argparse, json, os, re, shutil, statistics, subprocess, sys
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from raglitd import Daemon  # noqa: E402

REPOS = ["gitlab", "gitaly", "cli", "handbook"]
INCLUDE = ["*.go", "*.rb", "*.proto", "*.md", "*.yml", "*.yaml"]
IGNORE = ["*_test.go", "*_spec.rb", "*.pb.go", "**/spec/**", "**/specs/**", "**/test/**",
          "**/tests/**", "**/testdata/**", "**/qa/**", "**/changelogs/**", "**/doc-locale/**",
          "**/locale/**", "CHANGELOG*"]
STOP = set("""a an and are as at be by can for from has have how i in is it its of on or
should that the this to was we when where which while why will with not no do does
add added adding allow allows support use using make new into via all any only""".split())
HIT = re.compile(r"^\s*\d+\.\s+\[[^\]]*\]\s+(\S+)")


def sh(cmd, env, **kw):
    r = subprocess.run(cmd, capture_output=True, text=True, env=env, **kw)
    if r.returncode != 0:
        sys.exit(f"failed: {' '.join(cmd)}\n{r.stdout[-2000:]}\n{r.stderr[-2000:]}")
    return r.stdout


def prepare(a, snap, out):
    """A throwaway daemon on OUT/raglit-root (never the user's), and a project
    home whose config names the index roots. The index persists in the root, so
    a rerun reuses it unless --reindex."""
    home, root = out / "raglit-home", out / "raglit-root"
    home.mkdir(parents=True, exist_ok=True)
    model = {"base_url": a.llm_url, "embed_model": a.embed_model}
    for k in ("frag_window", "frag_stride", "frag_floor"):
        if getattr(a, k):
            model[k] = getattr(a, k)
    cfg = {**model, "project": "rtbench", "default_index": "org",
           "indexes": {"org": {"roots": [str(snap / r) for r in a.repos],
                               "include": INCLUDE, "ignore": IGNORE}}}
    (home / "config.json").write_text(json.dumps(cfg, indent=1))
    idx = root / "indexes" / "rtbench__org"
    if a.reindex and idx.exists():
        shutil.rmtree(idx)
    fresh = not idx.exists()
    d = Daemon(a.raglit, root, model, embed=True)
    env = {**d.env, "RAGLIT_HOME": str(home)}
    if fresh:
        print("sync + embed (slow: ~600 files/min) ...", file=sys.stderr, flush=True)
        print(sh([a.raglit, "sync", "--home", str(home)], env).strip(), file=sys.stderr)
        print(sh([a.raglit, "work", "--home", str(home), "--index", "org"], env).strip(), file=sys.stderr)
    return home, env, d


def indexed_files(d, snap):
    docs = d.get("/api/documents?index=rtbench__org")["documents"]
    return {rel(x["path"], snap) for x in docs} - {None}


def rel(p, snap):
    p = p.removeprefix("file://")
    try:
        return str(Path(p).resolve().relative_to(snap))
    except ValueError:
        return None


def raglit_rank(a, home, env, snap, mode, q):
    r = subprocess.run([a.raglit, "search", "--home", str(home), "--index", "org",
                        "--mode", mode, "-n", str(a.limit * 3), q],
                       capture_output=True, text=True, errors="replace", env=env)
    ranked = []
    for ln in r.stdout.splitlines():
        m = HIT.match(ln)
        if m:
            p = rel(m.group(1), snap)
            if p and p not in ranked:
                ranked.append(p)
    return ranked[:a.limit]


def terms(q):
    words = re.findall(r"[A-Za-z][A-Za-z0-9_]{2,}", q.lower())
    return [w for w in dict.fromkeys(words) if w not in STOP]


def grep_rank(a, snap, files, q):
    distinct, total = Counter(), Counter()
    for t in terms(q):
        r = subprocess.run(["grep", "-rIciwF", "--exclude-dir=.git", "-e", t] + a.repos,
                           capture_output=True, text=True, errors="replace", cwd=snap)
        for ln in r.stdout.splitlines():
            path, _, n = ln.rpartition(":")
            if n != "0" and path in files:
                distinct[path] += 1
                total[path] += int(n)
    ranked = sorted(distinct, key=lambda p: (-distinct[p], -total[p], p))
    return ranked[:a.limit]


def metrics(ranked, relevant):
    rel = set(relevant)
    first = next((i + 1 for i, p in enumerate(ranked) if p in rel), None)
    out = {"mrr": 1 / first if first else 0.0, "first": first}
    for k in (1, 5, 10, 20):
        top = set(ranked[:k])
        out[f"s@{k}"] = float(bool(top & rel))
        out[f"r@{k}"] = len(top & rel) / len(rel)
    return out


def table(rows, modes, label, pick):
    L = [f"### {label}", "", "| mode | n | success@1 | success@5 | success@10 | recall@10 | recall@20 | MRR |",
         "|---|---|---|---|---|---|---|---|"]
    for m in modes:
        rs = [r["m"][m] for r in rows if pick(r)]
        if not rs:
            continue
        avg = lambda k: statistics.mean(x[k] for x in rs)
        L.append(f"| {m} | {len(rs)} | {avg('s@1'):.2f} | {avg('s@5'):.2f} | {avg('s@10'):.2f} | "
                 f"{avg('r@10'):.2f} | {avg('r@20'):.2f} | {avg('mrr'):.3f} |")
    return L + [""]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--snapshot", required=True)
    ap.add_argument("--queries")
    ap.add_argument("--out", required=True)
    ap.add_argument("--llm-url", default=os.environ.get("RAGLIT_LLM_URL", ""))
    ap.add_argument("--embed-model", default="nomic-embed-text")
    ap.add_argument("--modes", default="grep,bm25,vec,hybrid")
    ap.add_argument("-n", "--limit", type=int, default=20)
    ap.add_argument("-j", "--jobs", type=int, default=6)
    ap.add_argument("--raglit", default="raglit")
    ap.add_argument("--index-only", action="store_true")
    ap.add_argument("--reindex", action="store_true")
    ap.add_argument("--repos", default=",".join(REPOS), help="subset of the snapshot to index and query")
    ap.add_argument("--frag-window", type=int, default=0, help="fragment window in characters (raglit default 1000)")
    ap.add_argument("--frag-stride", type=int, default=0)
    ap.add_argument("--frag-floor", type=int, default=0)
    a = ap.parse_args()
    a.repos = a.repos.split(",")
    if not a.llm_url:
        sys.exit("--llm-url (or $RAGLIT_LLM_URL) is required for the embedder")
    snap, out = Path(a.snapshot).resolve(), Path(a.out).resolve()
    home, env, d = prepare(a, snap, out)
    if a.index_only:
        return
    files = indexed_files(d, snap)
    qs = [json.loads(l) for l in open(a.queries)]
    modes = a.modes.split(",")
    dropped = 0
    for q in qs:
        keep = [f for f in q["relevant"] if f in files]
        dropped += len(q["relevant"]) - len(keep)
        q["relevant"] = keep
    qs = [q for q in qs if q["relevant"] and set(q["repos"]) <= set(a.repos)]
    print(f"{len(qs)} queries, {len(files)} indexed files; {dropped} relevant files not in the index dropped",
          file=sys.stderr)

    def one(q):
        row = {"id": q["id"], "q": q["q"], "relevant": q["relevant"], "repos": q["repos"], "m": {}, "top": {}}
        for m in modes:
            ranked = grep_rank(a, snap, files, q["q"]) if m == "grep" else raglit_rank(a, home, env, snap, m, q["q"])
            row["m"][m] = metrics(ranked, q["relevant"])
            row["top"][m] = ranked
        return row

    with ThreadPoolExecutor(a.jobs) as ex:
        rows = list(ex.map(one, qs))
    with open(out / "results.jsonl", "w") as fh:
        for r in rows:
            fh.write(json.dumps(r) + "\n")
    is_doc = lambda r: all(f.endswith(".md") for f in r["relevant"])
    L = [f"## retrieval: {len(rows)} queries, {len(files)} indexed files, top {a.limit}", ""]
    L += table(rows, modes, "all", lambda r: True)
    L += table(rows, modes, "relevant files span 2+ repos", lambda r: len(r["repos"]) > 1)
    L += table(rows, modes, "code (any relevant file is not .md)", lambda r: not is_doc(r))
    L += table(rows, modes, "docs only (all relevant files .md)", is_doc)
    (out / "summary.md").write_text("\n".join(L))
    print("\n".join(L))


if __name__ == "__main__":
    main()
