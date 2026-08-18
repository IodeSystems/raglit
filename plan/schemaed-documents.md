# raglit: schemaed documents — the corpus that knows what it is

Status: BUILT 2026-08-18 (`doctype.go`, `docfields.go`, the index hint in
`indexdigest.go`, `raglit hint` / `raglit doctype` / `raglit fields`, the
`fields` job mode, MCP `get_fields`).
Living doc — prune as the follow-ups land.

## Why

Two absences, and they are the same absence.

A model reading one page is answering a general question about a SPECIFIC
collection, and the collection is the half it cannot see. "RO" on a garage's
paperwork is a repair order, not "received". The second column of a carbon copy
is the customer's, not a duplicate. A survey's marginal figures are bearings,
not measurements. None of that is inferable from the page, all of it is obvious
to whoever owns the corpus, and every prompt that read a document was worse for
not having it.

And identity (see `document-identity.md`) answers "what is this document" in
PROSE, which is right for a letter or a court filing and throws away most of the
value of a form. A corpus usually also holds documents that are the same shape
every time — receipts, work orders, lab reports, bills, evaluations — and a
hundred of those are worth far more as a hundred RECORDS than as a hundred
summaries.

## Delivered

### The index hint

`Store.IndexHint` — prose the corpus owner writes, injected by `HintBlock` into
every model call that reads the corpus: the page transcription (`OCR.Collection`),
the segmentation (`Segmenter.Collection`), the identity and tag asks, the type
proposal, and every extraction. One wording for the block, so the six prompts
that carry it cannot present it differently, and labelled as the owner's words
so a model weighs it as context about the collection rather than as an
instruction from the document.

It is PART OF THE READING RECIPE (`poolRecipe`, via the recipe string in
queuecmd.go). A changed hint changes what a page says, so pooled work read under
the old one must not be replayed under the new — without that, editing the hint
would leave every already-pooled document silently replaying its old reading.
`raglit hint --set` says so in as many words, because the fix costs a re-ingest.

### Document types

`doc_types`, per index: a name, one line on how to RECOGNISE one, an extraction
prompt, and a JSON Schema for the fields. Open-ended and per-index because which
fields a work order has is a property of the corpus — a garage's and a
hospital's share almost nothing.

The prompt travels WITH the schema. A schema alone produces a confidently
filled-in form; the prompt is where "the RO number is top right" and "these
three fields are usually blank" live.

### Authoring from gold documents

`Store.ProposeDocType(ctx, name, goldPaths)` — the step that makes types
practical. A person names the type and points at documents that ARE one; the
model reads them and proposes the description, the extraction prompt and the
schema. Writing a JSON Schema by hand for a form you are looking at is exactly
the work a model does well, and proposing from SEVERAL examples is what stops
the schema being a transcription of one document's quirks.

The proposal is RETURNED, not stored — `raglit doctype propose` prints it and
says it is not registered. A schema nobody read before it started filling in
records is a schema nobody will trust afterwards. `--save` takes it as-is;
`doctype add --file` takes an edited one.

The prompt is opinionated where it has been burned: identifiers and dates are
strings (a work order number is not a number, and losing its leading zero
corrupts it), `required` names only fields present on EVERY example, and
inventing a field the examples do not have is called out.

### Resolution, and the extraction

The identity call picks the type. The registered names become a `doc_type` enum
IN THAT CALL — the schema is not known until the call, so it is built per ask,
for the same reason the tag context is a parameter. Not required, and `""` is an
explicit member: most documents are not forms, and a required enum with no "none"
is an instruction to pick one anyway. An unregistered answer is REFUSED and
re-prompted — a document carrying it would claim a type nothing can extract.

`Identifier.ExtractFields` then turns the type's schema into the tool's
`parameters`, so the SAME fix loop that holds identity to its shape holds an
extraction to the corpus owner's. One guard the schema cannot express: a model
handed a schema sometimes answers WITH the schema, which validates cleanly, so
an echoed `properties` key is caught and re-prompted.

Stored in `doc_fields` (one row per document, replaced) and indexed as ONE
fragment marked `origin='fields'` — flattened to "label: value" lines rather
than the JSON, because a lexical index over `{"po_number":"4471"}` ranks the
punctuation and the word "number" alongside the value somebody is searching for.
Nulls and blanks are NOT indexed: a field the document was silent about is not a
fact.

### One queue, three asks

`identity_jobs.mode` — `identity` | `tags` | `fields`. Replaced the `tags_only`
boolean, because three asks are three states and two booleans are four. The keep
rules differ and getting them the same way round is what makes one queue serve
all three: identity declines an existing caption, tags REQUIRE one, fields
require a resolved type and decline an existing extraction. A person's is never
regenerated in any of the three.

## The trap this walked into, and out of

`indextext.go` already recorded a rule: the predicate for "the document's own
indexed words" is not `origin = ''`, because a photograph's only content is its
`described` fragment. It had become `origin <> 'identity'` in six queries.

`fields` is the same KIND of thing a caption is — a record ABOUT the document —
so `origin <> 'identity'` admitted it as the document's own words. Measured
before the fix: the extraction counted as a fragment, came back as the
document's text from `get_document`, and — worst — `IdentityText` fed it back
into the next captioning call, so identity would have been re-reading its own
extractions.

The predicate is now the constant `SQLOwnWords` / `SQLOwnWordsF`, used by every
query that means it. Adding a generated origin means deciding which list it
joins, once, in one place. `TestFieldsFragment_IsNotTheDocumentsOwnWords` fails
on the old predicate.

### Staleness — an extraction that answers questions nobody is asking

A schema is edited: a field added, a description sharpened, the reading
instructions corrected because the first hundred extractions got a column wrong.
Every extraction already made answers the OLD questions, and NOTHING about the
record says so — it carries the right type name and a plausible set of values,
and the field just added is simply absent, which is indistinguishable from a
document that did not state one.

So `doc_fields.type_hash` records the type definition an extraction was read
UNDER, next to the `text_hash` that records what it was read FROM. `DocType.Hash`
covers the description, the prompt and the schema — the parts that shape an
ANSWER. The name is deliberately out: a rename is a rename, and re-extracting a
corpus because somebody fixed a capital letter is a bill for nothing. The schema
is hashed through a decode/re-encode round trip (Go sorts map keys on marshal),
so a reformatted-but-identical schema hashes the same.

Five ways an extraction stops being current, and they are reported apart because
they need different sentences said to somebody:

| reason | what happened | re-queued? |
|---|---|---|
| `schema` | the type definition changed under it | yes |
| `text` | the document was re-read under it | yes |
| `type differs` | the document now resolves as a different type | yes |
| `type removed` | its type is no longer registered | NO — reported only |
| `none` | never extracted | yes (`DocumentsMissingFields`) |

`type removed` is reported and not queued for the reason `captionableMissing`
already gives about skipped documents: there is nothing to extract against, so a
re-queue is a permanent no-op that would nonetheless read as outstanding work
forever. The record stays — it is what that document said — and the person who
removed the type is the one who can decide.

A PERSON's extraction is never stale. A schema edit does not make what they
wrote wrong, and re-running over it would discard a ruling.

Stale is owed work, so a plain `raglit fields` re-runs it — no `--force`.
`--force` still means what it meant: re-extract everything that resolved as a
type, current or not. And the invalidation is reported at the moment somebody
can act on it, which is the registration itself:

    $ raglit doctype add --file wo.json "work order"
    registered "work order" with 4 field(s)

    2 extraction(s) answer the PREVIOUS schema and are now stale.
    They still read as complete records — the fields you just added are
    absent from them, which looks like a document that did not state one.

`FieldsCoverage.Stale` is its own column for the same reason: "88 of 88
extracted" over a schema edited yesterday is a coverage report that lies.

## Verified

- `doctype_test.go`: names normalise ("Work Order" and "work order" are one
  type); a schema that is not an object schema with properties is refused;
  identity resolves onto a registered name, refuses and re-prompts an
  unregistered one, and is not asked the question at all by an index with no
  types; the hint reaches the identity, tags and extraction asks; the proposal
  reads EVERY gold document, carries the hint, keeps the gold paths, and does
  not register itself.
- `docfields_test.go` (staleness): a schema edit invalidates what was read under
  it, while a reformat and a rename do NOT; a change to the reading instructions
  alone counts; stale is re-run by a plain sweep and current is declined after;
  a person's is never stale and never owed; a removed type is reported and not
  queued; a different resolved type and a moved transcript each get their own
  reason; and the coverage report counts stale apart from extracted.
- `docfields_test.go`: the schema is filled out, the type's own prompt reaches
  the ask, and the result is searchable by a value that appears nowhere else;
  a document that is not one of the types errors rather than guessing; an echoed
  schema is refused; a person's extraction survives a forced re-run; the
  fragment flattens nested values and skips what the document did not state; a
  fields job through the queue extracts against the resolved type and leaves the
  caption alone; and the fragment is not the document's own words.

## Open / not done

- **Extractions are not queryable in aggregate.** They are searchable and
  readable per document; "every work order over $500" needs a field index and a
  query surface, which was deliberately deferred.
- **Schema validation is shallow.** `agent.SchemaValidator` checks required keys
  and top-level types, so a nested array-of-objects is enforced only by the
  prompt. A real JSON-Schema library plugs in at the same seam.
- **The hint is one blob for the whole index.** A corpus with two genuinely
  different collections in it wants two, and the branch machinery is probably
  where that belongs rather than a second field here.
- **Type resolution rides on identity.** A document captioned before its type was
  registered keeps `doc_type=''` until identity is re-run for it, and nothing
  says how many those are. `raglit identify --force` is the manual answer.
