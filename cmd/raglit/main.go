// Command raglit is a local document RAG index: BM25 search over a single
// portable SQLite file, at document:page:fragment grain.
//
//	raglit index --db idx.sqlite FILE|DIR...   ingest text/markdown into the index
//	raglit search --db idx.sqlite "query"      BM25-ranked fragments, best first
//
// PDF pagify + vision-LLM OCR (feeding the same index) and an MCP `serve` mode
// land next; this is the offline lexical core.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
)

func main() {
	// Before anything else: a source-stamped build rebuilds itself when the tree
	// changed and re-execs. No-op for a released binary (srcDir empty) and for
	// spawned children. See selfbuild.go.
	selfUpdate()
	if len(os.Args) < 2 {
		// No command: run the setup wizard if this home isn't initialized yet,
		// otherwise show usage. (raglit is unusable until `init` writes config.)
		if !raglit.Inited(raglit.DiscoverHome()) {
			fmt.Fprintln(os.Stderr, "raglit is not configured yet — starting setup.")
			if err := runInit(nil); err != nil {
				fmt.Fprintf(os.Stderr, "raglit: %v\n", err)
				os.Exit(1)
			}
			return
		}
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(os.Args[2:])
	case "index":
		err = runIndex(os.Args[2:])
	case "ingest":
		err = runIngest(os.Args[2:])
	case "sync":
		err = runSync(os.Args[2:])
	case "branch":
		err = runBranch(os.Args[2:])
	case "drop-index":
		err = runDropIndex(os.Args[2:])
	case "watch":
		err = runWatch(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "reread":
		err = runReread(os.Args[2:])
	case "retry":
		err = runRetry(os.Args[2:])
	case "work":
		err = runWork(os.Args[2:])
	case "search":
		err = runSearch(os.Args[2:])
	case "similar":
		err = runSimilar(os.Args[2:])
	case "diff":
		err = runDiff(os.Args[2:])
	case "mark":
		err = runMark(os.Args[2:])
	case "marks":
		err = runMarks(os.Args[2:])
	case "withdraw":
		err = runWithdraw(os.Args[2:])
	case "withdrawn":
		err = runWithdrawn(os.Args[2:])
	case "forget":
		err = runForget(os.Args[2:])
	case "slice":
		err = runSlice(os.Args[2:])
	case "slices":
		err = runSlices(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "daemon":
		err = runDaemon(os.Args[2:])
	case "service":
		err = runService(os.Args[2:])
	case "review":
		err = runReview(os.Args[2:])
	case "demo":
		err = runDemo(os.Args[2:])
	case "pagify":
		err = runPagify(os.Args[2:])
	case "refragment":
		err = runRefragment(os.Args[2:])
	case "regions":
		err = runRegions(os.Args[2:])
	case "region":
		err = runRegion(os.Args[2:])
	case "attest":
		err = runAttest(os.Args[2:])
	case "ocr":
		err = runOcr(os.Args[2:])
	case "transcribe":
		err = runTranscribe(os.Args[2:])
	case "readings":
		err = runReadings(os.Args[2:])
	case "identify":
		err = runIdentify(os.Args[2:])
	case "health":
		err = runHealth(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "raglit: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `raglit — local BM25 document index (SQLite FTS5)

usage:
  raglit init   [--home DIR]                 configure endpoint + models (writes ./.raglit)
  raglit demo                                self-contained offline tour
  raglit index  [--home DIR] [--embed] FILE|DIR...   ingest local files (+ PDFs via OCR)
  raglit ingest [--home DIR] [--now] TARGET...  queue folders/files/URLs (file://, http(s)://)
                --fresh re-reads a document even if nothing changed: skips the
                unchanged-bytes check AND the cross-index pool. Use it when a cached
                result is wrong for a reason the cache key cannot see.
  raglit sync   [--home DIR] [--index N] [--dry-run]   ingest the config's [indexes] roots
                (per-index include/ignore globs, honors .gitignore; dedup skips unchanged)
  raglit branch list|fork NAME|delete NAME   manage copy-on-write branch indexes
                (daemon only; namespaced + scoped to this project)
  raglit watch  [start|list|stop]            daemon auto re-ingests this project's
                roots on change (config "watch":true; sync auto-registers)
  raglit retry  [--home DIR] [--dry-run] [--match S]   requeue failed ingest jobs
  raglit reread [--suspect] [--dry-run] <path>...      purge cached page OCR and read again
                                                       (a re-index alone CANNOT fix a bad read —
                                                        the page cache returns the same answer)
                (skips jobs whose file is gone — a rename outlives the row)
  raglit work   [--home DIR] [--embed]       drain the ingest queue once, then exit
  raglit status [--home DIR]                 index + queue status (done/pending/rate/eta)
  raglit search [--home DIR] [--mode M] [-n N] "query"
  raglit similar [--build|--rebuild|--status|--all] [--json] FILE|DOCPATH...
                near-duplicate + containment detection over shingles, at PAGE grain.
                Reports both directions (a deed INSIDE a title commitment has
                containment 1.0 one way and a Jaccard of 0.05), the page alignment
                ("probe p12-14 = match p1-3"), and which NUMBERS differ between two
                copies of one instrument. Local computation — no model, works offline.
                --build sketches documents that have none; --all audits the corpus.

  raglit diff [--json] A B
                compare two transcripts PAGE BY PAGE: byte identity, a per-page
                match rate, which pages are missing, and the numbers that differ.
                A whole-document 0.93 is equally consistent with a clean re-scan
                and with a refiling whose page 3 changed; the per-page shape is
                what tells those apart, and an average destroys it.

  raglit readings <DOC> [PAGE] [--text] [--json]
                every recorded version of what a page says, oldest first, with
                the reading in force marked. A correction attests that a better
                reading exists; it does not erase the old one, because a
                quotation taken before it matched the text in force then.

  raglit withdraw --reason "..." [--by WHO] [--dry-run] <path|dir>...
                rule a document OUT of the corpus, with grounds. Not a delete:
                the file stays on disk, the reason is recorded in the audit
                trail, and the INGEST path honours it — so the next sweep does
                not quietly put it back. Names what still cites the document,
                and rewrites nothing: a citation is a claim its author made.
                A directory withdraws what is indexed beneath it.

  raglit withdrawn [--json]     what has been ruled out of the corpus, and why

  raglit forget [--dry-run] <path|dir>...
                drop a document from the INDEX; the file on disk is untouched.
                Not a withdrawal: no grounds are recorded and nothing stops a
                re-index — for a row that should never have existed (raglit's own
                transcription sidecars, indexed as documents) rather than for a
                document somebody ruled out.

  raglit marks [--todo] [--json] [DOCPATH]
                what an overlapping pair IS, as opposed to how much text it shares.
                --todo lists pairs nobody has ruled on, each with a proposal: a
                COPY is the same instrument again (a scan of a PDF already held);
                a VERSION is the same instrument filed or corrected differently,
                and both matter. No score separates them — a re-recorded deed and
                a scan of one both align at 0.97 — so raglit proposes from WHAT
                disagrees and a person rules.

  raglit marks --identical [--write] [--json]
                documents held more than once BYTE for byte. Needs no sketches —
                the index already knows the hashes. This is the one relation with
                no judgement in it, so --write records them as copies attributed
                to raglit rather than to you; a person's ruling still overrides.

  raglit slice <DOC> <FROM-TO> [--title T] [--id ID] [--no-materialize]
                declare that a page range of a bundle is a document, and build a
                citable child from it. The bundle is never cut up — it is what
                was filed — and the child keeps the PARENT's page numbers, so a
                quotation from it stays checkable against the exhibit as filed.

  raglit slices [DOC] [--materialize] [--json]
                declared sub-documents, with coverage: which pages of a bundle
                no slice claims. That is what says a bundle is fully linearized
                rather than merely started.

  raglit identify [--list] [--force] [--limit N] [--wait] [--dry-run] [DOC...]
                what a document IS, as opposed to what its file is called: a
                caption, a summary and a kind, asked of the model on the text
                already indexed. Ingest does this per document; this is for a
                corpus indexed before it existed. With no arguments it QUEUES
                every document that has no name yet and returns — the rows are
                durable, the daemon works them two at a time (the endpoint's
                concurrency), and killing the terminal loses nothing. --wait
                follows the queue; 'raglit status' shows what is outstanding.
                The file is NEVER renamed: the caption is a display name and a
                search target, and the summary is indexed so a query for
                "purchase and sale agreement" can rank a document whose body
                never says it. Search marks such a hit — findable by it, not
                quotable from it.

  raglit identify --name "..." [--summary "..."] [--kind K] <DOC>
                a PERSON saying what the document is. Supersedes the machine's
                caption and is never regenerated over.

  raglit mark <A> <B> <copy|version|unrelated> [--supersedes PATH] [--note ...]
                record that ruling. Appended to raglit-audit.jsonl beside the
                documents and projected into judgements.db, never into .raglit/ —
                a ruling cannot be recomputed, so it must outlive a reindex and
                reach the other machines. The trail is the record: marks
                --rebuild reconstructs the database from it.
  raglit serve  [--home DIR] [-n N] [--embed]   stdio MCP server (search + ingest + index_status)
  raglit daemon [--root DIR|--home DIR] [--addr :7420] [--embed]   multi-protocol daemon:
                REST + review UI at / + OpenAPI (/openapi.json) + GraphQL (/graphql); scoped
                per-index storage under --root (default ~/.raglit); --home for a single index;
                records <root>/daemon.json for client discovery; --stop signals a running one,
                --restart replaces it with a detached one on these flags (after a rebuild)
  raglit review [--root DIR|--home DIR] [--addr :7420] [--embed]   same daemon, review-UI banner
  # daemon-routed clients (the default) need a project name: config "project" or
  # --project NAME (namespaces this project's indexes so they don't collide);
  # --embedded (or --db) opts out for a single-session in-process index
  # add --daemon URL (or $RAGLIT_DAEMON) to ingest/search/status to call a daemon
  raglit pagify [--out DIR] FILE.pdf...      extract page images (image/scanned PDFs)
  raglit refragment [--dry-run]              re-ingest documents whose fragments are
                larger than the embed model accepts (probes the limit, stores it)
  raglit regions [--page N] [--depth D] [--write] FILE   read a page as a TREE of
                regions — for a sheet whose text is too small to survive one look.
                Asks the model what is here and where to look closer, and descends.
                --write records the tree in <doc>.raglit-regions.json beside it
  raglit region [--list|--locate TEXT] FILE [REGION-ID]   re-render the exact crop
                a passage was read from: same bbox, same rotation, same dpi, and a
                digest check that it IS that image. PNG to stdout, or --out FILE.
                --locate says which region a quotation came from
  raglit attest [--port N] FILE              hand a recorded region read to a person:
                serves a review where each region can be confirmed, corrected or
                marked unreadable, beside the exact crop its text was read from.
                Verdicts append to <doc>.attest.jsonl; the region read is untouched
  raglit ocr    [--llm-*] IMAGE...           transcribe page images via a vision model
  raglit transcribe [--write] FILE...        page-delineated markdown of a document
                (--write puts <doc>.raglit-transcription.md beside it; index option
                 "writeback_transcription_md" does the same during ingest)
  raglit health [--json] [--kind K] [--quiet]
                what is WRONG with this corpus, worst first: documents that are
                indexed and unsearchable, PDFs with no pages, failed jobs with
                the stage they died in, pages the model would not segment, and
                jobs the endpoint made fight for. Exits non-zero if anything is
                wrong, so it works as a check and not only as a page to read.
                (A withdrawal is a decision, not a fault, and never fails it.)

  raglit doctor [--home DIR]                 OCR readiness: cheap engine + vision endpoint

flags:
  --home        index home dir; holds index.sqlite + originals/ + pages/. Default:
                nearest ./.raglit walking up from cwd, else $RAGLIT_HOME or ~/local/raglit
  --db          raw index file path (overrides --home; skips originals storage)
  -n            search/serve: max (default) results
  --embed       index: also embed fragments for vector/hybrid search
  --mode        search: bm25 (default) | vec | hybrid  (vec/hybrid need --embed'd index)
  --llm-url     model base URL (default https://llm.iodesystems.com)
  --llm-model   vision model id (default ternary-bonsai-27b)
  --embed-model embedding model id (default nomic-embed-text)
  --llm-key     API key (or $RAGLIT_LLM_KEY)

PDF indexing extracts embedded page images (pure-Go; image/scanned PDFs only)
and OCRs each page via the vision model. --embed adds nomic vectors; search
--mode hybrid fuses BM25 + cosine with reciprocal-rank fusion.
`)
}

// addStoreFlags registers --home/--db on fs and returns an opener to call after
// fs.Parse. --db (raw path) wins if set; otherwise --home (or the default home)
// is used, which also stores ingested originals.
func addStoreFlags(fs *flag.FlagSet) (open func() (*raglit.Store, error), homeOf func() raglit.Home) {
	home := fs.String("home", "", "index home dir (default $RAGLIT_HOME or ~/local/raglit)")
	db := fs.String("db", "", "raw index file path (overrides --home)")
	index := fs.String("index", "", "index name (default: config default_index, else 'default')")
	homeOf = func() raglit.Home {
		if *home != "" {
			return raglit.Home(*home)
		}
		return raglit.DiscoverHome()
	}
	open = func() (*raglit.Store, error) {
		if *db != "" {
			return raglit.Open(*db)
		}
		return raglit.OpenIndex(homeOf(), resolveIndexName(*index, homeOf))
	}
	return open, homeOf
}

// resolveIndexName picks the effective index: an explicit --index wins, else the
// home's config default_index, else "default".
func resolveIndexName(flagVal string, homeOf func() raglit.Home) string {
	if flagVal != "" {
		return flagVal
	}
	if cfg, _, _ := raglit.LoadConfig(homeOf()); cfg.DefaultIndex != "" {
		return cfg.DefaultIndex
	}
	return "default"
}

// resolveDaemon picks the effective daemon URL to route to: an explicit --daemon
// (or $RAGLIT_DAEMON, its flag default) wins, else the home config's daemon_url.
// Empty → local/embedded mode (open the index directly). This is what makes a
// project's .raglit/ a CLIENT config: set daemon_url and every command talks to
// the daemon without passing --daemon each time.
func resolveDaemon(flagVal string, homeOf func() raglit.Home) string {
	if flagVal != "" {
		return flagVal
	}
	if cfg, _, _ := raglit.LoadConfig(homeOf()); cfg.DaemonURL != "" {
		return cfg.DaemonURL
	}
	return ""
}

func runIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs) // vision model for PDFs; embed model when --embed
	embed := fs.Bool("embed", false, "also embed fragments for vector/hybrid search")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("index: no files/dirs given")
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()
	lf.resolve(homeOf())
	if *embed {
		if err := lf.requireEmbed(); err != nil {
			return err
		}
		store.SetEmbedder(lf.embedder())
		if ie := buildImageEmbedder(homeOf()); ie != nil {
			store.SetImageEmbedder(ie)
		}
	}

	// Local index goes through the SAME pipeline as URL ingest: enqueue each
	// file, then drain now. That gives local files the LLM segmentation (text +
	// code) and PDF OCR + concurrent embed — one code path, no duplicate splitter.
	var files []string
	for _, root := range fs.Args() {
		fi, err := os.Stat(root)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && !raglit.IsGeneratedSidecar(p) && (isText(p) || isPDF(p)) {
					files = append(files, p)
				}
				return nil
			})
			if err != nil {
				return err
			}
		} else {
			files = append(files, root)
		}
	}
	for _, p := range files {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if _, err := store.Enqueue(abs, ""); err != nil {
			return err
		}
	}
	n, err := buildWorker(store, lf, homeOf(), nil).Drain(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("indexed %d file(s) → %s\n", n, store.Path())
	return nil
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	openStore, homeOf := addStoreFlags(fs)
	lf := addLLMFlags(fs)
	client := addClientFlags(fs) // --daemon + --embedded
	limit := fs.Int("n", 10, "max results")
	mode := fs.String("mode", "bm25", "bm25 | vec | hybrid (vec/hybrid need embeddings)")
	path := fs.String("path", "", "constrain to documents whose path starts with this prefix (a subtree)")
	fs.Parse(args)
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("search: empty query")
	}

	// Default: query the shared daemon (auto-started if needed); --embedded/--db
	// open the index in-process.
	dURL, ns, err := client(homeOf, fs.Lookup("db").Value.String() != "")
	if err != nil {
		return err
	}
	if dURL != "" {
		// Empty --index → this project's indexes + its shared namespaces.
		sel := nsReadSelector(ns, projectShared(homeOf), fs.Lookup("index").Value.String())
		return daemonSearchPrint(dURL, query, sel, *mode, *path, *limit, ns)
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	var hits []raglit.Hit
	switch *mode {
	case "bm25":
		hits, err = store.SearchPath(query, *path, *limit)
	case "vec", "hybrid":
		lf.resolve(homeOf())
		if err := lf.requireEmbed(); err != nil {
			return err
		}
		store.SetEmbedder(lf.embedder())
		if *mode == "vec" {
			hits, err = store.VecSearchPath(ctx, query, *path, *limit)
		} else {
			hits, err = store.HybridSearchPath(ctx, query, *path, *limit)
		}
	default:
		return fmt.Errorf("search: unknown --mode %q (bm25|vec|hybrid)", *mode)
	}
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Println("(no matches)")
		return nil
	}
	for i, h := range hits {
		loc := h.Path
		if h.Page > 0 {
			loc = fmt.Sprintf("%s p%d", h.Path, h.Page)
		}
		if h.IsDescription() {
			// The hit is on the document's caption/summary, which is a machine's
			// description of it. Said here rather than left to the page number,
			// because there is no page and the text reads like the document.
			loc += "  [what this document is — a description of it, not its words]"
		}
		fmt.Printf("%d. [%.3f] %s\n   %s\n", i+1, h.Score, loc, clip(oneLine(h.Text), 160))
	}
	return nil
}

func isText(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".txt", ".md", ".markdown", ".rst", ".text":
		return true
	}
	return false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// runReread purges the cached page OCR for a document and reads it again.
//
// `raglit index` on a document whose OCR was wrong is a NO-OP: the page cache is
// keyed by the image's SHA, so the same pixels get the same answer back. That is
// right when the answer was right, and it is why re-ingesting a 200-page scan is
// free — but it means a bad read is permanent until somebody purges it, and
// nothing offered a way to. Observed: five documents re-indexed to fix
// watermark-only reads, four "completed" in twenty seconds, not one byte changed.
//
// Purge, then hand off to runIndex. Deliberately NOT a second ingest path — the
// point is to make the normal path do the work again, and a parallel pipeline
// here would be one more thing to keep in step with the real one.
//
// --suspect takes its targets from the transcription plausibility check instead
// of making somebody name them, which is the pairing that makes the check worth
// having.
func runReread(args []string) error {
	// Split our own flags out and pass everything else through to index, so
	// --home, --index, --model and friends keep working without being restated.
	var passthrough, targets []string
	suspectRoot, dry := "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run", a == "-dry-run":
			dry = true
		case strings.HasPrefix(a, "--suspect="), strings.HasPrefix(a, "-suspect="):
			suspectRoot = a[strings.Index(a, "=")+1:]
		case a == "--suspect", a == "-suspect":
			if i+1 < len(args) {
				i++
				suspectRoot = args[i]
			}
		case strings.HasPrefix(a, "-"):
			passthrough = append(passthrough, a)
			// A flag that takes a value: keep the value with it.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && strings.Contains(a, "=") == false {
				i++
				passthrough = append(passthrough, args[i])
			}
		default:
			targets = append(targets, a)
		}
	}

	if suspectRoot != "" {
		found, err := raglit.SuspectDocs(suspectRoot)
		if err != nil {
			return err
		}
		for doc, pages := range found {
			fmt.Printf("  page(s) %v look wrong: %s\n", pages, doc)
			targets = append(targets, doc)
		}
		if len(targets) == 0 {
			fmt.Println("no suspect transcriptions under", suspectRoot)
			return nil
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("reread: name a document, or pass --suspect DIR")
	}
	// Absolute, before anything is done with a target. A document is identified in
	// the index by its path, and a relative one names a different file for every
	// working directory — including the daemon's, which is not this process's. Sent
	// as-is it inserts a SECOND row for a file already indexed, and that row can
	// never be opened again: the daemon has no way to learn the cwd it was typed
	// in. --suspect makes this the common case, since it hands back whatever shape
	// the root it walked was written as.
	for i, t := range targets {
		abs, err := filepath.Abs(t)
		if err != nil {
			return fmt.Errorf("reread %s: %w", t, err)
		}
		targets[i] = abs
	}
	if dry {
		fmt.Printf("would re-read %d document(s)\n", len(targets))
		return nil
	}

	purge := flag.NewFlagSet("reread-purge", flag.ContinueOnError)
	purge.SetOutput(io.Discard)
	openStore, homeOf := addStoreFlags(purge)
	_ = purge.Parse(passthrough)

	// Rulings, for the copy announcement below. A project without any is the
	// normal state and must not stop a reread, so a failure here is not fatal.
	js, _ := openJudgements()
	if js != nil {
		defer js.Close()
	}

	// A reread WRITES: it purges cached pages and re-reads. On a daemon-routed
	// project both halves belong to the daemon — doing them locally would purge a
	// different index from the one in service and leave the bad read in place.
	if ns, err := resolveProject("", homeOf); err == nil && ns != "" && !explicitStoreFlag(purge) {
		read, rerr := openCorpus(purge, openStore, homeOf)
		if rerr != nil {
			return rerr
		}
		defer read.Close()
		for _, t := range targets {
			n, id, err := daemonReread(t)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
				continue
			}
			fmt.Printf("  purged %d cached page(s), queued job %d: %s\n", n, id, t)
			announceOtherCopies(read, js, t)
		}
		return nil
	}

	// Embedded/local: purge with a store opened only for that, then close it
	// before index opens its own — two writers on one sqlite file is a lock
	// fight for no gain.
	store, err := openStore()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, t := range targets {
		n, err := store.PurgeDocPageCache(ctx, t)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			continue
		}
		fmt.Printf("  purged %d cached page(s): %s\n", n, t)
		announceOtherCopies(store, js, t)
	}
	store.Close()

	return runIndex(append(passthrough, targets...))
}
