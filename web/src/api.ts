// The daemon's JSON surface, as the page sees it.
//
// Everything is same-origin and relative — the bundle is served by the daemon it
// talks to, so there is no base URL to configure and no CORS to negotiate.

// ApiError carries the status, because several callers need to tell one refusal
// from another — a 404 from the review mount means "nothing has read this yet",
// which is a state to explain rather than an error to report.
export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

// The daemon answers failures with huma's ErrorModel. Rendering that JSON at a
// person is what the review page was doing: a reader clicked a tab and got
// `{"$schema":"…/ErrorModel.json","title":"Not Found",…}`, which reads as the UI
// being broken rather than as an answer about their document.
async function fail(r: Response): Promise<never> {
  const body = await r.text();
  let msg = body || String(r.status);
  try {
    const j = JSON.parse(body) as { title?: string; detail?: string };
    msg = j.detail || j.title || msg;
  } catch {
    // Not JSON — a plain-text error from one of the non-huma routes. Use it.
  }
  throw new ApiError(msg, r.status);
}

export async function getJSON<T>(path: string, params?: Record<string, unknown>): Promise<T> {
  const r = await fetch(path + qs(params));
  if (!r.ok) return fail(r);
  return (await r.json()) as T;
}

export async function postJSON<T>(
  path: string,
  params?: Record<string, unknown>,
  body?: unknown,
): Promise<T> {
  const r = await fetch(path + qs(params), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
  });
  if (!r.ok) return fail(r);
  // A 204 has no body to parse, and several of these operations answer with one.
  if (r.status === 204) return undefined as T;
  const text = await r.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

function qs(params?: Record<string, unknown>): string {
  if (!params) return "";
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue;
    u.set(k, String(v));
  }
  const s = u.toString();
  return s ? "?" + s : "";
}

// ── shapes ────────────────────────────────────────────────────────────────
// Only the fields the page reads. The daemon's OpenAPI document is the full
// contract; mirroring all of it here would be a second copy to keep in step.

export type IndexInfo = {
  name: string;
  documents?: number;
  fragments?: number;
};

// One scheduling lane's share of the queue. Lanes no longer SCHEDULE anything —
// admission is per model (see ModelChannel) — but the classification is still
// recorded per job and still answers "is this queue scans or text files", which
// "12 pending" cannot.
export type LaneStatus = {
  pending?: number;
  running?: number;
  slots?: number;
};

// One model's admission channel. The width is LEARNED: a model starts at one
// slot and widens only while calls succeed, halving whenever the server pushes
// back. Shown because a learned number nobody can see is indistinguishable from
// a bug — a model sitting at 1 with a high 429 count is throttled, not broken.
export type ModelChannel = {
  model: string;
  width?: number;
  in_flight?: number;
  peak?: number;
  calls?: number;
  n_429?: number;
  chilled?: boolean;
};

// ── mail archives ─────────────────────────────────────────────────────────
// The index holds one message per page with four headers kept and the rest
// counted, which is the right transcription and unreadable as a conversation.
// /api/email re-reads the archive and returns the structure that was flattened.

export type EmailHeader = { name: string; value: string };

export type EmailAttachment = {
  name: string;
  mime?: string;
  size?: number;
  sum?: string;
  // The extracted file beside the archive, when extraction ran. EMPTY means the
  // attachment was seen and not extracted — a different fact from "there is no
  // attachment", and the two must not render alike.
  path?: string;
};

export type EmailMessage = {
  // The page this message occupies in the indexed document, so a search
  // citation and the thread agree about which message is which.
  page: number;
  // Enclosure: 0 is a message in the archive, 1 is one forwarded inside it.
  depth: number;
  from?: string;
  to?: string;
  cc?: string;
  date?: string;
  subject?: string;
  headers?: EmailHeader[];
  body?: string;
  attachments?: EmailAttachment[];
  notes?: string[];
};

export const getEmailThread = (index: string, path: string) =>
  getJSON<{ path: string; messages: EmailMessage[] }>("/api/email", { index, path });

// Whether a document is one /api/email can read. The daemon refuses anything
// else, so asking first is what keeps a 400 out of the document view.
export const isMailArchive = (path: string) => /\.(eml|mbox)$/i.test(path);

export const listChannels = () =>
  getJSON<{ channels: ModelChannel[] }>("/api/channels");

export type StatusSnapshot = {
  documents?: number;
  fragments?: number;
  pending?: number;
  running?: number;
  done?: number;
  failed?: number;
  jobs_per_min?: number;
  lanes?: Record<string, LaneStatus>;
};

// One stage of an ingest, as the pipeline recorded it: fetch → extract → ocr →
// segment → embed → commit, each with what it did and whether it survived.
//
// This is where a job's actual account lives — "23 page(s): 0 text-layer, 23
// scanned", "SALVAGED: 1 of 23 page(s) unread (p9)", the endpoint's verbatim
// 503. The daemon has always returned it and nothing rendered it, so a health
// row could name a bad ingest and offer no way to see what went wrong.
export type JobStage = {
  // The row's own id — the only stable key a stage has. seq is NOT unique within
  // a job: a retry appends a second run under the same job_id with seq
  // restarting at 1, so seq alone names several different rows.
  id: number;
  seq: number;
  name: string;
  engine?: string;
  state: string; // done | warn | error | skip
  detail?: string;
  at?: number;
};

// The field names ARE the daemon's (raglit.JobInfo). They were guessed once —
// `target` and `detail`, neither of which the API has ever returned — and the
// Target column rendered blank on every row for as long as the table existed.
export type Job = {
  id: number;
  url: string;
  title?: string;
  state: string;
  mode?: string;
  lane?: string;
  fragments?: number;
  error?: string;
  enqueued_at?: number;
  started_at?: number;
  finished_at?: number;
  eta_seconds?: number;
  stages?: JobStage[];
};

export type DocumentRow = {
  path: string;
  fragments?: number;
  pages?: number;
  vision?: number;
  text?: number;
  name?: string;
  kind?: string;
};

export type Hit = {
  // Which index the hit came from. Always present, and it MATTERS once a search
  // can span more than one: a project search returns hits from several indexes
  // and each result must link into the one it actually came from, not into the
  // scope that was searched.
  index?: string;
  doc_id: string;
  title?: string;
  path?: string;
  score?: number;
  snippet?: string;
  page?: number;
  // Whose words these are. Empty = the document's own, transcribed. "identity" =
  // a generated caption. "described" = a model's account of an IMAGE — the 700
  // words a vision model writes about a photograph, naming a car's make and its
  // licence plate, none of which anybody wrote down. Generated hits rank in the
  // same list on purpose (it is the only way a photograph is findable at all),
  // so the RENDERER is what keeps them from being read as the record.
  origin?: string;
};

// ── projects ──────────────────────────────────────────────────────────────
// A project is not stored anywhere: it is the "<project>__" prefix a client puts
// on every index name (cmd/raglit/namespace.go), and /api/projects derives the
// grouping from the names. See cmd/raglit/projectsapi.go for why there is no
// projects table.

export type ProjectIndex = {
  name: string; // the daemon name — what every endpoint takes
  local: string; // what a person in that project calls it
  documents?: number;
  fragments?: number;
  pending?: number;
  running?: number;
  failed?: number;
  parent?: string; // set when this index is a branch of another
};

export type Project = {
  name: string; // "" means the indexes carrying no namespace at all
  indexes: ProjectIndex[];
  documents?: number;
  fragments?: number;
  pending?: number;
  running?: number;
  failed?: number;
  home?: string;
  watching?: boolean;
  files?: number;
};

export const listProjects = () => getJSON<{ projects: Project[] }>("/api/projects");

export type Branch = {
  name: string;
  parent: string;
  created_at?: number;
  last_accessed_at?: number;
  documents?: number;
};

export const listBranches = () => getJSON<{ branches: Branch[] }>("/api/branches");

export const listIndexes = () => getJSON<{ indexes: IndexInfo[] }>("/indexes");

export const getStatus = (index: string) => getJSON<StatusSnapshot>("/status", { index });

export const listJobs = (index: string, limit = 200) =>
  getJSON<{ jobs: Job[] }>("/api/jobs", { index, limit });

// One job by id, however old. listJobs answers "the newest N", which is the
// wrong shape for a citation: the health report links AT job 927 while the
// newest is 1467, so the row the link is about is not in the window at all.
export const getJob = (index: string, id: number) =>
  getJSON<{ jobs: Job[] }>("/api/jobs", { index, id }).then((r) => r.jobs?.[0]);

export const listDocuments = (index: string) =>
  getJSON<{ documents: DocumentRow[] }>("/api/documents", { index });

export const search = (index: string, q: string, mode: string) =>
  getJSON<{ hits: Hit[] }>("/search", { index, q, mode });

// One document, assembled server-side. cmd/raglit/docdetail.go explains why the
// join happens there and not here: a UI stitching five responses together gets
// to decide what "reviewed" means, and that decision belongs next to the data.
export type DocIdentity = {
  name?: string;
  summary?: string;
  kind?: string;
  source?: string; // "machine" | "person"
  model?: string;
  at?: string;
};

export type DocFigure = {
  kind?: string;
  description: string;
};

export type DocPage = {
  page: number;
  text: string;
  source?: string;
  engine?: string;
  read_by?: string;
  read_at?: string;
  image_url?: string;
  has_layout?: boolean;
  figures?: DocFigure[];
};

// One block the layout-aware reader reported: where it is, what kind it thought
// it was, and what it read there. Coordinates are normalised 0-1000 PER AXIS,
// independently — not pixels — so they place as percentages with no image
// dimensions and stay aligned at any rendered width.
export type LayoutBox = {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
  label?: string;
  text?: string;
};

// A page seen three ways: what the reader saw (boxes), what it wrote (raw), and
// what search actually matches (indexed). They have differed by 40% of their
// bytes with nothing to show it.
export type PageLayout = {
  doc: string;
  page: number;
  engine?: string;
  model?: string;
  has_image: boolean;
  img_w?: number;
  img_h?: number;
  boxes?: LayoutBox[];
  raw?: string;
  indexed?: string;
};

export function getPageLayout(index: string, path: string, page: number) {
  return getJSON<PageLayout>("/api/page-layout", { index, path, page });
}

export type DocJob = {
  id: number;
  state: string; // pending | running
  mode?: string;
  since?: number;
};

export type SeenInRow = {
  path: string;
  title?: string;
  jaccard?: number;
  relation?: string;
  ruling?: string;
};

export type HistoryEvent = {
  kind: string;
  page?: number;
  seq?: number;
  source?: string;
  note?: string;
  by?: string;
  at?: string;
  active?: boolean;
  unit?: string;
};

export type AttestSummary = {
  producer?: string;
  total?: number;
  confirmed?: number;
  corrected?: number;
  affirmed?: number;
  unclear?: number;
  untouched?: number;
};

export type DocDetail = {
  path: string;
  // The handle the attest mount takes: ROOT-RELATIVE, because Root is attest's
  // security boundary. NOT the absolute path the document list and the routes
  // use — passing that one is a 404 "no such asset".
  asset?: string;
  title: string;
  kind?: string;
  identity?: DocIdentity;
  original?: string;
  pages?: DocPage[];
  text?: string;
  seen_in?: SeenInRow[];
  attest?: AttestSummary;
  history?: HistoryEvent[];
  // Work already queued for this document. It gates the re-ingest/re-read
  // buttons: the queue does not dedupe, so a button that stays live while a job
  // runs is an invitation to start the same minutes-long VLM pass three times.
  in_flight?: DocJob[];
};

export const docDetail = (index: string, path: string) =>
  getJSON<DocDetail>("/api/doc-detail", { index, path });

// Re-title. There is no separate endpoint for it: /api/identify already records
// a PERSON's name/summary/kind, and identity.go guarantees a machine re-run will
// not overwrite what a person said. That guarantee is the whole feature — the
// case this exists for is an auto-title that described a survey when the
// document is somebody's annotation OF the survey.
export const identify = (
  index: string,
  path: string,
  want: { name?: string; summary?: string; kind?: string },
) => postJSON<DocIdentity>("/api/identify", { index, path, ...want });

export type Note = {
  id: number;
  page?: number | null;
  body: string;
  author?: string;
  created_at?: string;
};

export const listNotes = (index: string, path: string) =>
  getJSON<{ notes: Note[] }>("/api/notes", { index, path });

export const addNote = (
  index: string,
  path: string,
  note: { body: string; author?: string; page?: number | null },
) => postJSON<Note>("/api/notes", { index, path }, note);

export const deleteNote = (index: string, id: number) =>
  postJSON<void>("/api/notes/delete", { index, id });
