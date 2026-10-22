import { useParams, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";

import {
  getIgnore,
  listIndexes,
  purgeIndex,
  putIgnore,
  setIndexPaused,
  type IgnoreList,
  type IndexInfo,
  type PurgeResult,
} from "../api";
import { usePoll } from "../usePoll";
import { ErrorText } from "../ui/Details";

// Operator controls for one index (USER 2026-10-22, plan/index-controls.md):
// pause, ignore patterns, purge by pattern. A daemon spending model calls on the
// wrong files had no stop short of killing it, which stops every index.
export function IndexControls() {
  const { index } = useParams({ from: "/i/$index" });
  const { data, error, refresh } = usePoll<{ indexes: IndexInfo[] }>(listIndexes, 10000, []);
  const info = data?.indexes.find((i) => i.name === index);

  return (
    <Stack spacing={2} sx={{ maxWidth: 820 }}>
      <Typography variant="h5">Controls</Typography>
      {error && <ErrorText raw={String(error)} />}
      <PauseCard index={index} paused={!!info?.paused} known={!!info} onChange={refresh} />
      <IgnoreCard index={index} />
      <PurgeCard index={index} />
    </Stack>
  );
}

function PauseCard({
  index,
  paused,
  known,
  onChange,
}: {
  index: string;
  paused: boolean;
  known: boolean;
  onChange: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const toggle = async () => {
    setBusy(true);
    setErr("");
    try {
      await setIndexPaused(index, !paused);
      onChange();
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <Stack spacing={1.5}>
        <Typography variant="h6">Pause</Typography>
        <Typography variant="body2" color="text.secondary">
          A paused index runs no work: no imports, captions or embeds. New files still queue, and
          run when you resume. A job already running finishes.
        </Typography>
        {paused && <Alert severity="warning">This index is paused.</Alert>}
        <div>
          <Button
            variant={paused ? "contained" : "outlined"}
            color={paused ? "primary" : "warning"}
            disabled={busy || !known}
            onClick={() => void toggle()}
          >
            {paused ? "Resume" : "Pause"}
          </Button>
        </div>
        {err && <ErrorText raw={err} />}
      </Stack>
    </Paper>
  );
}

const PATTERN_HELP =
  "A glob matched against the full path. **/name/** is a directory anywhere; a glob with no / (*.lock) is a file name.";

// The ignore list. Saved on every change: there is nothing to draft. Adding a
// pattern checks what is already indexed under it and offers to purge that,
// because ignoring stops new files and leaves the old ones searchable.
function IgnoreCard({ index }: { index: string }) {
  const { data, error, refresh } = usePoll<IgnoreList>(() => getIgnore(index), 60000, [index]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [offer, setOffer] = useState<PurgeResult | null>(null);

  const save = async (lists: { patterns?: string[]; allow?: string[] }, added?: string) => {
    setBusy(true);
    setErr("");
    try {
      await putIgnore(index, lists);
      refresh();
      if (added) {
        const pre = await purgeIndex(index, added, true);
        if (pre.documents + pre.ingest_jobs + pre.caption_jobs > 0) setOffer(pre);
      }
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  const own = data?.patterns ?? [];
  const allow = data?.allow ?? [];
  const builtin = data?.builtin ?? [];
  const add = () => {
    const p = draft.trim();
    if (!p) return;
    setDraft("");
    void save({ patterns: own.includes(p) ? own : [...own, p] }, p);
  };
  // A builtin is allowed on this index by putting the builtin glob itself on
  // the allow list. Narrower allow globs (set through MCP or the API) are
  // listed after it so they can be removed.
  const toggleBuiltin = (p: string) =>
    void save({
      allow: allow.includes(p) ? allow.filter((x) => x !== p) : [...allow, p],
    });
  const extraAllow = allow.filter((p) => !builtin.includes(p));

  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <Stack spacing={1.5}>
        <Typography variant="h6">Ignore patterns</Typography>
        <Typography variant="body2" color="text.secondary">
          Files matching these are refused when anything queues them: sync, the watcher, CLI ingest,
          the API and MCP. {PATTERN_HELP}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          Outlined patterns apply to every index. Click one to allow it on this index, for an index
          that is an iOS or Android project.
        </Typography>
        {(error || err) && <ErrorText raw={err || error} />}
        <Box sx={{ display: "flex", flexWrap: "wrap", gap: 1 }}>
          {builtin.map((p) => {
            const allowed = allow.includes(p);
            return (
              <Chip
                key={"b:" + p}
                label={allowed ? `${p} · allowed` : p}
                size="small"
                variant="outlined"
                color={allowed ? "success" : "default"}
                disabled={busy}
                title={
                  allowed
                    ? "Allowed on this index; click to refuse again"
                    : "Refused on every index; click to allow here"
                }
                onClick={() => toggleBuiltin(p)}
                sx={allowed ? { textDecoration: "line-through" } : undefined}
              />
            );
          })}
          {extraAllow.map((p) => (
            <Chip
              key={"a:" + p}
              label={`allow ${p}`}
              size="small"
              variant="outlined"
              color="success"
              disabled={busy}
              onDelete={() => void save({ allow: allow.filter((x) => x !== p) })}
            />
          ))}
          {own.map((p) => (
            <Chip
              key={p}
              label={p}
              size="small"
              disabled={busy}
              onDelete={() => void save({ patterns: own.filter((x) => x !== p) })}
            />
          ))}
        </Box>
        <Stack direction="row" spacing={1}>
          <TextField
            size="small"
            placeholder="**/dist/**"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") add();
            }}
            sx={{ flex: 1 }}
          />
          <Button variant="outlined" disabled={busy || !draft.trim()} onClick={add}>
            Add
          </Button>
        </Stack>
      </Stack>
      {offer && (
        <PurgeDialog
          index={index}
          preview={offer}
          lead={`Ignoring ${offer.pattern} stops new files. These are already in the index:`}
          onClose={() => setOffer(null)}
        />
      )}
    </Paper>
  );
}

// Purge by pattern: preview first, then confirm in a dialog that shows what
// goes. The preview is the only guard; a purge cannot be undone.
function PurgeCard({ index }: { index: string }) {
  const { purge } = useSearch({ from: "/i/$index/controls" });
  const [pattern, setPattern] = useState(purge ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [preview, setPreview] = useState<PurgeResult | null>(null);

  const look = async () => {
    setBusy(true);
    setErr("");
    try {
      setPreview(await purgeIndex(index, pattern.trim(), true));
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <Stack spacing={1.5}>
        <Typography variant="h6">Purge by pattern</Typography>
        <Typography variant="body2" color="text.secondary">
          Remove indexed documents whose path matches, with their pending jobs. Files on disk are
          not touched. {PATTERN_HELP}
        </Typography>
        <Stack direction="row" spacing={1}>
          <TextField
            size="small"
            placeholder="**/node_modules/**"
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && pattern.trim()) void look();
            }}
            sx={{ flex: 1 }}
          />
          <Button variant="outlined" disabled={busy || !pattern.trim()} onClick={() => void look()}>
            Preview
          </Button>
        </Stack>
        {err && <ErrorText raw={err} />}
      </Stack>
      {preview && (
        <PurgeDialog
          index={index}
          preview={preview}
          lead={`Matching ${preview.pattern}:`}
          onClose={() => setPreview(null)}
        />
      )}
    </Paper>
  );
}

function PurgeDialog({
  index,
  preview,
  lead,
  onClose,
}: {
  index: string;
  preview: PurgeResult;
  lead: string;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [done, setDone] = useState<PurgeResult | null>(null);
  const total = preview.documents + preview.ingest_jobs + preview.caption_jobs;

  const purge = async () => {
    setBusy(true);
    setErr("");
    try {
      setDone(await purgeIndex(index, preview.pattern, false));
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open onClose={busy ? undefined : onClose} maxWidth="md" fullWidth>
      <DialogTitle>{done ? "Purged" : "Purge documents?"}</DialogTitle>
      <DialogContent>
        <Stack spacing={1.5}>
          {done ? (
            <Alert severity="success">
              Removed {done.documents} document(s), {done.ingest_jobs} pending import(s) and{" "}
              {done.caption_jobs} pending caption(s).
            </Alert>
          ) : total === 0 ? (
            <Typography>Nothing indexed or queued matches {preview.pattern}.</Typography>
          ) : (
            <>
              <Typography>{lead}</Typography>
              <Typography>
                <b>{preview.documents}</b> document(s), <b>{preview.ingest_jobs}</b> pending
                import(s), <b>{preview.caption_jobs}</b> pending caption(s).
              </Typography>
              {preview.sample.length > 0 && (
                <Box
                  component="ul"
                  sx={{
                    m: 0,
                    pl: 2.5,
                    fontFamily: "monospace",
                    fontSize: 12,
                    wordBreak: "break-all",
                  }}
                >
                  {preview.sample.map((p) => (
                    <li key={p}>{p}</li>
                  ))}
                  {preview.documents > preview.sample.length && (
                    <li>… and {preview.documents - preview.sample.length} more</li>
                  )}
                </Box>
              )}
              <Alert severity="warning">This cannot be undone. Files on disk stay.</Alert>
            </>
          )}
          {err && <ErrorText raw={err} />}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={busy}>
          {done || total === 0 ? "Close" : "Keep them"}
        </Button>
        {!done && total > 0 && (
          <Button color="error" variant="contained" disabled={busy} onClick={() => void purge()}>
            Purge {preview.documents} document(s)
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
}
