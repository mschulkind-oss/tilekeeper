# tilekeeper Justfile

# Version info from git
version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
commit := `git rev-parse --short=12 HEAD 2>/dev/null || echo unknown`
buildTime := `date -u +%Y-%m-%dT%H:%M:%SZ`
treeState := `git diff --quiet 2>/dev/null && echo clean || echo dirty`
ldflags := "-X main.version=" + version + " -X main.commit=" + commit + " -X main.buildTime=" + buildTime + " -X main.treeState=" + treeState

default:
    @just --list

# Build the binary with version info
build:
    go build -ldflags "{{ldflags}}" -o bin/tilekeeper ./cmd/tilekeeper

# Run the binary
run *args:
    go run -ldflags "{{ldflags}}" ./cmd/tilekeeper {{args}}

# Run all quality checks (local dev — fixes formatting in place)
check: format lint test fuzz-gate

# Read-only quality gate (CI + pre-commit hook — verifies, never writes)
check-ci: lint-ci test fuzz-gate

# Install toolchain + dependencies (fresh clone)
setup:
    mise install
    go mod download

# Format code
format:
    go fmt ./...

# Lint code (vet + staticcheck)
lint:
    go vet ./...
    staticcheck ./...

# Lint (read-only): gofmt check (source dirs only), vet, staticcheck
lint-ci:
    test -z "$(gofmt -l cmd internal)" || (echo "unformatted:"; gofmt -l cmd internal; exit 1)
    go vet ./...
    staticcheck ./...

# Run tests
test *args:
    go test ./... {{args}}

# End-of-session gate: read-only checks + clean-tree assertion
done: check-ci
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -n "$(git status --porcelain)" ]; then
        echo "⚠ working tree is dirty — commit before ending the session:"
        git status --short
        exit 1
    fi
    echo "✓ checks pass, tree clean"

# Run tests with race detector
test-race:
    go test ./... -race -v

# Clean build artifacts
clean:
    rm -rf bin/

# Install to ~/.local/bin (or GOBIN)
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    dest="${GOBIN:-${HOME}/.local/bin}"
    mkdir -p "$dest"
    cp bin/tilekeeper "$dest/tilekeeper"
    echo "Installed to ${dest}/tilekeeper"

# Uninstall from ~/.local/bin (or GOBIN)
uninstall:
    #!/usr/bin/env bash
    set -euo pipefail
    dest="${GOBIN:-${HOME}/.local/bin}"
    rm -f "$dest/tilekeeper"
    echo "Removed ${dest}/tilekeeper"

# Install the systemd user service
install-service: install
    bin/tilekeeper install-service

# Restart the systemd service, picking up any unit-file changes first
restart-service:
    systemctl --user daemon-reload
    systemctl --user restart tilekeeper

# Full rebuild and restart: build, install, restart service
deploy: install restart-service
    @echo "tilekeeper deployed and service restarted"

# Show service status
status:
    systemctl --user status tilekeeper

# View service logs (follow mode)
logs:
    journalctl --user -u tilekeeper -f

# Check environment
doctor:
    go run -ldflags "{{ldflags}}" ./cmd/tilekeeper doctor

# Print version info
version:
    @echo "{{version}} ({{commit}})"

# Run integration tests with headless Sway (for CI / inside jail)
test-integration:
    #!/usr/bin/env bash
    set -e
    export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/xdg-runtime-$$}"
    mkdir -p "$XDG_RUNTIME_DIR"
    chmod 0700 "$XDG_RUNTIME_DIR"
    export WLR_BACKENDS=headless
    export WLR_LIBINPUT_NO_DEVICES=1
    export WAYLAND_DISPLAY=wayland-test

    # Start headless sway in background
    if command -v sway &>/dev/null; then
        sway -c /dev/null &
        SWAY_PID=$!
        sleep 2
        echo "Headless sway started (PID $SWAY_PID)"
        go test ./... -v -tags integration -run Integration || true
        kill $SWAY_PID 2>/dev/null || true
        rm -rf "$XDG_RUNTIME_DIR"
    else
        echo "sway not found — skipping integration tests"
        echo "Install sway to run integration tests"
    fi

# Run a specific test by pattern
test-match pattern:
    go test ./... -v -run "{{pattern}}"

# Fuzz — quick property-based smoke (500 steps, seed 1)
fuzz-smoke: build
    bin/tilekeeper harness fuzz --seed 1 --steps 500

# Fuzz — longer sweep (5 seeds x 1000 steps)
fuzz-sweep: build
    #!/usr/bin/env bash
    set -euo pipefail
    for seed in 1 2 3 4 5; do
        echo "--- seed=$seed ---"
        bin/tilekeeper harness fuzz --seed "$seed" --steps 1000 || exit 1
    done

# Fuzz — invariant gate: reference sweep, fails (exit 1) if any invariant
# exceeds its checked-in floor (internal/harness/fuzz/floors.json) or a new
# invariant class appears. The proactive CI signal; ~2.5s.
fuzz-gate:
    go run ./cmd/fuzz-gate

# Replay a daemon event capture (TK_EVENT_CAPTURE JSONL) through the fuzzer
# invariants; exit 1 on any violation. Turns a captured incident into a repro.
replay-capture capture:
    go run ./cmd/replay-journal {{capture}}

# Best-effort replay of a text journal (journalctl export) — lossy:
# geometry invariants are unreliable without a real capture.
replay-journal journal:
    go run ./cmd/replay-journal --journal {{journal}}

# Mine operation-distribution generator weights from a capture or journal.
replay-weights file:
    go run ./cmd/replay-journal --weights {{file}}

# Differential test: boot an OWN headless sway and validate the in-memory sim
# against real sway per command. Self-skips (exit 0) when no sway binary is on
# PATH. Install `sway`/`weston` so it exercises rather than skips.
sway-difftest:
    go run ./cmd/sway-difftest
