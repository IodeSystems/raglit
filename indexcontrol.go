package raglit

import "time"

// Operator controls on one index: pause, ignore patterns, purge by pattern.
//
// USER 2026-10-22: "we desperately need a pause index … I'm burning money on
// node_modules." A daemon spending model calls on the wrong files had no stop
// short of killing it, which stops every index.

const metaPaused = "paused"

// Paused reports whether this index runs no work. Jobs still queue while it is
// paused, and run when it is resumed.
func (s *Store) Paused() bool {
	v, _ := s.Meta(metaPaused)
	return v == "1"
}

// SetPaused pauses or resumes this index. Stored in the index, so a restart
// keeps it. A job already running finishes; nothing new is claimed.
func (s *Store) SetPaused(paused bool) error {
	v := ""
	if paused {
		v = "1"
	}
	return s.SetMeta(metaPaused, v, time.Now().Unix())
}
