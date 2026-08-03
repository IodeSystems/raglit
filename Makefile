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

.PHONY: build install release test lint clean web web-dev

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
