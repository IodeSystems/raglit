import { useParams } from "@tanstack/react-router";
import { useState } from "react";
import Alert from "@mui/material/Alert";
import Button from "@mui/material/Button";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";

import { listIndexes, setIndexPaused, type IndexInfo } from "../api";
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
