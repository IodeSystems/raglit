package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// expandIngestTargets turns ingest args into a flat list of ingestable targets:
// a URL (file://, http(s)://) passes through; a local directory is walked for
// every file raglit can read (ClassifyDoc decides); a local file becomes its
// absolute path and is queued whatever it is. So `ingest ./repo` queues every
// readable file under repo.
func expandIngestTargets(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		if strings.Contains(a, "://") { // a URL
			out = append(out, a)
			continue
		}
		fi, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			err = filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				// ClassifyDoc is the single authority on what raglit can read.
				// This walk used to test isText||isPDF, which silently skipped
				// every office format and every image the extractor handles — a
				// directory of .docx or scanned .tif enqueued nothing and looked
				// like it had been covered. A format added to the extractor must
				// become discoverable in the same change, not the next one.
				// Skipped silently in a WALK: a directory ingest should not
				// report an error per generated file. Naming one explicitly still
				// gets the refusal, from Enqueue.
				if !d.IsDir() && !raglit.IsGeneratedSidecar(p) &&
					raglit.ClassifyDoc(p, "") != raglit.KindUnknown {
					abs, _ := filepath.Abs(p)
					out = append(out, abs)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			abs, _ := filepath.Abs(a)
			out = append(out, abs)
		}
	}
	return out, nil
}

// buildWorker wires a Worker with OCR (PDF) + LLM segmentation (text/code) when a
// model is configured; text-window sizing is resolved lazily on the first text
// job (probing + caching the model's context). No model → offline blank-line text
// + PDF-fails-gracefully.
func buildWorker(store *raglit.Store, lf *llmFlags, home raglit.Home, pool *raglit.Pool) *raglit.Worker {
	cfg, _, _ := raglit.LoadConfig(home)
	w := &raglit.Worker{Store: store}
	// Audio ingest turns on with the MODEL alone. oidio is normally behind the
	// same broker as the vision and embed models, so the endpoint is already
	// known; AudioBaseURL is only for the case where it is not.
	if cfg.AudioModel != "" {
		base, key, _ := cfg.Endpoint(cfg.AudioProvider)
		if cfg.AudioBaseURL != "" {
			base = cfg.AudioBaseURL
		}
		// Gated on the same channels as every other model. The daemon starts one
		// runner per lane slot and will offer several recordings at once; oidio
		// holds a single recogniser behind corrallm, and the width for it is
		// learned from its own 429s rather than assumed here.
		w.STT = &raglit.STT{BaseURL: base, APIKey: key, Model: cfg.AudioModel, Chan: lf.chans}
	}
	// Deterministic text fragmenter params (config-or-default), with the fragment
	// ceiling capped by the embed model's probed input limit.
	// The embed model's input limit is DISCOVERED, not configured: it is a fact
	// about the endpoint, and leaving it at zero means "no cap", which is how
	// fragments came to be sized by taste with nothing checking them.
	embedLimit := cfg.EmbedLimitChars
	embedLimitTokens, embedCounter := 0, raglit.TokenCounter(nil)
	if *lf.embedModel != "" {
		ec := lf.embedClientForProbe()
		embedLimit = store.EmbedLimitChars(context.Background(),
			raglit.NewEmbedder(ec, *lf.embedModel), cfg.EmbedLimitChars)
		// The character number above is a conversion; this is the limit itself.
		// A fragment inside the character ceiling can still be past the token one
		// — 16128 characters of a scanned brief is 10240 tokens against an 8192
		// window — and that refusal fails the document at the embed stage.
		embedLimitTokens = store.EmbedLimitTokens(context.Background(), ec,
			*lf.embedModel, cfg.EmbedLimitTokens)
		if ec.HasTokenizer(context.Background()) {
			embedCounter = ec
		}
		if embedLimitTokens > 0 {
			log.Printf("raglit: embed limit model=%s %d tokens (exact counting: %v)",
				*lf.embedModel, embedLimitTokens, embedCounter != nil)
		}
	}
	w.Frag = raglit.FragConfig{
		Window:            cfg.FragWindow,
		Stride:            cfg.FragStride,
		Floor:             cfg.FragFloor,
		EmbedLimit:        embedLimit,
		EmbedLimitTokens:  embedLimitTokens,
		EmbedTokenCounter: embedCounter,
		FigurePrompt:      raglit.FigurePromptVersion(),
	}
	cheapEngine := cfg.OCR.CheapEngine
	if *lf.visionModel != "" {
		client := lf.visionClient()
		// Every retry, recovery and give-up this client makes, attributed to the
		// job in flight. The client has always reported them; nothing was
		// listening, so a corpus took 244 upstream 5xx and 95 429s with no record
		// of any of it outside a sampled log.
		w.Retries = &raglit.RetryTally{}
		client.OnRetry = w.Retries.Observe
		// What the CHAT endpoint accepts as one request, discovered on the same
		// terms as the embed limit above and for the same reason: it is a fact
		// about the endpoint. Until this was probed, the fragments coming BACK
		// were capped by the embedder while the request carrying a page IN was
		// capped by nothing, and a page over the endpoint's physical batch failed
		// its whole document.
		w.Frag.SegmentInputLimit = store.ChatInputLimitTokens(
			context.Background(), client, *lf.visionModel, cfg.SegmentInputLimitTokens)
		// Counting is exact wherever the endpoint has a tokenizer. Without one,
		// sizes are estimated from a ratio learned per document — which is still
		// four times better than the constant it replaced, but is an estimate.
		if client.HasTokenizer(context.Background()) {
			w.Frag.TokenCounter = client
		}
		if w.Frag.SegmentInputLimit > 0 {
			log.Printf("raglit: chat input limit model=%s %d tokens (exact counting: %v)",
				*lf.visionModel, w.Frag.SegmentInputLimit, w.Frag.TokenCounter != nil)
		}
		// Printed once per index worker so the effective retry policy is visible
		// in the log. Without it, "5xx attempt 2/5" in a log is ambiguous between
		// "the cap was not raised" and "this is not the client you think it is".
		log.Printf("raglit: ocr client model=%s 5xx-attempts=%d", *lf.visionModel, client.Retry5xxAttempts)
		// Gated on the VISION model's own channel — and only it. This is the
		// change: transcription used to be serialised against segmentation and
		// embedding by one "the GPU admits one" slot in the scheduler, and those
		// are three different models on three different cards.
		w.OCR = raglit.NewOCR(lf.gate(client, *lf.visionModel))
		// Recorded on every page this reads, so a transcription says which model
		// produced it (page_readings.model) rather than only "machine".
		w.OCR.Model = *lf.visionModel
		// What the corpus owner says about reading THIS collection, carried into
		// every prompt that reads it. Read once here rather than per page: it is
		// a property of the index, and the worker is per-index.
		w.OCR.Collection = store.IndexHint()
		attachCheapOCR(w.OCR, home, "")
		// Only used when a page escalates to the VLM (llm-seg); text never does.
		// Its own client, because the segmenter's job is a structured tool call
		// over TEXT and the vision model is chosen for reading PIXELS.
		segClient := lf.segmentClient()
		// The tally is chained here too. It was only ever on the vision client,
		// so a segmentation that fought the endpoint for two minutes contributed
		// nothing to the job's retry record — and the 429s that must narrow the
		// segment model's channel were not being seen at all.
		segClient.OnRetry = w.Retries.Observe
		segModel := firstNonEmpty(*lf.segmentModel, *lf.visionModel)
		w.Segmenter = raglit.NewSegmenter(lf.gate(segClient, segModel))
		w.Segmenter.Collection = store.IndexHint()
		if *lf.segmentModel != "" && *lf.segmentModel != *lf.visionModel {
			log.Printf("raglit: segmenter model=%s (vision model=%s)", *lf.segmentModel, *lf.visionModel)
		}
	} else if ocr := tesseractOnlyOCR(home); ocr != nil {
		// No vision model (USER 2026-10-22: vision may be unavailable): scans and
		// images are read by the local engine alone. Without one, they fail as
		// unreadable, which is what w.OCR == nil already does.
		w.OCR = ocr
		cheapEngine = ocr.Cheap.Name()
		log.Printf("raglit: no vision model — scans and images are read by %s only", cheapEngine)
	}
	// What each ingested document IS, asked once per document (identity.go). Set
	// on the STORE rather than the worker because the identity call happens in
	// the commit path, which is the store's — and setting it here covers every
	// route into that path: each of the daemon's per-index workers.
	store.SetIdentifier(lf.identifier(home))
	// Cross-index pool (daemon only): key ingest work by (recipe, file). The
	// recipe is the models + config that shape the output — including the
	// fragmenter (§5) — so alt models OR a stride change reprocess.
	if pool != nil {
		w.Pool = pool
		fw, fs, ff := raglit.ResolveFragParams(cfg.FragWindow, cfg.FragStride, cfg.FragFloor, cfg.EmbedLimitChars)
		w.RecipeHash = raglit.HashHex([]byte(ingestRecipe(recipeInputs{
			VisionModel: *lf.visionModel, EmbedModel: *lf.embedModel,
			CheapEngine: cheapEngine,
			FragWindow:  fw, FragStride: fs, FragFloor: ff,
			IndexHint: store.IndexHint(),
		})))
	}
	return w
}

// recipeInputs is everything that shapes a document's INDEXED OUTPUT — the
// models, the fragmenter, and the corpus hint.
type recipeInputs struct {
	VisionModel string
	EmbedModel  string
	CheapEngine string
	FragWindow  int
	FragStride  int
	FragFloor   int
	// IndexHint is the corpus owner's account of how to read this collection.
	// It is IN the recipe because it changes the transcription and segmentation
	// prompts: a page read under one hint is not the same page read under
	// another. Left out, editing a hint would leave every already-pooled
	// document replaying the reading it got under the old one, and nothing
	// would say so — the pool would report a hit and the job would report done.
	IndexHint string
}

// ingestRecipe renders the pool key's inputs. Its own function so the thing
// that must not silently lose a term can be asserted on.
func ingestRecipe(in recipeInputs) string {
	return fmt.Sprintf("seg=%s|emb=%s|ocr=%s|frag=overlap,w=%d,s=%d,f=%d|fig=%d|hint=%s",
		in.VisionModel, in.EmbedModel, in.CheapEngine,
		in.FragWindow, in.FragStride, in.FragFloor,
		raglit.FigurePromptVersion(), raglit.HashHex([]byte(in.IndexHint)))
}

// runIngest queues targets on the daemon. With --wait it also waits until the
// index's queue is empty, then prints its status.
func runIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	homeOf, index := addHomeFlags(fs)
	client := addClientFlags(fs) // --daemon + --project
	title := fs.String("title", "", "document title (single-URL convenience)")
	wait := fs.Bool("wait", false, "wait until the daemon has worked the queue, then print status")
	fs.BoolVar(wait, "now", false, "old name of --wait")
	_ = fs.MarkHidden("now")
	fresh := fs.Bool("fresh", false, "re-read even if unchanged: skip the unchanged-bytes check AND the cross-index pool")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("ingest: nothing given (a folder, file, file://<path>, or http(s)://...)")
	}
	targets, err := expandIngestTargets(fs.Args())
	if err != nil {
		return err
	}
	dURL, ns, err := client(homeOf)
	if err != nil {
		return err
	}
	idx, err := nsWriteIndex(ns, projectShared(homeOf), resolveIndexName(*index, homeOf))
	if err != nil {
		return err
	}
	if err := daemonIngest(dURL, targets, idx, *title, *fresh, projectDirOf(homeOf())); err != nil {
		return err
	}
	if *wait {
		return waitForQueue(dURL, idx)
	}
	return nil
}

// waitForQueue polls the daemon until index has nothing pending or running,
// then prints its status. The daemon does the work; this only watches.
func waitForQueue(base, index string) error {
	for {
		b, err := daemonGet(base, "/status", url.Values{"index": {index}})
		if err != nil {
			return err
		}
		var st raglit.Status
		if err := json.Unmarshal(b, &st); err != nil {
			return err
		}
		if st.Pending == 0 && st.Running == 0 {
			renderStatus(st)
			return nil
		}
		time.Sleep(time.Second)
	}
}

// runWork waits until the daemon has worked the index's queue, then prints
// its status. It used to drain the queue in-process; the daemon is the only
// worker now (USER 2026-10-30: always a daemon).
func runWork(args []string) error {
	fs := flag.NewFlagSet("work", flag.ExitOnError)
	resolve := addTargetFlags(fs)
	fs.Parse(args)
	t, err := resolve()
	if err != nil {
		return err
	}
	return waitForQueue(t.base, t.index)
}

// runStatus prints the index + queue status.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	homeOf, index := addHomeFlags(fs)
	client := addClientFlags(fs) // --daemon + --project
	fs.Parse(args)
	dURL, ns, err := client(homeOf)
	if err != nil {
		// No project to resolve. Show what EXISTS instead of only naming a file to
		// edit — this is the command people run to find out where they are.
		return printIndexDirectory(err)
	}
	return daemonStatusPrint(dURL, nsReadSelector(ns, projectShared(homeOf), *index))
}

// renderStatus prints a Status value (shared by the local + daemon paths).
func renderStatus(st raglit.Status) {
	fmt.Printf("index: %d document(s), %d fragment(s)\n", st.Documents, st.Fragments)
	fmt.Printf("jobs:  done=%d running=%d pending=%d failed=%d", st.Done, st.Running, st.Pending, st.Failed)
	if st.RatePerMin > 0 {
		fmt.Printf("  (%.1f/min)", st.RatePerMin)
	}
	fmt.Println()
	if iq := st.Identity; !iq.Empty() || iq.Failed > 0 {
		fmt.Printf("names: %d pending, %d running, %d captioned, %d skipped, %d failed  (identify)\n",
			iq.Pending, iq.Running, iq.Done, iq.Skipped, iq.Failed)
	}
	if st.Prunable > 0 {
		fmt.Printf("prune: %d document(s) whose file is gone — out of search; remove with `raglit prune`\n", st.Prunable)
	}
	for _, it := range st.Items {
		eta := "eta n/a"
		if it.ETASeconds > 0 {
			eta = fmt.Sprintf("eta ~%.0fs", it.ETASeconds)
		}
		fmt.Printf("  %-8s #%d %s  (%s)\n", it.State, it.ID, it.URL, eta)
	}
}

// printIndexDirectory is what `raglit status` shows when it has no project to
// resolve — i.e. run anywhere outside a configured project.
//
// Refusing with "no project name — set project in ..." is correct and useless:
// it names a file to edit without saying what exists, so the indexes the daemon
// is holding, and their paths, are undiscoverable from the command whose whole
// job is to report state. A tool that knows the answer should say it.
func printIndexDirectory(reason error) error {
	root := raglit.DefaultRoot()
	fmt.Printf("not in a project — no index selected.\n")
	if reason != nil {
		fmt.Printf("  (%s)\n", firstSentence(reason.Error()))
	}
	fmt.Printf("\nstorage root: %s\n", root)

	// daemon.json records where a daemon WAS; it survives a kill, so believing it
	// would report a dead daemon as running. Probe.
	live := "" // the recorded daemon, when it answers; never auto-started here
	if st, recorded := readDaemonState(root); !recorded {
		fmt.Println("daemon:       not running")
	} else if base := "http://" + st.Addr; daemonHealthy(base) {
		live = base
		trustDaemon(base)
		fmt.Printf("daemon:       %s  (pid %d, since %s)\n", base, st.PID, st.StartedAt)
	} else {
		fmt.Printf("daemon:       not running  (stale %s records pid %d at %s)\n",
			filepath.Base(daemonStatePath(root)), st.PID, st.Addr)
	}

	// The indexes themselves, with counts from the daemon. With it down, the
	// names from disk and "?" for the counts: no command opens an index file.
	counts := map[string]idxRow{}
	if live != "" {
		var l struct {
			Indexes []idxRow `json:"indexes"`
		}
		if b, err := daemonGet(live, "/indexes", nil); err == nil && json.Unmarshal(b, &l) == nil {
			for _, r := range l.Indexes {
				counts[r.Name] = r
			}
		}
	}
	dir := filepath.Join(root, "indexes")
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		fmt.Printf("\nno indexes under %s\n", dir)
		fmt.Println("`raglit init` in a project directory creates one.")
		return nil
	}
	fmt.Printf("\n%-34s %-9s %-11s %s\n", "INDEX", "DOCS", "FRAGMENTS", "PATH")
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		docs, frags := "?", "?" // "?" is a claim about our access, 0 about the index
		if r, ok := counts[name]; ok {
			docs, frags = fmt.Sprint(r.Documents), fmt.Sprint(r.Fragments)
		}
		fmt.Printf("%-34s %-9s %-11s %s\n", name, docs, frags, filepath.Join(dir, name, "index.sqlite"))
	}
	fmt.Println("\nAn index is named <project>__<index>. To act on one:")
	fmt.Println("  cd into the project (its .raglit/config.json names it), or")
	fmt.Println("  raglit status --project <project> [--index <index>]")
	return nil
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// runRetry requeues failed ingest jobs.
//
// The case this exists for: a transient outage — the vision model unloaded
// during a restart — failed 72 PDFs in one window with "pdf ingest needs a
// vision model". Nothing surfaced `RetryJob`, so recovering meant reading URLs
// out of SQLite by hand and feeding them back to `ingest`.
//
// A vanished path is SKIPPED, not fatal. Failed rows outlive renames, and
// `ingest` aborts on the first missing file — one stale path in a batch of 122
// killed the whole retry. Reporting the skips is the point: after a rename
// sweep, "11 gone" is the answer, not an error.
func runRetry(args []string) error {
	fs := flag.NewFlagSet("retry", flag.ExitOnError)
	resolve := addTargetFlags(fs)
	dryRun := fs.Bool("dry-run", false, "list what would be requeued, change nothing")
	state := fs.String("state", "error", "which state to requeue: error|done")
	limit := fs.Int("limit", 0, "cap how many are requeued (0 = all)")
	match := fs.String("match", "", "only jobs whose error contains this substring")
	fs.Parse(args)
	t, err := resolve()
	if err != nil {
		return err
	}
	// Which index is this? Printed because its absence once hid that status
	// and retry were reading different indexes.
	fmt.Printf("index: %s\n", t.local())

	var jl struct {
		Jobs []raglit.JobInfo `json:"jobs"`
	}
	if err := t.get("/api/jobs", url.Values{"state": {*state}, "limit": {"100000"}}, &jl); err != nil {
		return err
	}
	var requeued, missing, filtered int
	for _, j := range jl.Jobs {
		if *match != "" && !strings.Contains(j.Error, *match) {
			filtered++
			continue
		}
		if p := localPath(j.URL); p != "" {
			if _, err := os.Stat(p); err != nil {
				missing++
				fmt.Printf("  gone     %s\n", p)
				continue
			}
		}
		if *limit > 0 && requeued >= *limit {
			break
		}
		if *dryRun {
			fmt.Printf("  would    #%d %s\n", j.ID, j.URL)
			requeued++
			continue
		}
		if err := t.post("/api/jobs/retry", map[string]any{"id": j.ID}, nil); err != nil {
			fmt.Fprintf(os.Stderr, "  failed   #%d: %v\n", j.ID, err)
			continue
		}
		requeued++
	}

	verb := "requeued"
	if *dryRun {
		verb = "would requeue"
	}
	fmt.Printf("%s %d · %d gone (skipped)", verb, requeued, missing)
	if filtered > 0 {
		fmt.Printf(" · %d filtered out by --match", filtered)
	}
	fmt.Println()
	if missing > 0 {
		fmt.Println("  gone = the row outlived the file, usually a rename. Nothing to do.")
	}
	return nil
}

// localPath returns the filesystem path a job URL refers to, or "" when the job
// is not a local file (http(s) targets have nothing to stat).
func localPath(u string) string {
	switch {
	case strings.HasPrefix(u, "file://"):
		return strings.TrimPrefix(u, "file://")
	case strings.HasPrefix(u, "/"):
		return u
	}
	return ""
}

// tesseractOnlyOCR is an OCR with no vision model: the configured cheap engine,
// else tesseract when it is on PATH. nil when neither exists. Every page is
// taken from the engine as read (OCR.PageAsSeen), and figures are not
// described, since describing one needs the vision model.
func tesseractOnlyOCR(home raglit.Home) *raglit.OCR {
	ocr := raglit.NewOCR(nil)
	attachCheapOCR(ocr, home, "")
	if ocr.Cheap == nil {
		cfg, _, _ := raglit.LoadConfig(home)
		t := raglit.NewTesseractEngine(cfg.OCR.TesseractBin, cfg.OCR.TesseractLang)
		if _, err := exec.LookPath(t.Bin); err != nil {
			return nil
		}
		ocr.Cheap = t
	}
	ocr.DescribeFigures, ocr.Assist = false, false
	return ocr
}
