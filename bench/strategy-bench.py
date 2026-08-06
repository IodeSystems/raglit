#!/usr/bin/env python3
"""Score raglit's OWN read path across a model x strategy matrix.

Why this exists alongside bench/README.md's llm-bench run:

  llm-bench feeds a probe's pre-rendered `_fixture/page.png` straight to a model
  endpoint. It never enters raglit. That measures MODEL + PROMPT, which is what
  ranked chandra against Qwen — and it is structurally unable to say anything
  about render resolution, descent or tiling, because none of that code runs.

  This harness drives `raglit ocr` instead, so the answer includes raglit's
  pipeline: the cheap tier, cascade-vs-assist, and the render policy a strategy
  carries. It scores against the SAME probe.md checks, so the two are comparable.

  Note it uses raglit's own transcription prompt, not the probe's. That is
  deliberate: the question here is what production does, and production does not
  send the probe's prompt.

THE STRATEGY AXIS ONLY MOVES ON A PDF. A strategy's render policy is applied by
renderDPIFor while rasterising a PDF page; hand it an already-rendered PNG and
there is nothing left to decide, so every strategy scores identically. Probes
with a `source:` PDF vary on both axes; PNG-only probes vary on model alone and
are reported as such rather than quietly producing a flat row.

Usage:
  bench/strategy-bench.py --models chandra-ocr-2,Qwen3-6-27B-MPT \
                          --strategies '' --out /tmp/sb
"""
import argparse, json, os, re, shutil, subprocess, sys, time
from pathlib import Path

HERE = Path(__file__).resolve().parent
PROBES = HERE / "probes"


def parse_probe(d: Path):
    """probe.md → prompt-independent facts: the fixture and the checks.

    Checks are `response_contains:` / `response_not_contains:` lines. A negative
    check matters as much as a positive one: it pins a KNOWN-BAD reading a model
    in this fleet actually produced, so a config that regresses to it fails
    loudly instead of drifting a score.
    """
    md = (d / "probe.md").read_text()
    checks = []
    for m in re.finditer(r"^- (response_not_contains|response_contains):\s*(.+)$", md, re.M):
        checks.append((m.group(1) == "response_contains", m.group(2).strip().strip('"')))
    src = None
    if m := re.search(r"^source:\s*(.+)$", md, re.M):
        src = m.group(1).strip()
    fixture = d / "_fixture" / "page.png"
    return dict(name=d.name, checks=checks, fixture=fixture if fixture.exists() else None, source=src)


def score(text, checks):
    """Returns (passed, total, [missed]). Case-insensitive: a reader that
    normalises capitalisation is a different failure from one that cannot read
    the characters, and these checks are about the latter."""
    t = (text or "").lower()
    missed, ok = [], 0
    for want, needle in checks:
        hit = needle.lower() in t
        if hit == want:
            ok += 1
        else:
            missed.append(("missing " if want else "FORBIDDEN ") + needle)
    return ok, len(checks), missed


def run_one(raglit, target, model, strategy, home, trace_dir, timeout):
    cmd = [raglit, "ocr", "--llm-model", model, "--trace", str(trace_dir)]
    if strategy:
        cmd += ["--strategy", strategy]
    if home:
        cmd += ["--home", home]
    cmd.append(str(target))
    t0 = time.time()
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        out = p.stdout
        err = None if p.returncode == 0 else (p.stderr.strip()[-300:] or f"exit {p.returncode}")
    except subprocess.TimeoutExpired:
        out, err = "", f"TIMEOUT after {timeout}s"
    return out, err, time.time() - t0


def trace_rows(trace_dir):
    f = Path(trace_dir) / "log.jsonl"
    if not f.exists():
        return []
    rows = []
    for line in f.read_text().splitlines():
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return rows


def diagnose(rows, model):
    """What the trace says about this run, in one line.

    Only facts the record actually holds — no inference about WHY a read is
    wrong, because the trace cannot see the pixels. Output length against
    duration is what exposes a degenerate loop, and tokens_est is what exposes a
    region nobody could have read.
    """
    reads = [r for r in rows if r.get("kind") == "vision.read" and r.get("model") == model]
    starts = [r for r in rows if r.get("kind") == "page.start" and r.get("model") == model]
    if not reads:
        errs = [r for r in rows if r.get("kind") == "vision.error" and r.get("model") == model]
        return f"no read ({errs[-1].get('err', '')[:60]})" if errs else "no read recorded"
    chars = sum(r.get("chars", 0) for r in reads)
    ms = sum(r.get("duration_ms", 0) for r in reads)
    tok = sum(s.get("tokens_est", 0) for s in starts)
    shr = sum(r.get("downscales", 0) for r in reads)
    note = ""
    if chars >= 8000:
        note = "  <- at the output ceiling, suspect a loop"
    elif tok and tok < 500:
        note = "  <- under-resolved input"
    return f"{chars:6}ch {ms/1000:6.1f}s  img~{tok}tok  downscales={shr}{note}"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--raglit", default=shutil.which("raglit") or "raglit")
    ap.add_argument("--models", default="chandra-ocr-2,Qwen3-6-27B-MPT")
    ap.add_argument("--strategies", default="", help="comma-separated; empty entry = project default")
    ap.add_argument("--probes", default="ocr-*")
    ap.add_argument("--home", default="", help="raglit home holding the config + strategies")
    ap.add_argument("--out", default="bench-out")
    ap.add_argument("--timeout", type=int, default=900)
    a = ap.parse_args()

    models = [m.strip() for m in a.models.split(",") if m.strip()]
    strategies = [s.strip() for s in a.strategies.split(",")] or [""]
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)

    probes = sorted(p for p in PROBES.glob(a.probes) if (p / "probe.md").exists())
    if not probes:
        sys.exit(f"no probes matching {a.probes} under {PROBES}")

    results = []
    for pd in probes:
        pr = parse_probe(pd)
        target = pr["fixture"]
        if target is None:
            print(f"skip {pr['name']}: no _fixture/page.png (run bench/make-fixtures.sh)")
            continue
        png_only = pr["source"] is None
        for model in models:
            for strat in strategies:
                tag = f"{pr['name']}|{model}|{strat or 'default'}"
                td = out / "traces" / re.sub(r"[^\w.-]", "_", tag)
                td.mkdir(parents=True, exist_ok=True)
                text, err, wall = run_one(a.raglit, target, model, strat, a.home, td, a.timeout)
                ok, total, missed = score(text, pr["checks"])
                rows = trace_rows(td)
                results.append(dict(probe=pr["name"], model=model, strategy=strat or "default",
                                    passed=ok, total=total, missed=missed, wall_s=round(wall, 1),
                                    err=err, png_only=png_only, diag=diagnose(rows, model)))
                print(f"  {tag:58} {ok}/{total} {wall:6.1f}s" + (f"  ERR {err}" if err else ""))
                (out / "readings").mkdir(exist_ok=True)
                (out / "readings" / (re.sub(r'[^\w.-]', '_', tag) + ".txt")).write_text(text or "")

    (out / "results.json").write_text(json.dumps(results, indent=1))

    print("\n=== matrix (checks passed) ===")
    hdr = f"{'probe':26}" + "".join(f"{m[:14]+'/'+ (s or 'def')[:6]:>24}"
                                     for m in models for s in strategies)
    print(hdr)
    for pd in probes:
        name = pd.name
        rs = [r for r in results if r["probe"] == name]
        if not rs:
            continue
        line = f"{name:26}"
        for m in models:
            for s in strategies:
                r = next((x for x in rs if x["model"] == m and x["strategy"] == (s or "default")), None)
                line += f"{(str(r['passed'])+'/'+str(r['total'])) if r else '-':>24}"
        print(line)

    if len(strategies) > 1 and any(r["png_only"] for r in results):
        n = len({r["probe"] for r in results if r["png_only"]})
        print(f"\nNOTE: {n} probe(s) are PNG-only, so the strategy axis cannot move on them —")
        print("      a render policy is applied while rasterising a PDF, and these are pre-rendered.")

    print("\n=== failures, with what the trace says ===")
    for r in results:
        if r["passed"] < r["total"] or r["err"]:
            print(f"  {r['probe']} / {r['model']} / {r['strategy']}")
            if r["err"]:
                print(f"      error: {r['err']}")
            for m in r["missed"]:
                print(f"      {m}")
            print(f"      trace: {r['diag']}")
    print(f"\nwrote {out}/results.json, {out}/readings/, {out}/traces/")


if __name__ == "__main__":
    main()
