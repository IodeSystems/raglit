import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  redirect,
} from "@tanstack/react-router";

import { RootShell } from "./shell/RootShell";
import { isAbsoluteDocPath, relDocPath } from "./docRoute";

// Every page is its own chunk (M4: the single bundle was over 500 kB). The
// review pane pulls in the attest packages and the document views pull in the
// page renderer, and neither is needed to open the overview. RootShell stays in
// the entry chunk because it is the frame the pending state renders in.
const IndexShell = lazyRouteComponent(() => import("./shell/IndexShell"), "IndexShell");
const ProjectShell = lazyRouteComponent(() => import("./shell/ProjectShell"), "ProjectShell");
const DocShell = lazyRouteComponent(() => import("./shell/DocShell"), "DocShell");
const Dashboard = lazyRouteComponent(() => import("./panes/Dashboard"), "Dashboard");
const Types = lazyRouteComponent(() => import("./panes/Types"), "Types");
const Overview = lazyRouteComponent(() => import("./panes/Overview"), "Overview");
const Project = lazyRouteComponent(() => import("./panes/Project"), "Project");
const Activity = lazyRouteComponent(() => import("./panes/Activity"), "Activity");
const Branches = lazyRouteComponent(() => import("./panes/Branches"), "Branches");
const Health = lazyRouteComponent(() => import("./panes/Health"), "Health");
const Jobs = lazyRouteComponent(() => import("./panes/Jobs"), "Jobs");
const Search = lazyRouteComponent(() => import("./panes/Search"), "Search");
const ProjectSearch = lazyRouteComponent(() => import("./panes/Search"), "ProjectSearch");
const DocumentList = lazyRouteComponent(() => import("./panes/DocumentList"), "DocumentList");
const Pages = lazyRouteComponent(() => import("./panes/Pages"), "Pages");
const PageDetail = lazyRouteComponent(() => import("./panes/Pages"), "PageDetail");
const Transcript = lazyRouteComponent(() => import("./panes/Transcript"), "Transcript");
const SeenIn = lazyRouteComponent(() => import("./panes/SeenIn"), "SeenIn");
const History = lazyRouteComponent(() => import("./panes/History"), "History");
const Notes = lazyRouteComponent(() => import("./panes/Notes"), "Notes");
const Email = lazyRouteComponent(() => import("./panes/Email"), "Email");
const Attest = lazyRouteComponent(() => import("./panes/Attest"), "Attest");
const AttestAsset = lazyRouteComponent(() => import("./panes/AttestAsset"), "AttestAsset");

// The document path is ONE encoded segment, and the router already does that.
//
// Worth stating because it is the rule everything here rests on, and because the
// obvious "help" breaks it. The vanilla page split the path on "/" and rejoined
// it, which silently ate the leading slash: "/home/someone/…" came back as
// "home/someone/…", and since documents are stored by ABSOLUTE path, every deep
// link resolved to a document that does not exist.
//
// TanStack's encodePathParam is encodeURIComponent, so a slash becomes %2F and
// the path stays one segment with no help from us. Adding a params.stringify
// that encodes as well produced %252F — double-encoded, still functional because
// the extra decode cancelled it, and unreadable in the address bar. Do not add
// one back.
const rootRoute = createRootRoute({ component: RootShell });

// "/" is the whole daemon: projects, then their indexes, then an index's
// branches. It used to REDIRECT to whichever index came back first, which made a
// daemon serving four projects look like a single-corpus tool and left nine
// `project__index` names as the only way in.
//
// The redirect is also what the legacy-hash rewrite lost its race to: it fired
// in beforeLoad, so an old `#/documents/<index>/…` link resolved to the first
// index before any component mounted. Removing it removes that too.
const homeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: Overview,
});

// A project is the "<project>__" prefix on an index name and nothing else — see
// cmd/raglit/projectsapi.go. Indexes stay addressed as /i/:index rather than
// nesting under /p/:project/i/:index: the daemon name is already unique and
// already what every endpoint takes, so nesting would duplicate the namespace in
// the URL and break every link that exists.
const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/p/$project",
  component: ProjectShell,
});

const projectIndexesRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "/",
  component: Project,
});

const projectSearchRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "search",
  component: ProjectSearch,
  validateSearch: (s: Record<string, unknown>) => ({
    q: typeof s.q === "string" ? s.q : "",
    mode: typeof s.mode === "string" ? s.mode : "bm25",
  }),
});

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/i/$index",
  component: IndexShell,
});

const dashboardRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "/",
  component: Dashboard,
});

const healthRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "health",
  component: Health,
});

// The job id is optional and lives in the path so a health row can link AT a
// failure. A link to a tab of two hundred rows leaves the reader searching by
// eye, which is what this route exists to avoid.
const jobsRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "jobs",
  component: Jobs,
});

const jobRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "jobs/$jobId",
  component: Jobs,
});

const searchRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "search",
  component: Search,
  validateSearch: (s: Record<string, unknown>) => ({
    q: typeof s.q === "string" ? s.q : "",
    mode: typeof s.mode === "string" ? s.mode : "bm25",
  }),
});

const docsRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "d",
  component: DocumentList,
});

// The document segment is the DISPLAY path, relative to the index root
// ("records/2010-westview-survey.pdf"); every document endpoint accepts it.
// An absolute path — any link written before 2026-10-20, every legacy hash
// link — is rewritten to the display path before anything renders, keeping
// the sub-tab and the query, so the home directory never sits in the address
// bar. A path the index cannot place is left alone; the daemon still takes it.
const docRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "d/$doc",
  component: DocShell,
  beforeLoad: async ({ params, matches, location, preload }) => {
    if (preload || !isAbsoluteDocPath(params.doc)) return;
    const rel = await relDocPath(params.index, params.doc);
    if (!rel || rel === params.doc) return;
    const leaf = matches[matches.length - 1];
    throw redirect({
      // The deepest match is the sub-tab being opened ("…/pages/$page",
      // "…/transcript"); redirecting to it keeps the reader where the link
      // pointed. Typed loosely because the leaf is any route under this one.
      to: (leaf?.fullPath ?? "/i/$index/d/$doc") as "/i/$index/d/$doc",
      params: { ...(leaf?.params ?? params), doc: rel } as { index: string; doc: string },
      search: location.search as never,
      replace: true,
    });
  },
});

// A bare document URL is not an error, it is somebody who has not chosen a
// sub-tab yet. Redirect rather than render nothing.
const docIndexRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "/",
  beforeLoad: ({ params }) => {
    // A mail archive opens on its thread, not on its pages. "Page 7 of 26" is
    // the indexed shape of an email and not how anybody reads one — and the
    // pages view was the complaint that produced this route.
    if (/\.(eml|mbox)$/i.test(params.doc)) {
      throw redirect({ to: "/i/$index/d/$doc/email", params });
    }
    throw redirect({ to: "/i/$index/d/$doc/pages", params });
  },
});

const pagesRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "pages",
  component: Pages,
});

// One page, on its own. A page of a bundle is the unit somebody actually wants
// to send you — "the signature is on page 14" is a link — and it is the only
// scope at which a note about a bundle means anything, so this renders the page
// AND its notes rather than highlighting a row in the list.
const pageRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "pages/$page",
  component: PageRoute,
});

const transcriptRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "transcript",
  component: Transcript,
});

const seenRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "seen",
  component: SeenIn,
});

const historyRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "history",
  component: History,
});

const notesRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "notes",
  component: Notes,
});

// A mail archive as a thread. Only meaningful for .eml/.mbox; the tab is hidden
// for everything else and the endpoint refuses it, so a hand-typed URL says why
// rather than rendering an empty conversation.
const emailRoute = createRoute({
  getParentRoute: () => docRoute,
  path: "email",
  component: Email,
});

// An index's branches: the copy-on-write overlays forked from it. The daemon has
// had fork/list/delete since the branch-storage work landed and nothing ever
// showed them, so the feature was reachable only by curl.
const branchesRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "branches",
  component: Branches,
});

// Two surfaces the daemon has served for a while and the UI did not read at
// all — see plan/ui-redesign.md §1.
const typesRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "types",
  component: Types,
});

const activityRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "activity",
  component: Activity,
});

const attestRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "attest",
  component: Attest,
});

// An attest asset is a root-relative path, so it needs the same one-segment
// treatment as a document path and for the same reason.
const attestAssetRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "attest/a/$asset",
  component: AttestAsset,
});

// The param arrives as a string; the component wants a number, and a URL
// carrying "pages/abc" should say the page does not exist rather than render
// against NaN.
function PageRoute() {
  const { page } = pageRoute.useParams();
  return <PageDetail page={Number(page)} />;
}

const routeTree = rootRoute.addChildren([
  homeRoute,
  projectRoute.addChildren([projectIndexesRoute, projectSearchRoute]),
  indexRoute.addChildren([
    dashboardRoute,
    healthRoute,
    jobsRoute,
    jobRoute,
    searchRoute,
    docsRoute,
    docRoute.addChildren([
      docIndexRoute,
      pagesRoute,
      pageRoute,
      transcriptRoute,
      seenRoute,
      historyRoute,
      notesRoute,
      emailRoute,
    ]),
    typesRoute,
    activityRoute,
    branchesRoute,
    attestRoute,
    attestAssetRoute,
  ]),
]);

export const router = createRouter({
  routeTree,
  // Shown while a page's chunk loads, after a short delay so a cached chunk
  // does not flash it.
  defaultPendingComponent: () => <div className="empty">Loading…</div>,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
