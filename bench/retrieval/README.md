# retrieval: does search return the documents that answered real work?

An open stand-in for a large, layered engineering organization: GitLab. Its
monolith (Rails), Gitaly (a Go service with its own gRPC protocol definitions),
the `glab` CLI client and the public handbook. Together they have domain teams,
event and metric definitions, table ownership docs, design and process pages,
and cross-service changes.

Nobody writes the questions. `fetch_gitlab.py` takes merged merge requests,
finds the issue each one closed or linked, and uses the **issue title** as the
query: a person describing a need before the code existed. The relevant files
are those changed by every merged MR related to that issue, in any indexed
project, minus tests, specs, changelogs and generated code.

`eval.py` indexes the snapshot with raglit (a throwaway daemon on its own root
and port, `bench/raglitd.py`; never the user's daemon), runs every query through `raglit search` per mode, and scores
success@k, recall@k and MRR. `grep` is the baseline an agent builds by hand:
files ranked by how many distinct query words they contain.

    # clones (shallow; the monolith sparse, see below) into SNAP/{gitlab,gitaly,cli,handbook}
    fetch_gitlab.py --snapshot SNAP --out Q --mrs 1500
    eval.py --snapshot SNAP --queries Q/queries.jsonl --out OUT --llm-url URL

Monolith slice (sparse checkout, non-cone): `app/models app/services
app/graphql app/workers lib/gitlab ee/app/models ee/app/services db/docs
config/events config/metrics doc/development`. Relevant files outside the
slice are dropped, and a query left with none is dropped; this affects every
mode equally.

## Limits

- Changed files are a proxy for "the documents that answer this". An MR also
  touches files nobody would search for (a registry, a feature-flag list), and
  misses the page a person read before starting. Recall is therefore a floor.
- Issue titles from engineers often carry code words ("Deprecate RPC
  FindAllCommits"), which favours grep. That is how such orgs write tickets.
- The handbook is indexed but is not yet a query source.
