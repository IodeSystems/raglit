#!/usr/bin/env python3
"""What raglit saves an agent: the same questions, answered by Claude Code once
with grep/read over the files and once with raglit's MCP tools only.

Per run it records what `claude -p --output-format stream-json` reports:
input tokens (uncached + cache write + cache read), output tokens, turns, tool
calls, characters of tool results, cost and wall time. The answer is scored
against the question's regexes (questions.json), so a cheap wrong answer does
not count as a saving.

Each arm also answers a probe that needs no tools ("Reply OK"). Its input
tokens are that arm's fixed overhead (system prompt + tool definitions), which
every model call re-sends; the summary prints it beside the totals.

Everything runs under OUT: a copy of the corpus plus filler documents, a fresh
raglit home and root (never the user's daemon: `serve --embedded`), and the
raw per-run JSON. Nothing is written to the repository.

Usage:
  bench/agent-ab/run.py --out DIR [--model haiku] [--reps 1] [-j 4]
                        [--only id,id] [--set S,S] [--arms grep,raglit] [--filler 300]
                        [--raglit PATH]
"""
import argparse, json, os, re, shutil, statistics, subprocess, sys, time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

HERE = Path(__file__).resolve().parent

PROMPTS = {
    "grep": "The company's documents are the files under the current directory "
            "(Markdown and .mbox email). Use your tools to find the answer.",
    "raglit": "The company's documents are indexed in raglit. Use the raglit tools "
              "to find the answer.",
}
ASK = ("Answer the question in one or two sentences and name the document(s) "
       "you used. If the documents do not answer it, say so.\n\nQuestion: {q}")
PROBE = "Reply with the word OK. Do not use any tools."

RAGLIT_TOOLS = ["mcp__raglit__search", "mcp__raglit__get_document",
                "mcp__raglit__list_documents", "mcp__raglit__list_indexes"]
RAGLIT_DENY = ["mcp__raglit__ingest", "mcp__raglit__ocr",
               "mcp__raglit__search_figures", "mcp__raglit__get_fields",
               "mcp__raglit__index_status"]


def sh(cmd, **kw):
    r = subprocess.run(cmd, capture_output=True, text=True, **kw)
    if r.returncode != 0:
        sys.exit(f"failed: {' '.join(map(str, cmd))}\n{r.stdout}\n{r.stderr}")
    return r.stdout


def prepare(a, out):
    corpus = out / "corpus"
    if corpus.exists():
        shutil.rmtree(corpus)
    shutil.copytree(HERE / "corpus", corpus)
    if a.filler:
        sh([sys.executable, str(HERE / "gen-distractors.py"), str(corpus), "-n", str(a.filler)])
    nfiles = sum(1 for p in corpus.rglob("*") if p.is_file())
    nbytes = sum(p.stat().st_size for p in corpus.rglob("*") if p.is_file())

    home, root = out / "raglit-home", out / "raglit-root"
    env = None
    if "raglit" in a.arms:
        for d in (home, root):
            if d.exists():
                shutil.rmtree(d)
            d.mkdir(parents=True)
        # Attachment extraction is off by default; without it the answer that
        # lives only in an attachment is not in the index at all.
        cfg = {"base_url": a.llm_url, "embed_model": a.embed_model,
               "project": "agentab", "default_index": "default",
               "extract_email_attachments": True}
        (home / "config.json").write_text(json.dumps(cfg))
        env = {**os.environ, "RAGLIT_HOME": str(home), "RAGLIT_ROOT": str(root)}
        print(f"indexing {nfiles} files with {a.embed_model} ...", flush=True)
        idx = [a.raglit, "index", "--home", str(home), "--embed"]
        sh(idx + [str(corpus)], env=env)
        # `raglit index DIR` walks text and PDF only, so mail archives are
        # named one by one; their attachments land under the home and are
        # indexed by a second pass.
        sh(idx + [str(p) for p in sorted(corpus.rglob("*.mbox"))], env=env)
        if (home / "attachments").exists() and any((home / "attachments").rglob("*.*")):
            sh(idx + [str(home / "attachments")], env=env)
        mcp = {"mcpServers": {"raglit": {
            "command": a.raglit,
            "args": ["serve", "--home", str(home), "--embedded", "--embed"],
            "env": {"RAGLIT_HOME": str(home), "RAGLIT_ROOT": str(root)}}}}
        (out / "mcp-raglit.json").write_text(json.dumps(mcp))
    (out / "mcp-none.json").write_text(json.dumps({"mcpServers": {}}))
    (out / "empty").mkdir(exist_ok=True)
    return corpus, nfiles, nbytes


def claude_cmd(a, out, arm, prompt):
    cmd = ["claude", "-p", prompt, "--output-format", "stream-json", "--verbose",
           "--model", a.model, "--strict-mcp-config", "--setting-sources", "project",
           "--no-session-persistence", "--max-budget-usd", str(a.max_usd)]
    if arm == "grep":
        cmd += ["--mcp-config", str(out / "mcp-none.json"),
                "--tools", "Read,Grep,Glob", "--allowedTools", "Read,Grep,Glob"]
    else:
        cmd += ["--mcp-config", str(out / "mcp-raglit.json"), "--tools", "",
                "--allowedTools", ",".join(RAGLIT_TOOLS),
                "--disallowedTools", ",".join(RAGLIT_DENY)]
    return cmd


def run_one(a, out, corpus, arm, qid, prompt, rep):
    cwd = corpus if arm == "grep" else out / "empty"
    t0 = time.time()
    try:
        p = subprocess.run(claude_cmd(a, out, arm, prompt), cwd=cwd, capture_output=True,
                           text=True, timeout=a.timeout)
        lines, err = p.stdout.splitlines(), p.stderr[-2000:]
    except subprocess.TimeoutExpired as e:
        lines, err = (e.stdout or b"").decode(errors="replace").splitlines(), "timeout"
    wall = time.time() - t0
    raw = out / "raw" / f"{arm}-{qid}-{rep}.jsonl"
    raw.parent.mkdir(exist_ok=True)
    raw.write_text("\n".join(lines))

    calls, result_chars, final = {}, 0, None
    for ln in lines:
        try:
            ev = json.loads(ln)
        except ValueError:
            continue
        if ev.get("type") == "result":
            final = ev
        msg = ev.get("message") or {}
        content = msg.get("content")
        for c in content if isinstance(content, list) else []:
            if c.get("type") == "tool_use":
                calls[c["name"]] = calls.get(c["name"], 0) + 1
            elif c.get("type") == "tool_result":
                body = c.get("content")
                result_chars += len(body if isinstance(body, str) else json.dumps(body))
    u = (final or {}).get("usage") or {}
    inp = u.get("input_tokens", 0) + u.get("cache_creation_input_tokens", 0) + u.get("cache_read_input_tokens", 0)
    return {"arm": arm, "id": qid, "rep": rep, "ok": final is not None and not final.get("is_error"),
            "error": None if final else err, "answer": (final or {}).get("result", ""),
            "input_tokens": inp, "output_tokens": u.get("output_tokens", 0),
            "turns": (final or {}).get("num_turns", 0), "tool_calls": calls,
            "tool_result_chars": result_chars, "cost_usd": (final or {}).get("total_cost_usd", 0),
            "wall_s": round(wall, 1)}


def facts(q, answer):
    """How many of a surfacing question's facts the answer brought up."""
    return sum(bool(re.search(r, answer, re.IGNORECASE)) for r in q.get("of", []))


def score(q, answer):
    f = re.IGNORECASE
    return (all(re.search(r, answer, f) for r in q.get("all", []))
            and not any(re.search(r, answer, f) for r in q.get("none", []))
            and facts(q, answer) >= q.get("k", 0))


def mean(xs):
    return statistics.mean(xs) if xs else 0


def summarize(a, out, qs, rows, probes, nfiles, nbytes):
    byid = {q["id"]: q for q in qs}
    L = [f"# agent A/B: grep vs raglit", "",
         f"model `{a.model}` · reps {a.reps} · corpus {nfiles} files, {nbytes:,} bytes "
         f"({a.filler} filler) · embed `{a.embed_model}`", ""]
    L += ["| set | arm | correct | input tok (mean) | output tok | turns | tool calls | tool result chars | cost USD (sum) | wall s (mean) |",
          "|---|---|---|---|---|---|---|---|---|---|"]
    sets = sorted({byid[r["id"]].get("set", "lookup") for r in rows})
    for arm, st in [(arm, st) for st in sets + ["all"] for arm in a.arms]:
        inset = [r for r in rows if r["arm"] == arm and st in ("all", byid[r["id"]].get("set", "lookup"))]
        rs = [r for r in inset if r["ok"]]
        if not rs:
            L.append(f"| {st} | {arm} | no completed runs | | | | | | | |")
            continue
        L.append("| {} | {} | {}/{} | {:,.0f} | {:,.0f} | {:.1f} | {:.1f} | {:,.0f} | {:.4f} | {:.1f} |".format(
            st, arm, sum(r["correct"] for r in rs), len(inset),
            mean([r["input_tokens"] for r in rs]),
            mean([r["output_tokens"] for r in rs]), mean([r["turns"] for r in rs]),
            mean([sum(r["tool_calls"].values()) for r in rs]), mean([r["tool_result_chars"] for r in rs]),
            sum(r["cost_usd"] for r in rs), mean([r["wall_s"] for r in rs])))
    L += ["", "Fixed overhead (input tokens of a no-tool probe; re-sent on every model call): " +
          ", ".join(f"{k} {v:,}" for k, v in probes.items()) + ".", "",
          "| question | wart | " + " | ".join(f"{arm} correct | {arm} input tok" for arm in a.arms) + " |",
          "|---|---|" + "---|---|" * len(a.arms)]
    for qid, q in byid.items():
        cells = []
        for arm in a.arms:
            rs = [r for r in rows if r["arm"] == arm and r["id"] == qid]
            fx = f" ({mean([facts(q, r['answer']) for r in rs]):.1f}/{len(q['of'])} facts)" if q.get("of") else ""
            cells += [f"{sum(r['correct'] for r in rs)}/{len(rs)}{fx}",
                      f"{mean([r['input_tokens'] for r in rs if r['ok']]):,.0f}"]
        if any(r["id"] == qid for r in rows):
            L.append(f"| {qid} | {q['wart']} | " + " | ".join(cells) + " |")
    (out / "summary.md").write_text("\n".join(L) + "\n")
    print("\n".join(L))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--model", default="haiku")
    ap.add_argument("--reps", type=int, default=1)
    ap.add_argument("-j", "--jobs", type=int, default=4)
    ap.add_argument("--only", default="")
    ap.add_argument("--arms", default="grep,raglit")
    ap.add_argument("--set", default="", help="lookup, contextual, surfacing (comma list); default all")
    ap.add_argument("--filler", type=int, default=300)
    ap.add_argument("--raglit", default="raglit")
    ap.add_argument("--llm-url", default=os.environ.get("RAGLIT_LLM_URL", ""))
    ap.add_argument("--embed-model", default="nomic-embed-text")
    ap.add_argument("--timeout", type=int, default=600)
    ap.add_argument("--max-usd", type=float, default=1.0)
    ap.add_argument("--rescore", action="store_true",
                    help="score OUT/results.jsonl again against questions.json; run nothing")
    a = ap.parse_args()
    a.arms = a.arms.split(",")
    if "raglit" in a.arms and not a.llm_url and not a.rescore:
        sys.exit("--llm-url (or $RAGLIT_LLM_URL) is required for the raglit arm's embedder")
    out = Path(a.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    qs = json.loads((HERE / "questions.json").read_text())
    if a.only:
        keep = set(a.only.split(","))
        qs = [q for q in qs if q["id"] in keep]
    if a.set:
        keep = set(a.set.split(","))
        qs = [q for q in qs if q.get("set", "lookup") in keep]
    if a.rescore:
        meta = json.loads((out / "meta.json").read_text())
        for k in ("model", "reps", "filler", "embed_model"):
            if k in meta:
                setattr(a, k, meta[k])
        rows = [json.loads(l) for l in (out / "results.jsonl").read_text().splitlines()]
        byid = {q["id"]: q for q in qs}
        for r in rows:
            r["correct"] = r["ok"] and score(byid[r["id"]], r["answer"])
        summarize(a, out, qs, rows, meta["probes"], meta["nfiles"], meta["nbytes"])
        return
    corpus, nfiles, nbytes = prepare(a, out)

    probes = {}
    for arm in a.arms:
        r = run_one(a, out, corpus, arm, "_probe", PROBE, 0)
        probes[arm] = r["input_tokens"]

    jobs = [(arm, q, rep) for rep in range(a.reps) for q in qs for arm in a.arms]
    rows = []
    with ThreadPoolExecutor(a.jobs) as ex:
        futs = [ex.submit(run_one, a, out, corpus, arm, q["id"],
                          PROMPTS[arm] + " " + ASK.format(q=q["q"]), rep) for arm, q, rep in jobs]
        for (arm, q, rep), f in zip(jobs, futs):
            r = f.result()
            r["correct"] = r["ok"] and score(q, r["answer"])
            rows.append(r)
            print(f"{arm:6} {q['id']:18} rep{rep} {'ok ' if r['correct'] else 'BAD'} "
                  f"in={r['input_tokens']:>7,} turns={r['turns']} {r['wall_s']}s", flush=True)
    (out / "meta.json").write_text(json.dumps({"probes": probes, "nfiles": nfiles, "nbytes": nbytes, "model": a.model,
                                                 "reps": a.reps, "filler": a.filler, "embed_model": a.embed_model}))
    with open(out / "results.jsonl", "w") as fh:
        for r in rows:
            fh.write(json.dumps(r) + "\n")
    summarize(a, out, qs, rows, probes, nfiles, nbytes)


if __name__ == "__main__":
    main()
