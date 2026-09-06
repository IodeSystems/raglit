import { useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Chip from "@mui/material/Chip";
import LinearProgress from "@mui/material/LinearProgress";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";

import { listDocTypes, type DocTypeInfo, type FieldsCoverage } from "../api";
import { mono } from "../theme";

// Types & fields — the schemaed-documents feature, which until now existed only
// at the CLI.
//
// A corpus usually holds documents that are the same shape every time: receipts,
// work orders, lab reports, inspection forms. A hundred of those are worth far
// more as a hundred RECORDS than as a hundred summaries, and this is where a
// person sees whether that is actually happening.
export function Types() {
  const { index } = useParams({ from: "/i/$index" });
  const [types, setTypes] = useState<DocTypeInfo[]>([]);
  const [cov, setCov] = useState<FieldsCoverage[]>([]);
  const [err, setErr] = useState<string>("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    listDocTypes(index)
      .then((got) => {
        setTypes(got.types ?? []);
        setCov(got.coverage ?? []);
        setErr("");
      })
      // A daemon older than this bundle answers 404 here. Say that, rather than
      // rendering an index with no types as if it had none registered — those
      // are different facts and only one of them is about the corpus.
      .catch((e: Error) => setErr(e.message))
      .finally(() => setLoading(false));
  }, [index]);

  const byType = new Map(cov.map((c) => [c.type, c]));

  if (loading) return <LinearProgress />;

  return (
    <Box>
      <Typography variant="overline" color="text.secondary" component="div" sx={{ mb: 1 }}>
        Types &amp; fields
      </Typography>

      {err && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          The daemon did not answer <code>/api/doc-types</code> ({err}). If it is running an older
          build than this page, restart it on the current binary — <code>raglit daemon --restart</code>.
        </Alert>
      )}

      {!err && types.length === 0 && (
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography sx={{ mb: 1 }}>No document types are registered for this index.</Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
            A type is authored from documents that ARE one: raglit reads the examples and proposes
            the fields and the reading instructions, and a person reviews them before anything is
            registered. Which fields a form has is a property of this corpus, not of raglit.
          </Typography>
          <Box component="pre" sx={{ fontFamily: mono, fontSize: 13, m: 0, p: 1.5, borderRadius: 1,
                                     bgcolor: (t) => t.palette.roles.chip, overflowX: "auto" }}>
{`raglit doctype propose "lab report" DOC1 DOC2 DOC3
raglit doctype add --file lab.json "lab report"`}
          </Box>
        </Paper>
      )}

      <Stack spacing={1.5}>
        {types.map((t) => (
          <TypeCard key={t.name} type={t} coverage={byType.get(t.name)} />
        ))}
      </Stack>
    </Box>
  );
}

function TypeCard({ type, coverage }: { type: DocTypeInfo; coverage?: FieldsCoverage }) {
  const fields = fieldNames(type.schema);
  const resolved = coverage?.resolved ?? 0;
  const extracted = coverage?.extracted ?? 0;
  const stale = coverage?.stale ?? 0;

  return (
    <Paper variant="outlined" sx={{ p: 1.5 }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: "baseline", flexWrap: "wrap" }}>
        <Typography sx={{ fontWeight: 600, fontSize: 15 }}>{type.name}</Typography>
        <Box sx={{ flex: 1 }} />
        <Typography variant="body2" color="text.secondary">
          {extracted} of {resolved} extracted
        </Typography>
        {/* Stale is its OWN number, never folded into extracted. An extraction
            that answers a previous schema still reads as a complete record — the
            field somebody just added is simply absent from it, which looks
            exactly like a document that did not state one. */}
        {stale > 0 && (
          <Chip
            label={`${stale} stale`}
            sx={{ bgcolor: (t) => t.palette.roles.warn, color: "#fff", height: 20 }}
          />
        )}
      </Stack>

      {type.description && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          {type.description}
        </Typography>
      )}

      <Coverage resolved={resolved} extracted={extracted} stale={stale} />

      {fields.length > 0 && (
        <Stack direction="row" spacing={0.5} useFlexGap sx={{ mt: 1, flexWrap: "wrap" }}>
          {fields.map((f) => (
            <Chip key={f} label={f} variant="outlined" sx={{ fontFamily: mono, fontSize: 12 }} />
          ))}
        </Stack>
      )}

      {type.prompt && (
        <Box sx={{ mt: 1.5 }}>
          <Typography variant="overline" color="text.secondary" component="div">
            How to read one
          </Typography>
          <Typography variant="body2" sx={{ whiteSpace: "pre-wrap", maxWidth: "78ch" }}>
            {type.prompt}
          </Typography>
        </Box>
      )}

      {(type.gold?.length ?? 0) > 0 && (
        <Typography variant="caption" color="text.secondary" component="div" sx={{ mt: 1 }}>
          proposed from {type.gold!.length} example{type.gold!.length === 1 ? "" : "s"}
          {type.model ? ` by ${type.model}` : ""}
        </Typography>
      )}
    </Paper>
  );
}

// The bar reads left to right as: current, stale, not yet done. Stale is shown
// INSIDE the extracted portion because those documents do have a record — it is
// just answering an older question.
function Coverage({ resolved, extracted, stale }: { resolved: number; extracted: number; stale: number }) {
  if (resolved === 0) {
    return (
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
        No document has resolved as this type yet. Resolution happens during
        captioning — a document captioned before the type was registered keeps no
        type until identity runs for it again.
      </Typography>
    );
  }
  const pct = (n: number) => `${(100 * n) / resolved}%`;
  const current = Math.max(0, extracted - stale);
  return (
    <Box sx={{ display: "flex", height: 6, borderRadius: 3, overflow: "hidden", mt: 1.25,
               bgcolor: (t) => t.palette.roles.chip }}>
      <Box sx={{ width: pct(current), bgcolor: (t) => t.palette.roles.ok }} />
      <Box sx={{ width: pct(stale), bgcolor: (t) => t.palette.roles.warn }} />
    </Box>
  );
}

// The schema is a JSON Schema object; its `properties` keys are the field names.
// Read defensively — this is authored content and a malformed schema should
// render a type with no field chips, not a blank page.
function fieldNames(schema: unknown): string[] {
  if (!schema || typeof schema !== "object") return [];
  const props = (schema as { properties?: unknown }).properties;
  if (!props || typeof props !== "object") return [];
  return Object.keys(props as Record<string, unknown>);
}
