import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import {
  Outlet,
  createMemoryHistory,
  createRoute,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { Dashboard } from "./Dashboard";

// The band specs of the index overview, discriminated by rendering:
//
//   the coverage band is visually separate from the size and work bands —
//   three distinct .band containers, three distinct heads, the coverage one
//   carrying the segments;
//
//   a shortfall in a segment is a link to the work that closes it — one
//   cov_missing=<key> anchor on the short segment, a plain span on a segment
//   at 100%. A number without its next step is what these tests catch.
//
// The daemon is mocked at fetch: the band claims are about the DOM the
// component builds from a given payload, not about a round-trip.
//
// The router is the real TanStack router with a memory history, carrying the
// two routes Dashboard reads — /i/$index for useParams and /i/$index/d for
// the shortfall Link. A bare MemoryRouter (react-router, not here) would
// leave useParams undefined and the render crashes before any band exists.

const INDEX = "demo";

const COVERAGE_FULL_CAPTIONED = {
  total: 10,
  captioned: 10,
  tagged: 4,
  typed: 6,
  extracted: 8,
  attested: 9,
};

const COVERAGE_CLEAN = {
  total: 5,
  captioned: 5,
  tagged: 5,
  typed: 5,
  extracted: 5,
  attested: 5,
};

function mockDaemon(coverage: typeof COVERAGE_FULL_CAPTIONED) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      const json = (body: unknown) =>
        Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
      switch (path) {
        case "/status":
          return json({ documents: 10, fragments: 12, pending: 0, running: 0, failed: 0 });
        case "/api/coverage":
          return json({ coverage });
        case "/api/problems":
          return json({ problems: [] });
        case "/indexes":
          return json({ indexes: [] });
        case "/api/channels":
          return json({ channels: [] });
        case "/api/jobs":
          return json({ jobs: [] });
        case "/api/identity-jobs":
          return json({ jobs: [] });
        default:
          return Promise.resolve(new Response("not found", { status: 404 }));
      }
    }),
  );
}

const rootRoute = createRootRoute({ component: () => <Outlet /> });
const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/i/$index" });
const dashboardRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "/",
  component: Dashboard,
});
const docsRoute = createRoute({
  getParentRoute: () => indexRoute,
  path: "d",
  component: () => null,
  validateSearch: (s: Record<string, unknown>) => ({
    ...(typeof s.cov_missing === "string" && s.cov_missing ? { cov_missing: s.cov_missing } : {}),
  }),
});
const routeTree = rootRoute.addChildren([indexRoute.addChildren([dashboardRoute, docsRoute])]);

function renderDashboard() {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/i/" + INDEX] }),
  });
  return render(<RouterProvider router={router} />);
}

beforeEach(() => {
  mockDaemon(COVERAGE_FULL_CAPTIONED);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the three bands", () => {
  it("TestCoverageBandIsVisuallySeparateFromSizeAndWorkBands", async () => {
    renderDashboard();

    // Size, coverage and work are three distinct containers — not one blob a
    // reader has to scan — each under its own head.
    const [size, coverage, work] = await waitFor(() => {
      const [s, c, w] = [
        screen.getByText("Size", { selector: ".bandhead" }),
        screen.getByText("Coverage", { selector: ".bandhead" }),
        screen.getByText("Work", { selector: ".bandhead" }),
      ];
      if (![s, c, w].every((h) => h.closest(".band")))
        throw new Error("band heads not in .band containers yet");
      return [s, c, w].map((h) => h.closest(".band") as HTMLElement);
    });

    expect(size.className).toContain("band");
    expect(coverage.className).toContain("band");
    expect(work.className).toContain("band");
    expect(size).not.toBe(coverage);
    expect(coverage).not.toBe(work);

    // The coverage band is the one that carries the segments.
    expect(coverage.querySelector(".bandbars")).not.toBeNull();
    expect(coverage.querySelectorAll(".bandbar")).toHaveLength(5);
    expect(size.querySelector(".bandbars")).toBeNull();
    expect(work.querySelector(".bandbars")).toBeNull();
  });
});

describe("shortfalls link to the work that closes them", () => {
  it("TestShortfallRendersCovMissingLinkPerSegment", async () => {
    renderDashboard();
    const coverage = (await waitFor(() => {
      const c = screen.getByText("Coverage", { selector: ".bandhead" }).closest(".band");
      if (!c?.querySelector("a")) throw new Error("coverage band not rendered");
      return c;
    }))!;

    // Tagged is 4 of 10: exactly one shortfall anchor, carrying the filter.
    const taggedRow = [...coverage.querySelectorAll(".bandbar")].find((r) =>
      r.querySelector(".label")?.textContent?.includes("Tagged"),
    )!;
    const anchors = taggedRow.querySelectorAll("a");
    expect(anchors).toHaveLength(1);
    expect(anchors[0].getAttribute("href")).toContain("cov_missing=tagged");

    // Captioned is 10 of 10: the count renders as a plain span, not a link —
    // a full segment has nothing to close.
    const captionedRow = [...coverage.querySelectorAll(".bandbar")].find((r) =>
      r.querySelector(".label")?.textContent?.includes("Captioned"),
    )!;
    expect(captionedRow.querySelectorAll("a")).toHaveLength(0);
    const captionedCount = captionedRow.querySelector(".n");
    expect(captionedCount?.tagName).toBe("SPAN");
    expect(captionedCount?.textContent).toContain("10");

    // The other three segments are short too — one anchor each, so the band
    // carries exactly as many links as it has gaps.
    expect(coverage.querySelectorAll("a[href*='cov_missing']").length).toBe(4);
  });

  it("TestShortfallRendersCovMissingLinkPerSegment_cleanCorpusHasNoLinks", async () => {
    mockDaemon(COVERAGE_CLEAN);
    renderDashboard();
    const coverage = (await waitFor(() => {
      const c = screen.getByText("Coverage", { selector: ".bandhead" }).closest(".band");
      if (!c?.querySelector(".bandbar")) throw new Error("coverage band not rendered");
      return c;
    }))!;

    // Every segment at 100%: no shortfall, no anchor anywhere in the band.
    expect(coverage.querySelectorAll("a[href*='cov_missing']")).toHaveLength(0);
    expect(coverage.querySelectorAll(".bandbar .n")).toHaveLength(5);
    for (const n of coverage.querySelectorAll(".bandbar .n")) {
      expect(n.tagName).toBe("SPAN");
    }
  });
});
