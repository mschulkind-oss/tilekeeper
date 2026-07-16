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
    # Swap the binary in via a same-directory temp file + rename(2), never
    # by writing through the destination path. The daemon normally IS
    # $dest/tilekeeper, and the kernel refuses to open a running
    # executable for writing — plain `cp` dies with ETXTBSY ("Text file
    # busy"). rename(2) replaces only the directory entry: the running
    # daemon keeps its old inode until it restarts, and anything exec'ing
    # $dest/tilekeeper concurrently sees either the old or the new binary,
    # never a half-written one. The temp file has to live in $dest —
    # rename(2) cannot cross filesystems.
    tmp="$(mktemp "$dest/.tilekeeper.XXXXXX")"
    trap 'rm -f "$tmp"' EXIT
    cp bin/tilekeeper "$tmp"
    chmod 755 "$tmp"   # mktemp creates 0600
    mv -f "$tmp" "$dest/tilekeeper"
    trap - EXIT
    echo "Installed to ${dest}/tilekeeper"

# Remove the binary and tear down the systemd user service
uninstall:
    #!/usr/bin/env bash
    set -euo pipefail
    # Tear the service down BEFORE removing the binary. install-service
    # enables the unit, so skipping this would leave systemd crash-looping
    # (Restart=on-failure) on a missing ExecStart at every login.
    if command -v systemctl >/dev/null 2>&1; then
        if systemctl --user is-enabled tilekeeper >/dev/null 2>&1 \
          || systemctl --user is-active tilekeeper >/dev/null 2>&1; then
            systemctl --user disable --now tilekeeper 2>/dev/null || true
            echo "Stopped and disabled tilekeeper.service"
        fi
        rm -f "${HOME}/.config/systemd/user/tilekeeper.service"
        systemctl --user daemon-reload 2>/dev/null || true
    fi
    dest="${GOBIN:-${HOME}/.local/bin}"
    rm -f "$dest/tilekeeper"
    echo "Removed ${dest}/tilekeeper"

# Write the systemd user unit, then reload + enable it (idempotent)
install-service: install
    #!/usr/bin/env bash
    set -euo pipefail
    # Writes the unit with ExecStart pointing at the installed binary
    # (not this repo's bin/), so the service survives moving or cleaning
    # the checkout.
    bin/tilekeeper install-service
    # A newly written unit is invisible to systemd — and so to `enable` —
    # until it re-reads the directory.
    systemctl --user daemon-reload
    # Idempotent, and what makes `just deploy` work on a fresh machine:
    # without it the first deploy leaves nothing to start at login. Announce
    # it only when it actually changes something — deploy runs this every
    # time, and a line that always prints stops being read.
    if ! systemctl --user is-enabled --quiet tilekeeper 2>/dev/null; then
        systemctl --user enable tilekeeper >/dev/null
        echo "Enabled tilekeeper.service"
    fi

# Restart the systemd service, picking up any unit-file changes first
restart-service:
    #!/usr/bin/env bash
    set -euo pipefail
    systemctl --user daemon-reload
    systemctl --user restart tilekeeper
    # The unit is Type=simple, so `restart` reports success the moment the
    # process is forked — a binary that dies on startup still looks like a
    # clean restart, and Restart=on-failure then quietly crash-loops it.
    # Give it a beat to fall over, then confirm it actually stayed up.
    sleep 0.5
    if ! systemctl --user is-active --quiet tilekeeper; then
        echo "ERROR: tilekeeper is not running after restart" >&2
        systemctl --user status tilekeeper --no-pager --lines=20 >&2 || true
        exit 1
    fi
    # `install` can now swap the binary while the daemon runs, so a restart
    # that silently didn't take is no longer caught by ETXTBSY the way it
    # used to be. A process still holding the replaced inode has its
    # /proc/<pid>/exe link marked "(deleted)" — i.e. the running code is NOT
    # what was just built, which is exactly what this check makes loud.
    # Match on the marker rather than comparing paths: /proc/<pid>/exe is
    # fully symlink-resolved, so a resolved ~/.local/bin would never compare
    # equal to the path install wrote to.
    pid="$(systemctl --user show -P MainPID tilekeeper)"
    if [ -n "$pid" ] && [ "$pid" != "0" ]; then
        exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
        case "$exe" in
            *" (deleted)")
                echo "ERROR: daemon (pid $pid) is still running a replaced binary:" >&2
                echo "       $exe" >&2
                echo "       The restart did not take — the new build is NOT live." >&2
                exit 1
                ;;
        esac
    fi
    echo "✓ tilekeeper active (pid ${pid})"

# Full rebuild and restart: build, install, write/refresh the unit, restart
deploy: install install-service restart-service
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
