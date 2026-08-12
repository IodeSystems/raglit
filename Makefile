# raglit — build/install with a source stamp.
#
# `make install` puts a SELF-UPDATING binary on your PATH: it carries the path of
# this tree, and rebuilds itself when the tree changed before running. Edit
# source, run `raglit`, get the new build. `RAGLIT_NO_AUTOBUILD=1` opts out per
# invocation.
#
# A plain `go install ./cmd/raglit` leaves srcDir empty and never self-updates —
# that is the release build, and it is the right default for anything shipped.
#
# The problem this exists to stop: a raglit twenty commits behind sat on PATH
# shadowing a current one and answered a real command with "unknown command".

SRCDIR  := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
LDFLAGS := -X main.srcDir=$(SRCDIR)
GOBIN   := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN   := $(shell go env GOPATH)/bin
endif

.PHONY: build install release test lint clean web web-dev generate

# Codegen runs OUR sqlc, never the one on PATH.
#
# The fork (../sqlc, fix/sqlite-rune-offsets) carries one patch: the sqlite
# engine reported RUNE offsets where the rest of sqlc indexes BYTES, so a single
# multibyte character anywhere in a query file shifts every rewrite after it.
# An em-dash in a comment is enough — a statement carries its leading comments,
# because that is how `-- name:` is found.
#
# Upstream v1.30.0 does not fail cleanly on this. It moved a trailing `= ?;` to
# the FRONT of two queries here and exited zero, and the damage surfaced as ~40
# tests failing with `SQL logic error: near "=": syntax error` — a message that
# describes the generated Go and says nothing about the comment that caused it.
# It also happily generates correct output for an all-ASCII file, so the trap
# only springs once somebody writes a comment with punctuation in it.
#
# So this target BUILDS the fork and refuses to fall back. A `sqlc generate`
# typed by hand picks up whatever is on PATH and will silently corrupt the tree;
# use `make generate`.
SQLC_SRC := $(abspath $(SRCDIR)/../sqlc)
PLUGIN_SRC := $(abspath $(SRCDIR)/../sqlc-go-codegen-metaquery)

bin/sqlc: $(SQLC_SRC)/go.mod
	@test -d "$(SQLC_SRC)" || { echo "missing sibling checkout $(SQLC_SRC) — see plan/daemon-ui.md"; exit 1; }
	@mkdir -p bin
	cd $(SQLC_SRC) && go build -o $(SRCDIR)/bin/sqlc ./cmd/sqlc
	@echo "built $(SRCDIR)/bin/sqlc ($$($(SRCDIR)/bin/sqlc version)) from $(SQLC_SRC)"

$(PLUGIN_SRC)/bin/sqlc-go-codegen-metaquery:
	$(MAKE) -C $(PLUGIN_SRC) bin/sqlc-go-codegen-metaquery

generate: bin/sqlc $(PLUGIN_SRC)/bin/sqlc-go-codegen-metaquery ## regenerate internal/db (uses ../sqlc, NOT the PATH sqlc)
	./bin/sqlc generate
	@echo "regenerated internal/db — run 'make test' before committing"

# The review UI is a React/vite app in web/, and web/dist is COMMITTED and
# //go:embed'd (web/embed.go says why). So `web` is not part of `build`: a Go
# build must keep working on a machine with no node, which is the whole reason
# the output is in the tree.
#
# Run it after changing anything under web/src, and commit what it writes. A dist
# that lags its source is a UI fix that does not appear, with a green build.
web: ## rebuild the embedded review UI (commit web/dist afterwards)
	cd web && npm ci && npm run build

web-dev: ## vite dev server against a running daemon (RAGLIT_DAEMON=url to point it)
	cd web && npm run dev

build: ## build ./raglit, source-stamped
	go build -ldflags "$(LDFLAGS)" -o raglit ./cmd/raglit

install: ## install a self-updating raglit to $(GOBIN)
	@mkdir -p $(GOBIN)
	go build -ldflags "$(LDFLAGS)" -o $(GOBIN)/raglit ./cmd/raglit
	@echo "installed $(GOBIN)/raglit (self-updating from $(SRCDIR))"

release: ## install WITHOUT the source stamp — never self-updates
	go install ./cmd/raglit

test:
	go test ./... -count=1

clean:
	rm -f raglit
