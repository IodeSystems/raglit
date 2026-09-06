package main

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/iodesystems/raglit"
)

// Notes on a document: list, add, delete.
//
// A note is what a person knows that the machine could not read off the page.
// notes.go says why it is neither an identity nor an attestation; this file is
// only the wire.

type notesIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Path  string `query:"path" required:"true" doc:"document path as indexed"`
}

type notesOut struct {
	Body struct {
		Notes []raglit.Note `json:"notes"`
	}
}

func listNotesOp(reg *raglit.Registry) func(context.Context, *notesIn) (*notesOut, error) {
	return func(_ context.Context, in *notesIn) (*notesOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound("open index", err)
		}
		ns, err := st.Notes(in.Path)
		if err != nil {
			return nil, huma.Error404NotFound("list notes", err)
		}
		out := &notesOut{}
		out.Body.Notes = ns
		return out, nil
	}
}

type addNoteIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Path  string `query:"path" required:"true" doc:"document path as indexed"`
	Body  struct {
		Body string `json:"body" required:"true" doc:"the note"`
		// Author is self-declared and deliberately not taken from a session, for
		// the reason attest gives about rulings: whoever holds the link may not
		// be the account holder, and the record has to be able to say who
		// actually wrote it.
		Author string `json:"author,omitempty" doc:"who is saying this"`
		Page   int    `json:"page,omitempty" doc:"page this note is about; omit for the whole document"`
	}
}

type addNoteOut struct{ Body raglit.Note }

func addNoteOp(reg *raglit.Registry) func(context.Context, *addNoteIn) (*addNoteOut, error) {
	return func(_ context.Context, in *addNoteIn) (*addNoteOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound("open index", err)
		}
		n, err := st.AddNote(in.Path, raglit.Note{
			Body:   in.Body.Body,
			Author: in.Body.Author,
			Page:   in.Body.Page,
		})
		if err != nil {
			// An empty body and an unknown path are both the caller's mistake,
			// and both are 400 rather than 500: nothing is wrong with the index.
			return nil, huma.Error400BadRequest("add note", err)
		}
		return &addNoteOut{Body: n}, nil
	}
}

type deleteNoteIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	ID    int64  `query:"id" required:"true" doc:"note id"`
}

type deleteNoteOut struct {
	Body struct {
		Deleted bool `json:"deleted"`
	}
}

// POST rather than DELETE, matching the job actions beside it (/api/jobs/forget)
// rather than the branch route. The page posts JSON everywhere else and one
// operation needing a different verb is a papercut with nothing behind it.
func deleteNoteOp(reg *raglit.Registry) func(context.Context, *deleteNoteIn) (*deleteNoteOut, error) {
	return func(_ context.Context, in *deleteNoteIn) (*deleteNoteOut, error) {
		st, err := reg.Get(in.Index)
		if err != nil {
			return nil, huma.Error404NotFound("open index", err)
		}
		if err := st.DeleteNote(in.ID); err != nil {
			return nil, huma.Error500InternalServerError("delete note", err)
		}
		out := &deleteNoteOut{}
		out.Body.Deleted = true
		return out, nil
	}
}
