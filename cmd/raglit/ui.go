package main

import _ "embed"

// reviewHTML is the self-contained review UI served at the daemon's / — status,
// job control (retry/cancel), the document list, and per-document OCR review
// (page image + engine badge + indexed text, with on-demand cascade re-OCR).
// One file, no external assets, so it works over a bare localhost daemon.
//
//go:embed ui.html
var reviewHTML []byte
