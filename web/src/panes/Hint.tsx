import { useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import LinearProgress from "@mui/material/LinearProgress";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import TextField from "@mui/material/TextField";
import Typography from "@mui/material/Typography";

import { getIndexHint, putIndexHint } from "../api";
import { ErrorText } from "../ui/Details";
import { useResetOn } from "../useResetOn";

// The owner hint, editable from the UI. Until now it existed only at the CLI
// (`raglit hint`), where the consequence of editing it was printed as a note
// after the fact. In the UI the warning sits NEXT to the text, because that is
// where the decision is made.
//
// What the hint does, stated the way the daemon states it: it is prose a person
// writes about the corpus — the conventions, the abbreviations, what a "RO"
// means here — and it is threaded into the page transcription, the
// segmentation, the identity and every extraction. It is part of the reading
// recipe, so documents already read keep the reading they have; re-ingest
// applies a changed hint to them.
export function Hint() {
  const { index } = useParams({ from: "/i/$index" });
  const [stored, setStored] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);

  useResetOn([index], () => {
    setStored(null);
    setDraft("");
    setErr("");
    setSaved(false);
  });

  useEffect(() => {
    let live = true;
    getIndexHint(index)
      .then((r) => {
        if (!live) return;
        const hint = r.hint ?? "";
        setStored(hint);
        setDraft(hint);
        setErr("");
      })
      .catch((e: unknown) => {
        if (!live) return;
        setStored("");
        setErr(String(e));
      });
    return () => {
      live = false;
    };
  }, [index]);

  const loaded = stored !== null;
  const dirty = loaded && draft !== stored;

  const save = async (text: string) => {
    setBusy(true);
    setErr("");
    setSaved(false);
    try {
      const r = await putIndexHint(index, text);
      setStored(r.hint ?? text);
      setDraft(r.hint ?? text);
      setSaved(true);
    } catch (e: unknown) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <LinearProgress />;

  return (
    <Stack spacing={2} sx={{ maxWidth: 760 }}>
      <Box>
        <Typography variant="h5">Hint</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: "60ch" }}>
          What the models should know about this corpus: the conventions, the abbreviations, what
          a code means on the page here. It is threaded into the page transcription, the
          segmentation, the identity and every extraction.
        </Typography>
      </Box>

      {/* The warning the pane exists to carry: visible, before the text, and
          saying what editing does to documents that were already read. */}
      <Alert severity="warning">
        Editing this hint changes how documents in this index are read. The hint is part of the
        reading recipe, so documents already read keep the reading they have — re-ingest a
        document to apply a changed hint to it.
      </Alert>

      {err && <ErrorText raw={err} />}

      <Paper variant="outlined" sx={{ p: 2 }}>
        <Stack spacing={2}>
          <TextField
            label="Hint"
            placeholder="e.g. RO means repair order. A “lot” is a 4-digit batch number stamped on the corner."
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              setSaved(false);
            }}
            multiline
            minRows={4}
            maxRows={24}
            fullWidth
            slotProps={{ input: { "aria-describedby": "hint-warning" } }}
          />
          <Stack direction="row" spacing={1}>
            <Button
              variant="contained"
              disabled={busy || !dirty}
              onClick={() => void save(draft)}
            >
              {stored ? "Save" : "Record"}
            </Button>
            {stored && (
              <Button
                variant="outlined"
                color="warning"
                disabled={busy}
                onClick={() => void save("")}
              >
                Clear hint
              </Button>
            )}
            {saved && (
              <Typography variant="body2" color="text.secondary">
                Saved. It reaches the page transcription, the segmentation, the identity and every
                extraction — and it is part of the reading recipe, so documents already read keep
                the reading they have. Re-ingest to apply it.
              </Typography>
            )}
          </Stack>
        </Stack>
      </Paper>
    </Stack>
  );
}
