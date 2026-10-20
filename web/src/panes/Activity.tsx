import { Link, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Chip from "@mui/material/Chip";
import LinearProgress from "@mui/material/LinearProgress";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Tab from "@mui/material/Tab";
import Tabs from "@mui/material/Tabs";
import Typography from "@mui/material/Typography";

import { listIdentityJobs, postJSON, type IdentityJob, type IdentityQueueStatus } from "../api";
import { Details, ErrorText } from "../ui/Details";
import { fileName, humanizeError } from "../ui/display";
import { askLabel, docParam, docRel, retryEach, stateLabel, useIndexRoot } from "./opsText";
import "./ops.css";

// Activity — what this index is doing, and what it just did.
//
// This exists because raglit has TWO queues and the UI showed one. `ingest_jobs`
// reads a document; `identity_jobs` captions it, tags it, and fills out the
// schema of whatever type it resolved as. They are different tables, so an index
// could be working through fifty captions while the Ingest jobs view showed
// nothing outstanding — both answers correct, neither one the question a person
// asked.
//
// This pane is the identity half, which had no surface at all. Merging the two
// feeds into one ordered timeline is the next step and wants a server-side
// change; see plan/ui-redesign.md §6.
export function Activity() {
  const { index } = useParams({ from: "/i/$index" });
  const { root, ready } = useIndexRoot(index);
  const [queue, setQueue] = useState<IdentityQueueStatus | null>(null);
  const [jobs, setJobs] = useState<IdentityJob[]>([]);
  const [state, setState] = useState("");
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let live = true;
    const load = () =>
      listIdentityJobs(index, state || undefined)
        .then((got) => {
          if (!live) return;
          setQueue(got.queue);
          setJobs(got.jobs ?? []);
          setErr("");
        })
        .catch((e: Error) => live && setErr(e.message))
        .finally(() => live && setLoading(false));
    load();
    // Work in flight changes on its own, so this polls. 3s matches the rest of
    // the app; live push is an open extension in the plan.
    const t = setInterval(load, 3000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [index, state, tick]);

  const refresh = () => setTick((n) => n + 1);

  if (loading || !ready) return <LinearProgress />;

  return (
    <Box className="ops">
      <Typography variant="overline" color="text.secondary" component="div" sx={{ mb: 1 }}>
        Activity — titles, summaries, tags and fields
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

      {queue && <QueueBand queue={queue} index={index} root={root} onDone={refresh} />}

      <Tabs
        value={state}
        onChange={(_, v: string) => setState(v)}
        sx={{ minHeight: 36, mb: 1, "& .MuiTab-root": { minHeight: 36, textTransform: "none" } }}
      >
        <Tab value="" label="All" />
        <Tab value="running" label="Running" />
        <Tab value="pending" label="Waiting" />
        <Tab value="error" label="Failed" />
        <Tab value="done" label="Done" />
      </Tabs>

      {jobs.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography color="text.secondary">
            Nothing to show. A document is queued here when it is imported or its text changes, so an
            index with no recent imports is expected to be empty.
          </Typography>
        </Paper>
      ) : (
        <Paper variant="outlined">
          {jobs.map((j, i) => (
            <JobRow key={j.id} job={j} index={index} root={root} first={i === 0} onDone={refresh} />
          ))}
        </Paper>
      )}
    </Box>
  );
}

// Re-queue one document for the same ask it failed. /api/identify/queue revives
// a finished, skipped or failed row (identityqueue.go enqueueIdentity).
const requeue = (index: string, job: IdentityJob, root?: string) =>
  postJSON("/api/identify/queue", { index, path: docParam(job.path, root), mode: job.mode || undefined });

// Skipped is deliberately not styled as a failure. It means there was nothing to
// caption — a scanned page carrying forty characters, an empty attachment —
// where nothing went wrong and nothing will go better next time. Failed is
// amber, not red, and sits next to the action that fixes it.
function QueueBand({
  queue,
  index,
  root,
  onDone,
}: {
  queue: IdentityQueueStatus;
  index: string;
  root?: string;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const cells: Array<{ k: string; n: number; color?: string }> = [
    { k: "Running", n: queue.running, color: "var(--run)" },
    { k: "Waiting", n: queue.pending },
    { k: "Done", n: queue.done, color: "var(--ok)" },
    { k: "Skipped", n: queue.skipped },
    { k: "Failed", n: queue.failed, color: "var(--warn)" },
  ];

  // Every failed row, not only the page shown: the list is capped, the count is not.
  const retryAll = async () => {
    setBusy(true);
    setNote("");
    try {
      const got = await listIdentityJobs(index, "error", 5000);
      const failed = await retryEach(got.jobs ?? [], (j) => requeue(index, j, root));
      setNote(failed ? `${failed.toLocaleString()} could not be queued again.` : "Queued again.");
      onDone();
    } catch (e: unknown) {
      setNote(humanizeError(String(e)).summary);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box sx={{ mb: 2 }}>
      <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap", alignItems: "stretch" }}>
        {cells.map((c) => {
          // A count of zero is never alarming, whatever it counts.
          const lit = !!c.color && c.n > 0;
          return (
            <Paper key={c.k} variant="outlined" sx={{ px: 1.5, py: 1, minWidth: 92, borderColor: lit ? c.color : "divider" }}>
              <Typography sx={{ fontSize: 20, fontWeight: 650, lineHeight: 1.2, color: lit ? c.color : "text.primary" }}>
                {c.n.toLocaleString()}
              </Typography>
              <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: ".4px" }}>
                {c.k}
              </Typography>
            </Paper>
          );
        })}
      </Stack>
      {queue.failed > 0 && (
        <div className="ops-head" style={{ marginTop: 8, marginBottom: 0 }}>
          <button className="act primary" disabled={busy} onClick={retryAll}>
            {busy ? "Queuing…" : `Retry ${queue.failed.toLocaleString()} failed`}
          </button>
          {note && <span className="ops-note">{note}</span>}
        </div>
      )}
    </Box>
  );
}

function JobRow({
  job,
  index,
  root,
  first,
  onDone,
}: {
  job: IdentityJob;
  index: string;
  root?: string;
  first: boolean;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [actErr, setActErr] = useState("");
  const rel = docRel(job.path, root);
  const name = fileName(rel);
  const retry = async () => {
    setBusy(true);
    setActErr("");
    try {
      await requeue(index, job, root);
      onDone();
    } catch (e: unknown) {
      setActErr(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Box sx={{ p: 1.25, borderTop: first ? 0 : 1, borderColor: "divider" }}>
      <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }}>
        <StateChip state={job.state} />
        {/* The mode IS the ask: identity captions, tags backfills what a document
            is about, fields fills out the schema of its type. */}
        {job.mode && <Chip label={askLabel(job.mode)} variant="outlined" sx={{ height: 20, fontSize: 11 }} />}
        {/* Filename first. The old job table ellipsized the MIDDLE of the path,
            which showed the directory every row shares and hid the one part that
            differs. */}
        {/* TanStack's Link, not MUI's wrapping it: Box/MuiLink with
            component={Link} loses the typed params and reports `index` as an
            unknown property. */}
        <Link className="doc-name" to="/i/$index/d/$doc/pages" params={{ index, doc: docParam(job.path, root) }}>
          {name}
        </Link>
        <Box sx={{ flex: 1 }} />
        {job.state === "error" && (
          <button className="act" disabled={busy} onClick={retry}>
            Retry
          </button>
        )}
        {took(job) && (
          <Typography variant="caption" color="text.secondary">
            {took(job)}
          </Typography>
        )}
      </Stack>
      {rel !== name && <div className="doc-path">{rel}</div>}
      {job.error && (
        <Box sx={{ mt: 0.5 }}>
          <ErrorText raw={job.error} />
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

function took(j: IdentityJob): string {
  if (!j.started_at || !j.finished_at) return "";
  const ms = (j.finished_at - j.started_at) / 1e6;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}
