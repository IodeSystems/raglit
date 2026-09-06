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

import { listIdentityJobs, type IdentityJob, type IdentityQueueStatus } from "../api";
import { mono } from "../theme";

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
  const [queue, setQueue] = useState<IdentityQueueStatus | null>(null);
  const [jobs, setJobs] = useState<IdentityJob[]>([]);
  const [state, setState] = useState("");
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(true);

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
  }, [index, state]);

  if (loading) return <LinearProgress />;

  return (
    <Box>
      <Typography variant="overline" color="text.secondary" component="div" sx={{ mb: 1 }}>
        Activity — captioning, tags and extraction
      </Typography>

      {err && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          The daemon did not answer <code>/api/identity-jobs</code> ({err}). A daemon older than this
          page does not serve it — <code>raglit daemon --restart</code>.
        </Alert>
      )}

      {queue && <QueueBand queue={queue} />}

      <Tabs
        value={state}
        onChange={(_, v: string) => setState(v)}
        sx={{ minHeight: 36, mb: 1, "& .MuiTab-root": { minHeight: 36, textTransform: "none" } }}
      >
        <Tab value="" label="All" />
        <Tab value="running" label="Running" />
        <Tab value="pending" label="Pending" />
        <Tab value="error" label="Failed" />
        <Tab value="done" label="Done" />
      </Tabs>

      {jobs.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography color="text.secondary">
            Nothing here. Captioning is queued when a document's text is committed, so an index with
            no recent ingest and a full set of captions is correctly empty.
          </Typography>
        </Paper>
      ) : (
        <Paper variant="outlined">
          {jobs.map((j, i) => (
            <JobRow key={j.id} job={j} index={index} first={i === 0} />
          ))}
        </Paper>
      )}
    </Box>
  );
}

// Skipped is deliberately not styled as a failure. It means there was nothing to
// caption — a scanned page carrying forty characters, an empty attachment —
// where nothing went wrong and nothing will go better next time.
function QueueBand({ queue }: { queue: IdentityQueueStatus }) {
  const cells: Array<{ k: string; n: number; role?: "ok" | "warn" | "err" | "run" }> = [
    { k: "running", n: queue.running, role: "run" },
    { k: "pending", n: queue.pending },
    { k: "done", n: queue.done, role: "ok" },
    { k: "skipped", n: queue.skipped },
    { k: "failed", n: queue.failed, role: "err" },
  ];
  return (
    <Stack direction="row" spacing={1} useFlexGap sx={{ mb: 2, flexWrap: "wrap" }}>
      {cells.map((c) => (
        <Paper
          key={c.k}
          variant="outlined"
          sx={{ px: 1.5, py: 1, minWidth: 92,
                // A count of zero is never alarming, whatever it counts.
                borderColor: (t) => (c.role && c.n > 0 ? t.palette.roles[c.role] : t.palette.divider) }}
        >
          <Typography sx={{ fontSize: 20, fontWeight: 650, lineHeight: 1.2,
                            color: (t) => (c.role && c.n > 0 ? t.palette.roles[c.role] : "text.primary") }}>
            {c.n}
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: ".4px" }}>
            {c.k}
          </Typography>
        </Paper>
      ))}
    </Stack>
  );
}

function JobRow({ job, index, first }: { job: IdentityJob; index: string; first: boolean }) {
  const name = job.path.split("/").pop() || job.path;
  return (
    <Box sx={{ p: 1.25, borderTop: first ? 0 : 1, borderColor: "divider" }}>
      <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }}>
        <StateChip state={job.state} />
        {/* The mode IS the ask: identity captions, tags backfills what a document
            is about, fields fills out the schema of its type. Three states, not
            one queue doing one thing. */}
        {job.mode && <Chip label={job.mode} variant="outlined" sx={{ height: 20, fontSize: 11 }} />}
        {/* Filename first. The old job table ellipsized the MIDDLE of the path,
            which showed the directory every row shares and hid the one part that
            differs. */}
        {/* TanStack's Link, not MUI's wrapping it: Box/MuiLink with
            component={Link} loses the typed params and reports `index` as an
            unknown property. Styling one anchor inline is cheaper than losing
            route-param typing across the app. */}
        <Link
          to="/i/$index/d/$doc/pages"
          params={{ index, doc: job.path }}
          style={{ fontWeight: 500, textDecoration: "none", color: "var(--mui-palette-primary-main)" }}
        >
          {name}
        </Link>
        <Box sx={{ flex: 1 }} />
        {took(job) && (
          <Typography variant="caption" color="text.secondary">
            {took(job)}
          </Typography>
        )}
      </Stack>
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ fontFamily: mono, wordBreak: "break-all", mt: 0.25 }}
      >
        {job.path}
      </Typography>
      {job.error && (
        <Typography
          variant="body2"
          component="div"
          sx={{ mt: 0.5, whiteSpace: "pre-wrap", wordBreak: "break-word",
                color: (t) => t.palette.roles.err }}
        >
          {job.error}
        </Typography>
      )}
    </Box>
  );
}

function StateChip({ state }: { state: string }) {
  const role = ({ done: "ok", error: "err", running: "run" } as const)[state as "done" | "error" | "running"];
  return (
    <Chip
      label={state}
      sx={{
        height: 20,
        fontSize: 11,
        fontWeight: 600,
        color: (t) => (role ? t.palette.roles[role] : t.palette.text.secondary),
        bgcolor: (t) => t.palette.roles.chip,
      }}
    />
  );
}

function took(j: IdentityJob): string {
  if (!j.started_at || !j.finished_at) return "";
  const ms = (j.finished_at - j.started_at) / 1e6;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}
