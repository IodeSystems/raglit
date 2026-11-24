package raglit

// The merged activity feed: ingest_jobs and identity_jobs as one timeline.
//
// Two tables, one question — "what is this index doing" — and two separate
// panes answering it half at a time. An index working through fifty captions
// looks idle on the jobs view because that view only sees ingest jobs; the
// identity queue, where the fifty are, has its own pane. Merging them into
// one ordered feed is the server-side change the Activity pane is waiting on
// (web/src/panes/Activity.tsx, docs/design/ui-redesign.md §6).
//
// Ordering is on enqueued_at, the one timestamp both tables carry in the same
// unit (Unix nanoseconds). id DESC is the tiebreaker: rows that share an
// enqueued_at (a batch) stay in a stable, newest-id-first order.

// ActivityKind is which of the two queues a feed row comes from. "ingest" is
// a row of ingest_jobs; the others are identity_jobs by mode, where mode is
// the job's ask (identityqueue.go): "caption" is the whole identity, "tags"
// the tags-only ask, "extract" the schema-extraction ask.
type ActivityKind string

const (
	ActivityIngest  ActivityKind = "ingest"
	ActivityCaption ActivityKind = "caption"
	ActivityTags    ActivityKind = "tags"
	ActivityExtract ActivityKind = "extract"
)

// activityKindForIdentity maps an identity_jobs.mode to its feed kind. An
// unknown mode — a row written before fields existed, or by a newer raglit —
// falls back to caption: the identity ask is the default, and a row that says
// something this build does not recognise should not vanish from the feed.
func activityKindForIdentity(mode string) ActivityKind {
	switch mode {
	case IdentityAskTags:
		return ActivityTags
	case IdentityAskFields:
		return ActivityExtract
	default:
		return ActivityCaption
	}
}

// ActivityItem is one row of the merged feed. The fields common to both
// queues are always set; the rest are set by the kind:
//
//   - ingest:   URL, Title, Lane, Fragments
//   - identity: Path, Mode, Force
type ActivityItem struct {
	ID         int64        `json:"id"`
	Kind       ActivityKind `json:"kind"`
	State      string       `json:"state"`
	Error      string       `json:"error,omitempty"`
	EnqueuedAt int64        `json:"enqueued_at"`
	StartedAt  int64        `json:"started_at,omitempty"`
	FinishedAt int64        `json:"finished_at,omitempty"`

	// Ingest rows.
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	Lane      string `json:"lane,omitempty"`
	Fragments int    `json:"fragments,omitempty"`

	// Identity rows. Mode is the ask as stored; Kind is its feed name.
	Path  string `json:"path,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Force bool   `json:"force,omitempty"`
}

// activityFilter is the WHERE clause for one table: "" excludes that table
// from the feed, and args is in the order it appears in the clause.
type activityFilter struct {
	where string
	args  []any
}

// activityFilters turns (kind, state) into a filter per table. kind picks the
// queue(s); state is appended to whichever table the kind allows.
func activityFilters(kind, state string) (ingest, identity activityFilter) {
	switch ActivityKind(kind) {
	case "", "all":
		ingest.where = " WHERE 1=1"
		identity.where = " WHERE 1=1"
	case ActivityIngest:
		ingest.where = " WHERE 1=1"
	case ActivityCaption:
		identity.where = " WHERE mode = ?"
		identity.args = append(identity.args, IdentityAskFull)
	case ActivityTags:
		identity.where = " WHERE mode = ?"
		identity.args = append(identity.args, IdentityAskTags)
	case ActivityExtract:
		identity.where = " WHERE mode = ?"
		identity.args = append(identity.args, IdentityAskFields)
	default:
		// An unknown kind matches neither table: an empty feed.
		return
	}
	if state != "" && state != "all" {
		if ingest.where != "" {
			ingest.where += " AND state = ?"
			ingest.args = append(ingest.args, state)
		}
		if identity.where != "" {
			identity.where += " AND state = ?"
			identity.args = append(identity.args, state)
		}
	}
	return
}

// Activity lists the two job tables as ONE feed, newest first by enqueued_at.
//
// kind filters to one queue (or one identity mode): "" or "all" returns both.
// state filters to one lifecycle state: "" or "all" returns every state. Both
// compose: kind=tags&state=pending is the tags jobs that are waiting, and
// only those. limit ≤ 0 → 100.
//
// The filters are pushed into the query rather than applied after the union:
// a LIMIT before a post-filter would return the limit's worth of the wrong
// kinds, and a feed of ten thousand rows is not a way to list the forty
// pending captions.
func (s *Store) Activity(kind, state string, limit int) ([]ActivityItem, error) {
	if limit <= 0 {
		limit = 100
	}
	ingest, identity := activityFilters(kind, state)
	if ingest.where == "" && identity.where == "" {
		return nil, nil
	}

	const ingestSel = `SELECT id, 1, url, title, state, error, lane, fragments, '', 0,
		            enqueued_at, started_at, finished_at FROM ingest_jobs`
	const identSel = `SELECT id, 2, path, '', state, error, '', 0, mode, force,
		            enqueued_at, started_at, finished_at FROM identity_jobs`

	var q string
	var args []any
	switch {
	case ingest.where != "" && identity.where != "":
		q = `SELECT * FROM (` +
			ingestSel + ingest.where + ` UNION ALL ` +
			identSel + identity.where +
			`) ORDER BY enqueued_at DESC, id DESC LIMIT ?`
		args = append(args, ingest.args...)
		args = append(args, identity.args...)
	case ingest.where != "":
		q = ingestSel + ingest.where +
			` ORDER BY enqueued_at DESC, id DESC LIMIT ?`
		args = append(args, ingest.args...)
	default:
		q = identSel + identity.where +
			` ORDER BY enqueued_at DESC, id DESC LIMIT ?`
		args = append(args, identity.args...)
	}
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActivityItem
	for rows.Next() {
		var it ActivityItem
		var src int
		var a, b, c string
		var fragments, force int
		if err := rows.Scan(&it.ID, &src, &a, &b, &it.State, &it.Error, &c, &fragments,
			&it.Mode, &force, &it.EnqueuedAt, &it.StartedAt, &it.FinishedAt); err != nil {
			return nil, err
		}
		if src == 1 {
			it.Kind = ActivityIngest
			it.URL, it.Title, it.Lane = a, b, c
			it.Fragments = fragments
		} else {
			it.Kind = activityKindForIdentity(it.Mode)
			it.Path = a
			it.Force = force != 0
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
