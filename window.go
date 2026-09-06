package raglit

import "context"

// ingestText fragments a text/code document deterministically (fragment.go): a
// text file never escalates to the VLM, so it is always the text-overlap path and
// needs no model and no OCR — the whole file is one pageless (page 0) unit and the
// overlapping windower handles arbitrary length.
func (s *Store) ingestText(ctx context.Context, docPath, title, text string, fc FragConfig, sl *StageLog) (int, string, error) {
	units := []ingestUnit{{page: 0, text: text}}
	return s.ingestUnits(ctx, nil, nil, docPath, title, units, fc, sl)
}
