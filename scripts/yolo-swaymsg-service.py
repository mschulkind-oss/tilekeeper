#!/usr/bin/env python3
"""yolo-swaymsg-service — read-only sway IPC bridge for yolo-jail.

Runs on the HOST. Listens on a Unix socket that gets bind-mounted into the
jail at /run/yolo-services/swaymsg.sock. Accepts JSON requests for a
hard-coded allowlist of sway IPC read-only message types, shells out to
`swaymsg -t <type> -r`, and streams stdout/stderr/exit back using the
same framed wire protocol as `yolo-journalctl`.

Safety invariants
-----------------

1. The `type` field is validated against READ_ONLY_TYPES. Anything else
   (including the empty string or the critical-looking `run_command`)
   is rejected before we touch swaymsg. There is no "fall through" to
   `swaymsg <free-form>` — the service cannot mutate sway state.

2. We never pass user-supplied strings as part of a shell command. The
   command vector is fixed: ["swaymsg", "-t", <validated_type>, "-r"]
   (plus `-m` and a validated event-list for subscribe).

3. For `subscribe`, the `events` field is also allowlisted, and we cap
   the wall-clock lifetime of the swaymsg process so a stuck client
   can't pin one subscription forever.

Wire protocol
-------------

Request (client → server): one line of JSON, e.g.
    {"type": "get_tree"}
    {"type": "subscribe", "events": ["window", "binding"], "seconds": 10}

Response (server → client): repeated frames, each:
    1 byte stream id  (1=stdout, 2=stderr, 3=exit)
    4 bytes big-endian payload length
    N bytes payload

The exit frame's payload is a 4-byte big-endian signed int (the exit code).

Usage
-----

Wire into yolo-jail.jsonc:

    "host_services": {
      "swaymsg": {
        "command": ["/path/to/yolo-swaymsg-service.py", "--socket", "{socket}",
                    "--swaysock", "/run/user/1000/sway-ipc..."]
      }
    }

If `--swaysock` is omitted the service uses $SWAYSOCK. When both are
missing, requests fail with a clear error instead of guessing.
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import socket
import struct
import subprocess
import sys
import threading
import time
from pathlib import Path
from typing import Iterable, List, Optional

# Frame types — identical to src/cli.py for operator familiarity.
FRAME_STDOUT = 1
FRAME_STDERR = 2
FRAME_EXIT = 3

# Sway IPC read-only message types. This list comes from sway-ipc(7) and is
# intentionally hard-coded rather than computed: new sway versions may add
# mutation verbs and we want a clean audit boundary, not "whatever swaymsg
# happens to accept today". Adding a type here is a conscious decision.
READ_ONLY_TYPES = frozenset({
    "get_workspaces",
    "get_tree",
    "get_outputs",
    "get_marks",
    "get_bar_config",
    "get_version",
    "get_binding_modes",
    "get_config",
    "get_seats",
    "get_inputs",
    "get_binding_state",
})

# Subscribable sway event types. These are *observations* — you can't
# change sway state by subscribing. No reason to deny them. See sway-ipc(7).
SUBSCRIBE_EVENTS = frozenset({
    "workspace",
    "mode",
    "window",
    "barconfig_update",
    "binding",
    "shutdown",
    "tick",
    "bar_state_update",
    "input",
})

# Cap subscribe lifetime so a misbehaving client can't leave a swaymsg
# subscribe running forever. 120 s is long enough for any reasonable
# debugging session; users who want longer can issue multiple requests.
SUBSCRIBE_MAX_SECONDS = 120

# How many frames/bytes we're willing to buffer from swaymsg at once.
# Sway trees can be large (megabytes on heavy sessions) but not unbounded.
READ_CHUNK = 64 * 1024

# Request body size cap. Real requests are well under 1 KiB.
MAX_REQUEST_BYTES = 8192


# ---------------------------------------------------------------------------
# Framed output helpers (identical to yolo-jail's journal bridge).
# ---------------------------------------------------------------------------


def send_frame(conn: socket.socket, stream: int, payload: bytes) -> None:
    header = struct.pack(">BI", stream, len(payload))
    try:
        conn.sendall(header + payload)
    except (BrokenPipeError, ConnectionResetError):
        pass


def send_exit(conn: socket.socket, code: int) -> None:
    send_frame(conn, FRAME_EXIT, struct.pack(">i", code))


def send_err(conn: socket.socket, msg: str, code: int = 2) -> None:
    if not msg.endswith("\n"):
        msg = msg + "\n"
    send_frame(conn, FRAME_STDERR, msg.encode("utf-8", errors="replace"))
    send_exit(conn, code)


# ---------------------------------------------------------------------------
# Request parsing and validation.
# ---------------------------------------------------------------------------


def read_request_line(conn: socket.socket) -> Optional[bytes]:
    """Read up to MAX_REQUEST_BYTES ending at a newline."""
    buf = bytearray()
    while b"\n" not in buf:
        if len(buf) >= MAX_REQUEST_BYTES:
            return None
        chunk = conn.recv(min(READ_CHUNK, MAX_REQUEST_BYTES - len(buf)))
        if not chunk:
            return bytes(buf) or None
        buf.extend(chunk)
    return bytes(buf)


def _validate_type(t: object) -> Optional[str]:
    if not isinstance(t, str) or t not in READ_ONLY_TYPES:
        return None
    return t


def _validate_events(events: object) -> Optional[List[str]]:
    if not isinstance(events, list) or not events:
        return None
    out: List[str] = []
    for e in events:
        if not isinstance(e, str) or e not in SUBSCRIBE_EVENTS:
            return None
        out.append(e)
    return out


def _validate_seconds(seconds: object, default: int = 10) -> int:
    if seconds is None:
        return default
    if isinstance(seconds, bool):
        return default  # JSON `true`/`false` would be accepted as int otherwise.
    if not isinstance(seconds, int) or seconds <= 0:
        return -1
    return min(seconds, SUBSCRIBE_MAX_SECONDS)


# ---------------------------------------------------------------------------
# Handlers.
# ---------------------------------------------------------------------------


def handle_query(
    conn: socket.socket,
    msg_type: str,
    swaysock: Optional[str],
    log,
) -> None:
    cmd = ["swaymsg", "-r", "-t", msg_type]
    env = dict(os.environ)
    if swaysock:
        env["SWAYSOCK"] = swaysock
    _log(log, f"[query] type={msg_type} swaysock={swaysock or env.get('SWAYSOCK', '?')}")

    try:
        proc = subprocess.Popen(
            cmd,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except FileNotFoundError:
        send_err(conn, "yolo-swaymsg: swaymsg not found on host", 127)
        return
    except OSError as e:
        send_err(conn, f"yolo-swaymsg: spawn failed: {e}", 1)
        return

    _stream_proc(conn, proc)


def handle_subscribe(
    conn: socket.socket,
    events: List[str],
    seconds: int,
    swaysock: Optional[str],
    log,
) -> None:
    cmd = ["swaymsg", "-m", "-t", "subscribe", json.dumps(events)]
    env = dict(os.environ)
    if swaysock:
        env["SWAYSOCK"] = swaysock
    _log(log, f"[subscribe] events={events} seconds={seconds}")

    try:
        proc = subprocess.Popen(
            cmd,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except FileNotFoundError:
        send_err(conn, "yolo-swaymsg: swaymsg not found on host", 127)
        return
    except OSError as e:
        send_err(conn, f"yolo-swaymsg: spawn failed: {e}", 1)
        return

    # Kill the subscribe after `seconds` — swaymsg -m runs forever otherwise.
    timer = threading.Timer(seconds, _sigterm_group, args=(proc,))
    timer.daemon = True
    timer.start()
    try:
        _stream_proc(conn, proc)
    finally:
        timer.cancel()


def _stream_proc(conn: socket.socket, proc: subprocess.Popen) -> None:
    """Relay the process's stdout/stderr to the client as framed output."""

    def pump(stream, frame_type):
        if stream is None:
            return
        try:
            while True:
                chunk = stream.read(READ_CHUNK)
                if not chunk:
                    break
                send_frame(conn, frame_type, chunk)
        except Exception:
            pass

    t_out = threading.Thread(target=pump, args=(proc.stdout, FRAME_STDOUT), daemon=True)
    t_err = threading.Thread(target=pump, args=(proc.stderr, FRAME_STDERR), daemon=True)
    t_out.start()
    t_err.start()

    rc = proc.wait()
    t_out.join(timeout=2)
    t_err.join(timeout=2)
    send_exit(conn, rc)


def _sigterm_group(proc: subprocess.Popen) -> None:
    """Send SIGTERM to the child's whole process group; SIGKILL after 2s."""
    try:
        os.killpg(proc.pid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        return
    try:
        proc.wait(timeout=2)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass


# ---------------------------------------------------------------------------
# Connection dispatch.
# ---------------------------------------------------------------------------


def handle_client(conn: socket.socket, swaysock: Optional[str], log) -> None:
    try:
        raw = read_request_line(conn)
        if raw is None:
            send_err(conn, "yolo-swaymsg: request too large or empty")
            return

        try:
            req = json.loads(raw.decode("utf-8", errors="replace").strip() or "{}")
        except ValueError as e:
            send_err(conn, f"yolo-swaymsg: invalid JSON: {e}")
            return

        if not isinstance(req, dict):
            send_err(conn, "yolo-swaymsg: request must be a JSON object")
            return

        action = req.get("type")
        if action == "subscribe":
            events = _validate_events(req.get("events"))
            if events is None:
                send_err(
                    conn,
                    "yolo-swaymsg: subscribe requires non-empty 'events' "
                    f"allowlist subset of {sorted(SUBSCRIBE_EVENTS)}",
                )
                return
            seconds = _validate_seconds(req.get("seconds"))
            if seconds <= 0:
                send_err(conn, "yolo-swaymsg: 'seconds' must be a positive integer")
                return
            handle_subscribe(conn, events, seconds, swaysock, log)
            return

        mt = _validate_type(action)
        if mt is None:
            send_err(
                conn,
                "yolo-swaymsg: 'type' must be one of "
                f"{sorted(READ_ONLY_TYPES)} or 'subscribe'. "
                f"Got {action!r}. Mutating commands are not supported.",
            )
            return

        handle_query(conn, mt, swaysock, log)
    except Exception as e:
        # Last-ditch error reporting — never let the service crash.
        try:
            send_err(conn, f"yolo-swaymsg: internal error: {e!r}", 1)
        except Exception:
            pass
    finally:
        try:
            conn.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            conn.close()
        except OSError:
            pass


# ---------------------------------------------------------------------------
# Service entry point.
# ---------------------------------------------------------------------------


def _log(log, msg: str) -> None:
    if log is None:
        return
    try:
        log.write(f"{time.strftime('%Y-%m-%dT%H:%M:%S')} {msg}\n")
        log.flush()
    except Exception:
        pass


def _open_log(path: Optional[Path]):
    if path is None:
        return None
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        return open(path, "a", buffering=1, encoding="utf-8")
    except OSError as e:
        print(f"yolo-swaymsg: cannot open log {path}: {e}", file=sys.stderr)
        return None


def serve(sock_path: str, swaysock: Optional[str], log_path: Optional[Path]) -> int:
    # Bind before we do anything else so the jail sees the socket quickly
    # (yolo-jail times out after 5 s).
    try:
        os.unlink(sock_path)
    except FileNotFoundError:
        pass
    except OSError as e:
        print(f"yolo-swaymsg: cannot clear stale socket {sock_path}: {e}", file=sys.stderr)
        return 1

    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    # The socket is bind-mounted into the jail root; tight perms keep other
    # host users off it. Inside the jail it's accessible to the jail user.
    old_umask = os.umask(0o177)
    try:
        srv.bind(sock_path)
    except OSError as e:
        print(f"yolo-swaymsg: bind({sock_path}): {e}", file=sys.stderr)
        return 1
    finally:
        os.umask(old_umask)
    srv.listen(16)

    log = _open_log(log_path)
    _log(log, f"[start] socket={sock_path} swaysock={swaysock or os.environ.get('SWAYSOCK')}")

    # Graceful shutdown on SIGTERM/SIGINT.
    stop = threading.Event()

    def _on_signal(*args):
        del args
        stop.set()
        try:
            srv.close()
        except OSError:
            pass

    signal.signal(signal.SIGTERM, _on_signal)
    signal.signal(signal.SIGINT, _on_signal)

    try:
        while not stop.is_set():
            try:
                conn, _ = srv.accept()
            except OSError:
                break
            t = threading.Thread(
                target=handle_client,
                args=(conn, swaysock, log),
                daemon=True,
            )
            t.start()
    finally:
        _log(log, "[stop]")
        if log is not None:
            try:
                log.close()
            except Exception:
                pass
        try:
            os.unlink(sock_path)
        except OSError:
            pass
    return 0


def main(argv: Optional[Iterable[str]] = None) -> int:
    ap = argparse.ArgumentParser(
        prog="yolo-swaymsg-service",
        description="Read-only swaymsg bridge for yolo-jail.",
    )
    ap.add_argument("--socket", required=True, help="Unix socket path to bind")
    ap.add_argument(
        "--swaysock",
        default=None,
        help="Path to sway's IPC socket (defaults to $SWAYSOCK)",
    )
    ap.add_argument(
        "--log",
        default=str(Path.home() / ".local/share/yolo-jail/logs/host-service-swaymsg.log"),
        help="Log file path (set to empty string to disable)",
    )
    args = ap.parse_args(list(argv) if argv is not None else None)

    log_path: Optional[Path] = Path(args.log) if args.log else None
    return serve(args.socket, args.swaysock, log_path)


if __name__ == "__main__":
    sys.exit(main())
