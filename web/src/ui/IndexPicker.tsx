import { useState } from "react";
import Button from "@mui/material/Button";
import Checkbox from "@mui/material/Checkbox";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import FormControlLabel from "@mui/material/FormControlLabel";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";

import type { IndexInfo } from "../api";
import { documents, splitName } from "./display";

// Which indexes a search reads. Opened from the gear beside the search box,
// before a search, and from "from N indexes" on the results, after one; both
// change the same `in` list in the search URL, so a search with a chosen set of
// indexes is still a link.
//
// `candidates` is every index the scope could search (a project's indexes, or
// the whole daemon's); `selected` is the set in force. An index that a search
// read and that is not a candidate (a sibling branch the daemon added) is not
// listed: it is not the reader's to switch off here.
export function IndexPicker({
  open,
  candidates,
  selected,
  skipped,
  onApply,
  onClose,
}: {
  open: boolean;
  candidates: IndexInfo[];
  selected: string[];
  // Indexes that sat out a meaning search for want of an embedding model.
  skipped?: string[];
  onApply: (names: string[]) => void;
  onClose: () => void;
}) {
  const [on, setOn] = useState<Set<string>>(new Set(selected));
  // Reset to what is in force each time it opens, so Cancel means cancel.
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setOn(new Set(selected));
  }

  const toggle = (name: string) =>
    setOn((s) => {
      const n = new Set(s);
      if (n.has(name)) n.delete(name);
      else n.add(name);
      return n;
    });
  const all = candidates.length > 0 && candidates.every((c) => on.has(c.name));

  // Grouped by project: a daemon-wide search lists several, and "default" in
  // two projects is two different indexes.
  const groups = new Map<string, IndexInfo[]>();
  for (const c of candidates) {
    const { project } = splitName(c.name);
    groups.set(project, [...(groups.get(project) ?? []), c]);
  }

  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>Indexes to search</DialogTitle>
      <DialogContent dividers>
        <FormControlLabel
          control={
            <Checkbox
              checked={all}
              indeterminate={!all && on.size > 0}
              onChange={() => setOn(all ? new Set() : new Set(candidates.map((c) => c.name)))}
            />
          }
          label="All"
        />
        {[...groups].map(([project, rows]) => (
          <Stack key={project || "(none)"} sx={{ mt: 1 }}>
            {groups.size > 1 && (
              <Typography variant="overline" color="text.secondary">
                {project || "Other indexes"}
              </Typography>
            )}
            {rows.map((c) => (
              <FormControlLabel
                key={c.name}
                control={<Checkbox checked={on.has(c.name)} onChange={() => toggle(c.name)} />}
                label={
                  <span>
                    {splitName(c.name).local}{" "}
                    <Typography component="span" variant="body2" color="text.secondary">
                      · {documents(c.documents ?? 0)}
                      {skipped?.includes(c.name) && " · cannot search by meaning"}
                    </Typography>
                  </span>
                }
              />
            ))}
          </Stack>
        ))}
        {!candidates.length && <Typography color="text.secondary">No indexes to choose from.</Typography>}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          disabled={on.size === 0}
          onClick={() => onApply(candidates.map((c) => c.name).filter((n) => on.has(n)))}
        >
          Apply
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// An index worth offering: not the empty "default" the daemon creates on first
// use whether or not anybody indexes into it. The first page hides it for the
// same reason (Overview.tsx).
export function searchable(i: IndexInfo): boolean {
  return i.name !== "default" || (i.documents ?? 0) > 0;
}

// The `in` URL value for a selection: empty when it is the scope's default set,
// so the ordinary search keeps its short URL.
export function inParam(names: string[], defaults: string[]): string {
  const same = names.length === defaults.length && defaults.every((d) => names.includes(d));
  return same ? "" : names.join(",");
}
