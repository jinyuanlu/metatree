# metatree (mt) — task runner. Install: brew install just

# show recipes when run without an argument
default:
    @just --list

# end-to-end integration test — the load-bearing one (~3s, no extra deps)
test:
    bash tests/smoke.sh

# pure-function unit tests (requires: brew install bats-core)
test-units:
    @command -v bats >/dev/null || { echo "bats not installed: brew install bats-core" >&2; exit 1; }
    bats tests/mt.bats

# run smoke + units
test-all: test test-units

# bash syntax check on every shipped script (no extra deps)
check:
    bash -n mt.sh
    bash -n install.sh
    bash -n tests/smoke.sh
    bash -n bin/mt
    bash -n bin/mt-test
    bash -n bin/mt-bats
    @echo "syntax: OK"

# shellcheck (requires: brew install shellcheck) — bash-side lint
lint-sh:
    @command -v shellcheck >/dev/null || { echo "shellcheck not installed: brew install shellcheck" >&2; exit 1; }
    shellcheck mt.sh install.sh tests/smoke.sh bin/mt bin/mt-test bin/mt-bats

# build the Go binary into ./bin/mt-go
build:
    go build -o ./bin/mt-go ./cmd/mt

# Go unit + integration tests with race detector
test-go:
    go test -race ./...

# Go lint: gofmt (must be clean) + go vet
lint:
    go vet ./...
    @gofmt -l . | grep -q . && exit 1 || exit 0

# install local mt.sh → ~/.local/bin/mt (dev shortcut, no curl)
install:
    install -m 755 mt.sh ~/.local/bin/mt
    @echo "installed: ~/.local/bin/mt"
    @echo "verify:    mt --help"

# remove leftover smoke fixtures from /tmp (rare cleanup)
clean:
    rm -rf /tmp/mt-smoke.* /tmp/mt-repro.* /tmp/mt-repro2.* /tmp/mt-repro3.* /tmp/mt-verify.* /tmp/mt-debug-show.* /tmp/mt-cmptest.*
    @echo "cleaned /tmp/mt-* fixtures"
