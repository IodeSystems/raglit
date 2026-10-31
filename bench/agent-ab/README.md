# agent A/B: what raglit saves an agent

The same questions, answered by Claude Code (`claude -p`) twice:

- **grep** arm: built-in `Read`, `Grep`, `Glob` over the files; no MCP.
- **raglit** arm: raglit's MCP tools only (`search`, `get_document`,
  `list_documents`, `list_indexes`); no file tools.

Per run, `run.py` records input tokens (uncached + cache write + cache read),
output tokens, turns, tool calls, characters of tool results, cost and wall
time, and scores the answer against regexes in `questions.json`. A wrong answer
is not a saving, so read the `correct` column first.

    bench/agent-ab/run.py --out /tmp/ab --llm-url URL --reps 3 [--model haiku]

`--llm-url` is the OpenAI-compatible endpoint for the raglit arm's embedder
(`--embed-model`, default `nomic-embed-text`). Everything runs under `--out`: a
copy of the corpus plus filler, a fresh raglit home, and a throwaway daemon on
its own root and port (`bench/raglitd.py`), so the user's daemon and indexes
are never touched.

## The corpus

`corpus/` is fictional (Thornbury Instruments): HR policy, engineering docs and
three mail archives. Questions come in three sets (`--set`):

- **lookup**: the question carries a rare word the answer line also has
  (Denver, 401(k), v2). grep's home ground.
- **contextual**: how-to and which-document questions asked the way a newcomer
  would, sharing no rare word with the document that answers ("How do I file a
  travel expense?", "I left my work computer in a taxi"). The filler mentions
  expenses, laptops, access and incidents constantly.
- **surfacing**: a task ("I'm flying to Chicago next month...") where a good
  answer brings up several documents the asker did not know to name. Scored
  k of n facts (`of`, `k`); the per-question table shows the mean facts found.

Lookup questions by wart:

| wart | question ids |
|---|---|
| near-duplicates that disagree (old version, unadopted draft) | leave-current, api-limit |
| answer split across two documents | denver-lodging, expense-approver, dep-pager |
| answer only in an email thread, after a reversed proposal | cert-owner, stipend-date |
| answer only in an email attachment (base64) | oncall-attachment |
| superseded decision record | sessions-now |
| question words differ from document words | leave-current, outage-cause |
| not in the corpus | no-sabbatical |

`gen-distractors.py` adds filler (default 300 documents, fixed seed): status
notes, meetings and retros that use the questions' words without answering
them. Without filler, reading everything is cheap and grep has nothing to
wade through.

## Limits

- One model per run; behaviour differs by model. Pass `--model`.
- The scorer is regex. Check `results.jsonl` answers before quoting a number.
- Input tokens are dominated by the fixed prompt re-sent on every model call,
  so turns drive cost more than tool-result size. The summary prints each arm's
  fixed overhead (a no-tool probe) for that reason.
