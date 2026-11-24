import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Chip from "@mui/material/Chip";
import LinearProgress from "@mui/material/LinearProgress";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";

import { listActivity, postJSON, type ActivityItem } from "../api";
import { Details, ErrorText } from "../ui/Details";
import { fileName, humanizeError } from "../ui/display";
import { askLabel, docParam, docRel, retryJob, stateLabel, useIndexRoot } from "./opsText";
import "./ops.css";

// Activity — what this index is doing, and what it just did.
//
// One ordered feed from /api/activity: ingest_jobs (a document is read) and
// identity_jobs (it is captioned, tagged, fields extracted) merged, newest
// first by enqueued_at. They used to be two panes, which is why an index working
// through fifty captions looked idle on the jobs view — both answers correct,
// neither one the question a person asked.
//
// Each row retries and cancels IN PLACE: the kind decides which endpoints the
// row's job lives on (ingest → /api/jobs/*, identity → /api/identify/queue and
// /api/identity-jobs/cancel).
export function Activity() {
  const { index } = useParams({ from: "/i/$index" });
  const { root, ready } = useIndexRoot(index);
  const [items, setItems] = useState<ActivityItem[]>([]);
  const [kind, setKind] = useState("");
  const [state, setState] = useState("");
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let live = true;
    const load = () =>
      listActivity(index, kind || undefined, state || undefined)
        .then((got) => {
          if (!live) return;
          setItems(got.items ?? []);
          setErr("");
        })
        .catch((e: Error) => live && setErr(e.message))
        .finally(() => live && setLoading(false));
    void load();
    // Work in flight changes on its own, so this polls. 3s matches the rest of
    // the app; live push is an open extension in the plan.
    const t = setInterval(() => void load(), 3000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [index, kind, state, tick]);

  const refresh = () => setTick((n) => n + 1);

  if (loading || !ready) return <LinearProgress />;

  return (
    <Box className="ops">
      <Typography variant="overline" color="text.secondary" component="div" sx={{ mb: 1 }}>
        Activity — ingest, titles, summaries, tags and fields
      </Typography>

      {err && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          Activity could not be loaded. The server may be running an older version.
          <Details>
            <pre>{err}</pre>
            <div>
              Restart the server on the current version: <code>raglit daemon --restart</code>
            </div>
          </Details>
        </Alert>
      )}

      <KindChips kind={kind} onPick={setKind} />
      <StateTabs state={state} onPick={setState} />

      {items.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography color="text.secondary">
            Nothing to show. A document lands here when it is imported or its text changes, so an
            index with no recent imports is expected to be empty.
          </Typography>
        </Paper>
      ) : (
        <Paper variant="outlined">
          {items.map((it, i) => (
            <Row key={it.kind + "-" + it.id} item={it} index={index} root={root} first={i === 0} onDone={refresh} />
          ))}
        </Paper>
      )}
    </Box>
  );
}

// The kind filter. "" is the whole feed; each chip narrows to one queue —
// ingest is a table of its own, caption/tags/extract are the identity table by
// mode. The chips pass through to the server, which filters before its LIMIT.
const KINDS: Array<{ v: string; label: string }> = [
  { v: "", label: "All" },
  { v: "ingest", label: "Ingest" },
  { v: "caption", label: "Captions" },
  { v: "tags", label: "Tags" },
  { v: "extract", label: "Fields" },
];

function KindChips({ kind, onPick }: { kind: string; onPick: (v: string) => void }) {
  return (
    <Stack direction="row" spacing={0.75} useFlexGap sx={{ flexWrap: "wrap", mb: 1 }}>
      {KINDS.map((k) => (
        <Chip
          key={k.v}
          label={k.label}
          size="small"
          clickable
          color={kind === k.v ? "primary" : "default"}
          variant={kind === k.v ? "filled" : "outlined"}
          onClick={() => onPick(k.v)}
        />
      ))}
    </Stack>
  );
}

function StateTabs({ state, onPick }: { state: string; onPick: (v: string) => void }) {
  return (
    <Stack direction="row" spacing={0.75} useFlexGap sx={{ flexWrap: "wrap", mb: 1.5 }}>
      {[
        { v: "", label: "All states" },
        { v: "running", label: "Running" },
        { v: "pending", label: "Waiting" },
        { v: "error", label: "Failed" },
        { v: "done", label: "Done" },
      ].map((s) => (
        <Chip
          key={s.v}
          label={s.label}
          size="small"
          clickable
          variant={state === s.v ? "filled" : "outlined"}
          color={state === s.v ? "primary" : "default"}
          onClick={() => onPick(s.v)}
        />
      ))}
    </Stack>
  );
}

// Identity rows re-queue through /api/identify/queue, which revives a finished,
// skipped or failed row for the same ask (identityqueue.go enqueueIdentity).
// There is no in-place retry on the identity table; this IS the retry.
const requeue = (index: string, item: ActivityItem, root?: string) =>
  postJSON("/api/identify/queue", { index, path: docParam(item.path ?? "", root), mode: item.mode || undefined });

const cancelIdentity = (index: string, id: number) =>
  postJSON("/api/identity-jobs/cancel", { index }, { id });

function Row({
  item,
  index,
  root,
  first,
  onDone,
}: {
  item: ActivityItem;
  index: string;
  root?: string;
  first: boolean;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [actErr, setActErr] = useState("");
  const rel = docRel(item.url ?? item.path ?? "", root);
  const name = fileName(rel) || rel;
  const running = item.state === "running" || item.state === "pending";
  const failed = item.state === "error";

  const act = (fn: () => Promise<unknown>) => async () => {
    setBusy(true);
    setActErr("");
    try {
      await fn();
      onDone();
    } catch (e: unknown) {
      setActErr(humanizeError(String(e)).summary);
    } finally {
      setBusy(false);
    }
  };

  const retry = act(async () => {
    if (item.kind === "ingest") await retryJob(index, item.id);
    else await requeue(index, item, root);
  });
  const cancel = act(async () => {
    if (item.kind === "ingest") await postJSON("/api/jobs/cancel", { index }, { id: item.id });
    else await cancelIdentity(index, item.id);
  });

  return (
    <Box sx={{ p: 1.25, borderTop: first ? 0 : 1, borderColor: "divider" }}>
      <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }}>
        <StateChip state={item.state} />
        <Chip
          label={item.kind === "ingest" ? "Ingest" : askLabel(item.mode) || item.kind}
          variant="outlined"
          sx={{ height: 20, fontSize: 11 }}
        />
        {/* Filename first. The old job table ellipsized the MIDDLE of the path,
            which showed the directory every row shares and hid the one part that
            differs. */}
        <Link className="doc-name" to="/i/$index/d/$doc/pages" params={{ index, doc: docParam(item.url ?? item.path ?? "", root) }}>
          {name}
        </Link>
        <Box sx={{ flex: 1 }} />
        {failed && (
          <button className="act" disabled={busy} onClick={() => void retry()}>
            Retry
          </button>
        )}
        {running && (
          <button className="act" disabled={busy} onClick={() => void cancel()}>
            Cancel
          </button>
        )}
        {took(item) && (
          <Typography variant="caption" color="text.secondary">
            {took(item)}
          </Typography>
        )}
      </Stack>
      {rel !== name && <div className="doc-path">{rel}</div>}
      {item.error && (
        <Box sx={{ mt: 0.5 }}>
          <ErrorText raw={item.error} />
        </Box>
      )}
      {actErr && <ErrorText raw={actErr} />}
    </Box>
  );
}

function StateChip({ state }: { state: string }) {
  const color = ({ done: "var(--ok)", error: "var(--warn)", running: "var(--run)" } as Record<string, string>)[state];
  return (
    <Chip
      label={stateLabel(state)}
      sx={{
        height: 20,
        fontSize: 11,
        fontWeight: 600,
        color: color ?? "text.secondary",
        bgcolor: "var(--chip)",
      }}
    />
  );
}

function took(it: ActivityItem): string {
  if (!it.started_at || !it.finished_at) return "";
  const ms = (it.finished_at - it.started_at) / 1e6;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}
