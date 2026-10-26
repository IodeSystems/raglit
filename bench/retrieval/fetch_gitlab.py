#!/usr/bin/env python3
"""Build a retrieval query set from GitLab's public history: nobody writes the
questions.

A query is the TITLE OF AN ISSUE that a merged merge request closed: a person
describing what they needed, before anyone wrote the code. The relevant
documents are the files changed by every merged MR related to that issue, in
any of the indexed projects (an issue in gitlab-org/gitlab whose fix also
touched Gitaly counts both). MR titles are not used as queries: they are
written in the code's own vocabulary ("gitcmd: stop unpacking ...").

Files are kept as relevant only if they exist in the local snapshot and would
be indexed (extension and path filters below); tests, specs, changelogs and
generated code are dropped. A query with no relevant file left, or more than
--max-files, is dropped.

API responses are cached under OUT/cache, so a rerun costs nothing.

Usage:
  fetch_gitlab.py --snapshot DIR --out DIR [--since 2026-06-01] [--mrs 400]
  (DIR holds clones named gitlab, gitaly, cli, handbook)
"""
import argparse, hashlib, json, os, re, sys, time, urllib.parse, urllib.request
from pathlib import Path

API = "https://gitlab.com/api/v4"
PROJECTS = {"gitlab-org/gitlab": "gitlab", "gitlab-org/gitaly": "gitaly", "gitlab-org/cli": "cli"}
EXTS = (".go", ".rb", ".proto", ".md", ".yml", ".yaml")
DROP = re.compile(r"(^|/)(spec|specs|test|tests|testdata|qa|changelogs?|locale|doc-locale)/|_test\.go$|_spec\.rb$|\.pb\.go$|CHANGELOG")

CACHE = None
last_call = [0.0]


def get(path, params=None):
    url = f"{API}{path}" + ("?" + urllib.parse.urlencode(params) if params else "")
    key = CACHE / (hashlib.sha1(url.encode()).hexdigest() + ".json")
    if key.exists():
        return json.loads(key.read_text())
    for attempt in range(5):
        wait = 0.25 - (time.time() - last_call[0])
        if wait > 0:
            time.sleep(wait)
        last_call[0] = time.time()
        try:
            with urllib.request.urlopen(url, timeout=60) as r:
                data = json.loads(r.read())
            key.write_text(json.dumps(data))
            return data
        except urllib.error.HTTPError as e:
            if e.code in (429, 500, 502, 503):
                time.sleep(5 * (attempt + 1))
                continue
            if e.code in (403, 404):
                key.write_text("null")
                return None
            raise
    raise RuntimeError(f"gave up on {url}")


def pid(project):
    return urllib.parse.quote(project, safe="")


def changed_files(project, iid):
    out, page = [], 1
    while True:
        d = get(f"/projects/{pid(project)}/merge_requests/{iid}/diffs", {"per_page": 100, "page": page})
        if not d:
            break
        out += [x["new_path"] for x in d if not x.get("deleted_file")]
        if len(d) < 100:
            break
        page += 1
    return out


ISSUE_URL = re.compile(r"gitlab\.com/([\w.-]+/[\w./-]+?)/-/issues/(\d+)")
ISSUE_REF = re.compile(r"(?:Closes|Fixes|Resolves|Related to|Relates to|Part of|Contributes to)\s*:?\s*#(\d+)", re.I)


def linked_issues(project, text):
    found = [(m.group(1), int(m.group(2))) for m in ISSUE_URL.finditer(text)]
    found += [(project, int(m.group(1))) for m in ISSUE_REF.finditer(text)]
    return list(dict.fromkeys(found))[:3]


def indexed(snapshot, repo, path):
    return (path.endswith(EXTS) and not DROP.search(path)
            and (snapshot / repo / path).is_file())


def main():
    global CACHE
    ap = argparse.ArgumentParser()
    ap.add_argument("--snapshot", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--since", default="2026-06-01")
    ap.add_argument("--mrs", type=int, default=400, help="merged MRs to scan per project")
    ap.add_argument("--max-files", type=int, default=12)
    a = ap.parse_args()
    snap, out = Path(a.snapshot).resolve(), Path(a.out).resolve()
    CACHE = out / "cache"
    CACHE.mkdir(parents=True, exist_ok=True)

    issues = {}  # (project, iid) -> issue
    for project in PROJECTS:
        seen, page = 0, 1
        while seen < a.mrs:
            mrs = get(f"/projects/{pid(project)}/merge_requests",
                      {"state": "merged", "merged_after": a.since, "per_page": 100, "page": page,
                       "order_by": "merged_at"})
            if not mrs:
                break
            for mr in mrs:
                seen += 1
                closes = get(f"/projects/{pid(project)}/merge_requests/{mr['iid']}/closes_issues") or []
                for iss in closes:
                    p = iss.get("references", {}).get("full", "").split("#")[0] or project
                    issues[(p, iss["iid"])] = iss
                if closes:
                    continue
                # gitlab-org/gitlab MRs rarely use "Closes"; they link the issue
                # in the description ("Related to #123" or a full issue URL).
                for p, iid in linked_issues(project, mr.get("description") or ""):
                    if (p, iid) not in issues and p in PROJECTS:
                        iss = get(f"/projects/{pid(p)}/issues/{iid}")
                        if iss:
                            issues[(p, iid)] = iss
            page += 1
            print(f"{project}: scanned {seen} MRs, {len(issues)} closing issues so far", file=sys.stderr)

    queries = []
    for (project, iid), iss in sorted(issues.items()):
        rel = get(f"/projects/{pid(project)}/issues/{iid}/related_merge_requests", {"per_page": 100}) or []
        files, mrs = set(), []
        for mr in rel:
            if mr.get("state") != "merged":
                continue
            mp = mr.get("references", {}).get("full", "").split("!")[0]
            if mp not in PROJECTS:
                continue
            repo = PROJECTS[mp]
            mrs.append(f"{mp}!{mr['iid']}")
            files |= {f"{repo}/{f}" for f in changed_files(mp, mr["iid"]) if indexed(snap, repo, f)}
        if not files or len(files) > a.max_files:
            continue
        queries.append({"id": f"{project}#{iid}", "q": iss["title"], "relevant": sorted(files),
                        "mrs": mrs, "repos": sorted({f.split('/')[0] for f in files}),
                        "url": iss.get("web_url")})
    with open(out / "queries.jsonl", "w") as fh:
        for q in queries:
            fh.write(json.dumps(q) + "\n")
    shas = {r: os.popen(f"git -C {snap / r} rev-parse HEAD").read().strip() for r in ("gitlab", "gitaly", "cli", "handbook")}
    (out / "snapshot.json").write_text(json.dumps(shas, indent=2))
    multi = sum(len(q["repos"]) > 1 for q in queries)
    print(f"{len(queries)} queries ({multi} span repos) -> {out / 'queries.jsonl'}", file=sys.stderr)


if __name__ == "__main__":
    main()
