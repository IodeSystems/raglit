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

.PHONY: build install stamped release test lint clean web web-dev generate notices

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
	@test -d "$(SQLC_SRC)" || { echo "missing sibling checkout $(SQLC_SRC) — see docs/design/daemon-ui.md"; exit 1; }
	@mkdir -p bin
	cd $(SQLC_SRC) && go build -o $(SRCDIR)/bin/sqlc ./cmd/sqlc
	@echo "built $(SRCDIR)/bin/sqlc ($$($(SRCDIR)/bin/sqlc version)) from $(SQLC_SRC)"

$(PLUGIN_SRC)/bin/sqlc-go-codegen-metaquery:
	$(MAKE) -C $(PLUGIN_SRC) bin/sqlc-go-codegen-metaquery

generate: bin/sqlc $(PLUGIN_SRC)/bin/sqlc-go-codegen-metaquery ## regenerate internal/db (uses ../sqlc, NOT the PATH sqlc)
	./bin/sqlc generate
	@echo "regenerated internal/db — run 'make test' before committing"

# The review UI is a React/vite app in web/. web/dist is //go:embed'd; the real
# vite build is GENERATED (web/embed.go says why), and only a two-file
# PLACEHOLDER bundle is committed so a clean checkout compiles, serves a page,
# and passes the SPA tests without node.
#
# `web` is deliberately not a prerequisite of `build`/`install`: a Go-only change
# should not shell out to npm, and a Go build must keep working on a machine with
# no node. The committed placeholder is what that binary serves until someone
# runs `make web`; if dist/index.html were deleted entirely the binary would
# serve a page that SAYS the UI was not built — never a blank one.
#
# `release` DOES depend on it: a shipped binary with no UI is the one case where
# the honest page is not good enough.
web: ## rebuild the embedded review UI (replaces the committed placeholder in dist)
	cd web && npm ci && npm run build

web-dev: ## vite dev server against a running daemon (RAGLIT_DAEMON=url to point it)
	cd web && npm run dev

build: ## build ./raglit, source-stamped
	go build -ldflags "$(LDFLAGS)" -o raglit ./cmd/raglit

# ~/.local/bin is on PATH by default on most Linux desktops and is where the
# rest of the fleet installs; $(GOBIN) often is not. A SYMLINK, not a second
# copy: two copies drift apart (a self-update rewrites one), and a stale copy
# shadowing a current one is the problem this file exists to stop.
BINDIR ?= $(HOME)/.local/bin

# `stamped` builds a source-stamped binary at $(OUT) WITH the real UI: vite
# into web/dist, go build, then web/dist back to the committed placeholder so
# the tree stays clean. Without the UI step the binary embeds the placeholder
# page. Self-update (cmd/raglit/selfbuild.go) rebuilds through this target too,
# so a rebuilt binary keeps its UI.
OUT ?= $(GOBIN)/raglit

web/node_modules/.package-lock.json: web/package.json web/package-lock.json
	@command -v npm >/dev/null || { echo "npm not found — the UI build needs node (brew install node / apt-get install nodejs npm)"; exit 1; }
	cd web && npm ci
	@touch $@

stamped: web/node_modules/.package-lock.json
	cd web && npm run build
	@go build -ldflags "$(LDFLAGS)" -o $(OUT) ./cmd/raglit; s=$$?; \
		git checkout -- web/dist && git clean -fdq web/dist; exit $$s

install: ## install a self-updating raglit (with the UI) to $(GOBIN), linked from $(BINDIR)
	@mkdir -p $(GOBIN) $(BINDIR)
	@$(MAKE) --no-print-directory stamped OUT=$(GOBIN)/raglit
	@if [ "$(abspath $(BINDIR))" != "$(abspath $(GOBIN))" ]; then ln -sfn $(GOBIN)/raglit $(BINDIR)/raglit; echo "linked $(BINDIR)/raglit -> $(GOBIN)/raglit"; fi
	@echo "installed $(GOBIN)/raglit (self-updating from $(SRCDIR))"

# Every dependency a release ships must be under an allowed license, and the
# tarball carries their notices (tools/notices). Needs web/node_modules.
notices: web/node_modules/.package-lock.json ## check dependency licenses; write THIRD_PARTY_NOTICES.md
	go run ./tools/notices -o THIRD_PARTY_NOTICES.md

release: web ## install WITHOUT the source stamp — never self-updates (builds the UI first)
	go install ./cmd/raglit

test:
	go test ./... -count=1

clean:
	rm -f raglit
