import { useCallback, useEffect, useMemo, useState } from "react";
import Alert from "@mui/material/Alert";
import Autocomplete from "@mui/material/Autocomplete";
import Button from "@mui/material/Button";
import Checkbox from "@mui/material/Checkbox";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import FormControlLabel from "@mui/material/FormControlLabel";
import MenuItem from "@mui/material/MenuItem";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import TextField from "@mui/material/TextField";

import {
  ApiError,
  getModelSettings,
  listProviderModels,
  reembedImpact,
  saveModelSettings,
  type ModelRole,
  type ModelSettings as Settings,
  type ProviderModels,
  type ProviderPreset,
  type RoleView,
} from "../api";
import { AppShell, CrumbLink } from "../shell/AppShell";
import { settingsSections } from "../shell/NavDrawer";
import { ErrorText } from "../ui/Details";
import "./scope.css";

// Model settings: providers first, then a provider and a model for each role
// (USER 2026-10-22, plan/model-settings.md; corrallm's add-model flow is the
// pattern). Every change here is a draft until Save, which writes config.json
// and has the daemon reload once no job is running.
//
// Vision and audio may be None: a vision model is often what a site does not
// have. With none, scans and images are read by tesseract when the daemon has
// it, which the vision card says.

type Draft = {
  name: string;
  kind: string;
  base_url: string;
  // A key typed in this session. Never the stored one: the server does not
  // send keys back.
  api_key: string;
  has_key: boolean;
  clear_key: boolean;
  // On the server as loaded, and changed since. A saved, unchanged provider is
  // listed by name so its stored key is used.
  saved: boolean;
  dirty: boolean;
};

const ROLES: { role: ModelRole; title: string; what: string; empty: string }[] = [
  { role: "vision", title: "Vision", what: "Reads scans, photos and page images (OCR).", empty: "None" },
  { role: "embed", title: "Embedding", what: "Turns text into vectors for semantic search.", empty: "None (keyword search only)" },
  {
    role: "segment",
    title: "Text",
    what: "Splits transcribed pages into passages.",
    empty: "Same as vision",
  },
  {
    role: "identity",
    title: "Summary",
    what: "Writes each document's caption, summary and kind.",
    empty: "Same as text",
  },
  { role: "audio", title: "Audio", what: "Transcribes recordings.", empty: "None" },
];

// Which listed models suit a role, when the provider says what its models
// accept. Text work is anything that is not an embedder or a transcriber.
function suits(role: ModelRole, m: { vision: boolean; embed: boolean; audio: boolean }): boolean {
  switch (role) {
    case "vision":
      return m.vision;
    case "embed":
      return m.embed;
    case "audio":
      return m.audio;
    case "segment":
    case "identity":
      return !m.embed && !m.audio;
  }
}

export function ModelSettings() {
  const [loaded, setLoaded] = useState<Settings | null>(null);
  const [loadErr, setLoadErr] = useState<{ msg: string; status?: number } | null>(null);
  const [providers, setProviders] = useState<Draft[]>([]);
  const [roles, setRoles] = useState<Record<ModelRole, RoleView> | null>(null);
  const [editing, setEditing] = useState<{ draft: Draft; index: number } | null>(null);
  const [lists, setLists] = useState<Record<string, ProviderModels | "loading">>({});
  const [busy, setBusy] = useState(false);
  const [saveErr, setSaveErr] = useState("");
  const [saved, setSaved] = useState(false);
  const [confirm, setConfirm] = useState<{ indexes: number; fragments: number } | null>(null);

  // One listing per provider, started when a role needs it: on load, when a
  // role picks a provider, and after the provider is edited.
  const fetchList = useCallback((p: Draft) => {
    setLists((l) => ({ ...l, [p.name]: "loading" }));
    const q =
      p.saved && !p.dirty
        ? { provider: p.name }
        : { provider: p.saved ? p.name : undefined, base_url: p.base_url, api_key: p.api_key || undefined };
    listProviderModels(q)
      .then((r) => setLists((l) => ({ ...l, [p.name]: r })))
      .catch((e: unknown) => setLists((l) => ({ ...l, [p.name]: { capabilities: false, error: String(e) } })));
  }, []);

  const load = useCallback(() => {
    getModelSettings()
      .then((s) => {
        const drafts = (s.providers ?? []).map((p) => ({ ...p, api_key: "", clear_key: false, saved: true, dirty: false }));
        setLoaded(s);
        setProviders(drafts);
        setRoles(s.roles);
        setLists({});
        const wanted = new Set(Object.values(s.roles).map((r) => r.provider));
        for (const d of drafts) if (wanted.has(d.name)) fetchList(d);
      })
      .catch((e: unknown) =>
        setLoadErr({ msg: String(e), status: e instanceof ApiError ? e.status : undefined }),
      );
  }, [fetchList]);
  useEffect(load, [load]);

  const byName = useMemo(() => new Map(providers.map((p) => [p.name, p])), [providers]);

  if (loadErr) {
    return (
      <Frame>
        <div className="empty">
          {loadErr.status === 403 ? "Only the admin can change model settings." : <ErrorText raw={loadErr.msg} />}
        </div>
      </Frame>
    );
  }
  if (!loaded || !roles) {
    return (
      <Frame>
        <div className="empty">Loading…</div>
      </Frame>
    );
  }

  const used = (name: string) => Object.values(roles).some((r) => r.provider === name);
  const setRole = (role: ModelRole, v: Partial<RoleView>) => {
    setSaved(false);
    setRoles({ ...roles, [role]: { ...roles[role], ...v } });
    const p = v.provider ? byName.get(v.provider) : undefined;
    if (p && !lists[p.name]) fetchList(p);
  };

  const putProvider = (d: Draft, index: number) => {
    setSaved(false);
    const next = [...providers];
    const old = index >= 0 ? next[index] : undefined;
    if (index >= 0) next[index] = d;
    else next.push(d);
    setProviders(next);
    // A renamed provider takes its roles with it; an edited one is listed again.
    if (old && old.name !== d.name) {
      const moved = { ...roles };
      for (const k of Object.keys(moved) as ModelRole[]) {
        if (moved[k].provider === old.name) moved[k] = { ...moved[k], provider: d.name };
      }
      setRoles(moved);
    }
    setLists((l) => {
      const rest = { ...l };
      delete rest[d.name];
      if (old) delete rest[old.name];
      return rest;
    });
    if (old && used(old.name)) fetchList(d);
    setEditing(null);
  };

  const save = async (confirmed: boolean) => {
    setSaveErr("");
    const embed = roles.embed.model.trim();
    if (!confirmed && embed && embed !== loaded.roles.embed.model) {
      try {
        const impact = await reembedImpact(embed);
        if (impact.fragments > 0) {
          setConfirm(impact);
          return;
        }
      } catch {
        // No count is no reason to block a save; the daemon re-embeds anyway.
      }
    }
    setConfirm(null);
    setBusy(true);
    try {
      await saveModelSettings({
        providers: providers.map((p) => ({
          name: p.name,
          kind: p.kind,
          base_url: p.base_url,
          api_key: p.api_key || undefined,
          clear_key: p.clear_key || undefined,
        })),
        roles,
      });
      setSaved(true);
      load();
    } catch (e: unknown) {
      setSaveErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Frame>
      <section className="panel" style={{ padding: "var(--sp-4)", marginBottom: "var(--sp-4)" }}>
        <h3 className="scopesec" style={{ marginTop: 0 }}>
          Providers
        </h3>
        <p className="muted" style={{ marginTop: 0 }}>
          An OpenAI-compatible endpoint each role can use. Tokens are stored on the server and never shown again.
        </p>
        {!providers.length && <div className="empty">No providers yet.</div>}
        <Stack spacing={1}>
          {providers.map((p, i) => (
            <Paper key={p.name} variant="outlined" sx={{ p: 1.5, display: "flex", gap: 2, alignItems: "center", flexWrap: "wrap" }}>
              <div style={{ flex: 1, minWidth: 200 }}>
                <strong>{p.name}</strong>{" "}
                <span className="muted small">{presetLabel(loaded.presets, p.kind)}</span>
                <div className="muted small" style={{ overflowWrap: "anywhere" }}>
                  {p.base_url} · {p.api_key || (p.has_key && !p.clear_key) ? "token set" : "no token"}
                  {p.dirty && " · unsaved"}
                </div>
              </div>
              <Button size="small" onClick={() => setEditing({ draft: p, index: i })}>
                Edit
              </Button>
              <Button
                size="small"
                color="error"
                disabled={used(p.name)}
                title={used(p.name) ? "A role uses this provider" : undefined}
                onClick={() => {
                  setSaved(false);
                  setProviders(providers.filter((_, j) => j !== i));
                }}
              >
                Remove
              </Button>
            </Paper>
          ))}
        </Stack>
        <Button
          sx={{ mt: 1.5 }}
          variant="outlined"
          onClick={() =>
            setEditing({
              draft: { name: "", kind: "custom", base_url: "", api_key: "", has_key: false, clear_key: false, saved: false, dirty: true },
              index: -1,
            })
          }
        >
          Add provider
        </Button>
      </section>

      <div className="scopegrid">
        {ROLES.map(({ role, title, what, empty }) => (
          <RoleCard
            key={role}
            role={role}
            title={title}
            what={what}
            // "Same as vision" means nothing once vision is None.
            empty={role === "segment" && !roles.vision.model.trim() ? "None" : empty}
            value={roles[role]}
            providers={providers}
            list={roles[role].provider ? lists[roles[role].provider] : undefined}
            onChange={(v) => setRole(role, v)}
            note={
              role === "vision" && !roles.vision.model.trim()
                ? loaded.tesseract
                  ? "Scans and images are read by tesseract. Layout, tables and handwriting come out worse than with a vision model."
                  : "Scans and images will be listed as unreadable: the daemon has no vision model and no tesseract."
                : undefined
            }
          />
        ))}
      </div>

      <Stack direction="row" spacing={2} sx={{ mt: 3, alignItems: "center", flexWrap: "wrap" }}>
        <Button variant="contained" disabled={busy} onClick={() => void save(false)}>
          {busy ? "Saving…" : "Save"}
        </Button>
        {saved && (
          <Alert severity="success" sx={{ py: 0 }}>
            Saved. The daemon applies it as soon as no job is running.
          </Alert>
        )}
      </Stack>
      {saveErr && <ErrorText raw={saveErr} />}

      {editing && (
        <ProviderDialog
          initial={editing.draft}
          presets={loaded.presets}
          taken={providers.filter((_, j) => j !== editing.index).map((p) => p.name)}
          onClose={() => setEditing(null)}
          onDone={(d) => putProvider(d, editing.index)}
        />
      )}

      <Dialog open={!!confirm} onClose={() => setConfirm(null)}>
        <DialogTitle>Re-embed with {roles.embed.model}?</DialogTitle>
        <DialogContent>
          Vectors from one embedding model cannot be searched with another, so{" "}
          {confirm?.fragments.toLocaleString()} passages in {confirm?.indexes.toLocaleString()}{" "}
          {confirm?.indexes === 1 ? "index" : "indexes"} will be embedded again. Keyword search keeps working while
          it runs; semantic search fills back in as it goes.
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirm(null)}>Cancel</Button>
          <Button variant="contained" onClick={() => void save(true)}>
            Save and re-embed
          </Button>
        </DialogActions>
      </Dialog>
    </Frame>
  );
}

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <AppShell crumbs={[<CrumbLink key="projects" to="/">Projects</CrumbLink>, "Settings", "Models"]} nav={settingsSections()}>
      <div className="scopehead">
        <h1>Models</h1>
        <div className="facts">Pick a provider, then a model, for each job the daemon does.</div>
      </div>
      {children}
    </AppShell>
  );
}

function presetLabel(presets: ProviderPreset[], kind: string): string {
  return presets.find((p) => p.kind === kind)?.label ?? kind;
}

function RoleCard({
  role,
  title,
  what,
  empty,
  value,
  providers,
  list,
  onChange,
  note,
}: {
  role: ModelRole;
  title: string;
  what: string;
  empty: string;
  value: RoleView;
  providers: Draft[];
  list?: ProviderModels | "loading";
  onChange: (v: Partial<RoleView>) => void;
  note?: string;
}) {
  const listed = list && list !== "loading" ? (list.models ?? []) : [];
  const fitting = listed.filter((m) => suits(role, m));
  const filtered = list && list !== "loading" && list.capabilities && fitting.length > 0;
  const options = (filtered ? fitting : listed).map((m) => m.id);

  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <h3 style={{ margin: 0 }}>{title}</h3>
      <p className="muted small" style={{ margin: "4px 0 12px" }}>
        {what}
      </p>
      <Stack spacing={1.5}>
        <TextField
          select
          size="small"
          label="Provider"
          value={value.provider}
          slotProps={{ inputLabel: { shrink: true }, select: { displayEmpty: true } }}
          onChange={(e) => onChange({ provider: e.target.value, model: e.target.value ? value.model : "" })}
        >
          <MenuItem value="">
            <em>{empty}</em>
          </MenuItem>
          {providers.map((p) => (
            <MenuItem key={p.name} value={p.name}>
              {p.name}
            </MenuItem>
          ))}
        </TextField>
        {value.provider && (
          <Autocomplete
            freeSolo
            size="small"
            options={options}
            value={value.model}
            loading={list === "loading"}
            onInputChange={(_, v) => onChange({ model: v })}
            renderInput={(params) => (
              <TextField
                {...params}
                label="Model"
                helperText={
                  list === "loading"
                    ? "Listing models…"
                    : list?.error
                      ? `Could not list models (${list.error}). Type the model id.`
                      : list && !filtered && listed.length
                        ? "This provider does not say what its models accept; all are shown."
                        : undefined
                }
              />
            )}
          />
        )}
        {note && <Alert severity="info">{note}</Alert>}
      </Stack>
    </Paper>
  );
}

function ProviderDialog({
  initial,
  presets,
  taken,
  onClose,
  onDone,
}: {
  initial: Draft;
  presets: ProviderPreset[];
  taken: string[];
  onClose: () => void;
  onDone: (d: Draft) => void;
}) {
  const [d, setD] = useState<Draft>(initial);
  const [check, setCheck] = useState<ProviderModels | "loading" | null>(null);
  const preset = presets.find((p) => p.kind === d.kind);
  const set = (v: Partial<Draft>) => {
    setCheck(null);
    setD({ ...d, ...v, dirty: true });
  };
  const nameErr = !/^[a-z0-9][a-z0-9-]{0,39}$/.test(d.name)
    ? "Lowercase letters, digits and dashes"
    : taken.includes(d.name)
      ? "Another provider has this name"
      : "";
  const urlErr = /^https?:\/\/[^/]+/.test(d.base_url) ? "" : "http(s)://host…";
  const keyMissing = preset?.key_hint === "required" && !d.api_key && !(d.has_key && !d.clear_key);

  const runCheck = () => {
    setCheck("loading");
    listProviderModels({ provider: d.saved ? initial.name : undefined, base_url: d.base_url, api_key: d.api_key || undefined })
      .then(setCheck)
      .catch((e: unknown) => setCheck({ capabilities: false, error: String(e) }));
  };

  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>{initial.saved ? `Edit ${initial.name}` : "Add provider"}</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ mt: 1 }}>
          <TextField
            select
            label="Type"
            value={d.kind}
            onChange={(e) => {
              const p = presets.find((x) => x.kind === e.target.value);
              set({
                kind: e.target.value,
                base_url: p?.base_url || d.base_url,
                name: d.name || (p && p.kind !== "custom" ? p.kind : d.name),
              });
            }}
          >
            {presets.map((p) => (
              <MenuItem key={p.kind} value={p.kind}>
                {p.label}
              </MenuItem>
            ))}
          </TextField>
          <TextField
            label="Name"
            value={d.name}
            onChange={(e) => set({ name: e.target.value.trim() })}
            error={!!d.name && !!nameErr}
            helperText={(d.name && nameErr) || "How roles refer to it"}
          />
          <TextField
            label="Base URL"
            value={d.base_url}
            onChange={(e) => set({ base_url: e.target.value.trim() })}
            error={!!d.base_url && !!urlErr}
            helperText={(d.base_url && urlErr) || "The part before /models, e.g. https://host/v1"}
          />
          <TextField
            label={preset?.key_hint === "required" ? "API token" : "API token (optional)"}
            type="password"
            autoComplete="off"
            value={d.api_key}
            onChange={(e) => set({ api_key: e.target.value })}
            placeholder={d.has_key && !d.clear_key ? "Stored — leave blank to keep" : ""}
            helperText="Sent as a bearer token"
          />
          {d.has_key && (
            <FormControlLabel
              control={<Checkbox checked={d.clear_key} onChange={(e) => set({ clear_key: e.target.checked, api_key: "" })} />}
              label="Remove the stored token"
            />
          )}
          <Stack direction="row" spacing={2} sx={{ alignItems: "center" }}>
            <Button onClick={runCheck} disabled={!!urlErr || check === "loading"}>
              {check === "loading" ? "Checking…" : "Check connection"}
            </Button>
            {check && check !== "loading" && (
              <span className={check.error ? "err small" : "muted small"}>
                {check.error
                  ? `Failed: ${check.error}`
                  : `${(check.models ?? []).length} models${check.capabilities ? ", with what each accepts" : ""}`}
              </span>
            )}
          </Stack>
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" disabled={!!nameErr || !!urlErr || keyMissing} onClick={() => onDone(d)}>
          {initial.saved ? "Done" : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
