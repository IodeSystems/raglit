package main

import (
	"context"
	"encoding/base64"

	"github.com/danielgtaylor/huma/v2"

	"github.com/iodesystems/raglit"
)

// Ingest by content: the remote client's ingest. See raglit/upload.go. Owner
// write like every other write — no admin check, because nothing here touches
// a path the caller did not create.

// uploadMaxBytes bounds one request. Base64 inflates by 4/3, so this admits a
// document of about 48 MB.
const uploadMaxBytes = 64 << 20

type uploadIn struct {
	Index string `query:"index" doc:"index name (default: the default index)"`
	Body  struct {
		Name string `json:"name" required:"true" doc:"relative, slash-separated name; the document's identity in this index"`
		// Exactly one of Text or Data.
		Text  string `json:"text,omitempty" doc:"document content as text"`
		Data  string `json:"data,omitempty" doc:"document content, base64"`
		Title string `json:"title,omitempty"`
	}
}

type uploadOut struct {
	Body struct {
		Index string `json:"index"`
		Path  string `json:"path"`
		JobID int64  `json:"job_id"`
	}
}

func uploadOp(reg *raglit.Registry) func(context.Context, *uploadIn) (*uploadOut, error) {
	return func(ctx context.Context, in *uploadIn) (*uploadOut, error) {
		var data []byte
		switch {
		case in.Body.Text != "" && in.Body.Data != "":
			return nil, huma.Error400BadRequest("give text OR data, not both")
		case in.Body.Text != "":
			data = []byte(in.Body.Text)
		case in.Body.Data != "":
			raw, err := base64.StdEncoding.DecodeString(in.Body.Data)
			if err != nil {
				return nil, huma.Error400BadRequest("data is not valid base64", err)
			}
			data = raw
		default:
			return nil, huma.Error400BadRequest("text or data is required")
		}
		st, err := writeIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		path, err := st.SaveUpload(in.Body.Name, data)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		id, err := st.EnqueueFresh(raglit.UploadURL(path), in.Body.Title, false)
		if err != nil {
			return nil, huma.Error500InternalServerError("enqueue", err)
		}
		out := &uploadOut{}
		out.Body.Index, out.Body.Path, out.Body.JobID = defaultIndexName(in.Index), raglit.UploadURL(path), id
		return out, nil
	}
}
