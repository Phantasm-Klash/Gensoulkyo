#!/usr/bin/env python3
"""End-to-end MVP smoke test for Phantasm Klash (codename gotouhou).

This script wires the *real* runtime together and proves the whole chain works:

    Go lobby (gensoulkyo)  <--WebSocket-->  2 test clients
          |
          | spawn (per match)
          v
    C++ battle server (phk_battle_server) --UDP--> KCP (not exercised here)
          |
          | POST /internal/battle/result
          v
    Go lobby  --MatchResultMessage--> 2 test clients

It is intentionally dependency-free: only the Python standard library is used
(no pip installs), plus the `go`, `cmake` and `ninja` toolchains that the repo
already requires.

What it asserts, step by step:

  1. the C++ battle server and the Go lobby binaries build;
  2. the lobby starts and serves HTTP;
  3. two hand-written RFC6455 WebSocket clients connect, authenticate,
     one creates a room and the other joins it with the room code;
  4. both clients receive a MatchStartMessage whose `endpoint` points at a real
     spawned UDP port and whose seed / signed battle ticket are non-empty;
  5. a `phk_battle_server` process exists and its UDP port is actually bound;
  6. a synthetic battle result POSTed to `/internal/battle/result` is accepted,
     both clients receive a MatchResultMessage with the winner and points, and a
     replayed POST is reported as an idempotent duplicate;
  7. the lobby and every spawned battle server are killed and the ports released.

Exit code is 0 only when every assertion passes.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import queue
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

# --------------------------------------------------------------------------- #
# Protocol constants (mirror runtime/lobbyws/protocol.go)
# --------------------------------------------------------------------------- #

WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

TYPE_AUTH_REQUEST = 1
TYPE_AUTH_RESPONSE = 2
TYPE_ROOM_CREATE_REQUEST = 5
TYPE_ROOM_CREATE_RESPONSE = 6
TYPE_ROOM_JOIN_REQUEST = 7
TYPE_ROOM_JOIN_RESPONSE = 8
TYPE_ROOM_STATE = 10
TYPE_MATCH_START = 11
TYPE_MATCH_RESULT = 12
TYPE_ERROR = 100

DEFAULT_LOBBY_PORT = 7399
DEFAULT_RULESET = "mvp-boss-race-s0"

BUILD_TIMEOUT = 300
GO_BUILD_TIMEOUT = 240
CMD_TIMEOUT = 30


# --------------------------------------------------------------------------- #
# Small reporting / assertion helpers
# --------------------------------------------------------------------------- #


class StepFailure(Exception):
    """Raised when an assertion for a named step fails."""


class Reporter:
    def __init__(self) -> None:
        self.entries = []
        self.failed = False

    def ok(self, name: str, detail: str = "") -> None:
        self.entries.append(("PASS", name, detail))
        line = f"[PASS] {name}"
        if detail:
            line += f"  | {detail}"
        print(line, flush=True)

    def fail(self, name: str, detail: str = "") -> None:
        self.entries.append(("FAIL", name, detail))
        self.failed = True
        line = f"[FAIL] {name}"
        if detail:
            line += f"  | {detail}"
        print(line, flush=True)

    def summary(self) -> str:
        passed = sum(1 for status, _, _ in self.entries if status == "PASS")
        failed = sum(1 for status, _, _ in self.entries if status == "FAIL")
        return f"{passed} passed, {failed} failed, {len(self.entries)} total"


def require(condition, message: str) -> None:
    if not condition:
        raise StepFailure(message)


def run_step(reporter: Reporter, name: str, fn):
    """Run one step, record PASS/FAIL and re-raise on failure."""
    try:
        detail = fn()
    except Exception as exc:  # noqa: BLE001 - we want the whole failure surfaced
        reporter.fail(name, f"{type(exc).__name__}: {exc}")
        raise
    reporter.ok(name, detail or "")
    return detail


def run_cmd(args, cwd=None, timeout=CMD_TIMEOUT, env=None, check=True):
    """Run a command, returning (returncode, combined stdout+stderr)."""
    proc = subprocess.run(
        args,
        cwd=cwd,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=timeout,
        text=True,
    )
    output = proc.stdout or ""
    if check and proc.returncode != 0:
        raise StepFailure(
            f"command failed (rc={proc.returncode}): {' '.join(args)}\n{output[-4000:]}"
        )
    return proc.returncode, output


# --------------------------------------------------------------------------- #
# Minimal RFC6455 WebSocket client (hand-written, stdlib only)
# --------------------------------------------------------------------------- #


class WSClient:
    """A tiny masked WebSocket client sufficient for the lobby JSON protocol."""

    def __init__(self, host: str, port: int, path: str = "/v1/lobby/ws", timeout: float = 15.0):
        self.host = host
        self.port = port
        self.history = []
        self._buf = b""
        self._send_lock = threading.Lock()
        self._q: "queue.Queue[dict]" = queue.Queue()
        self._closed = threading.Event()

        self.sock = socket.create_connection((host, port), timeout=timeout)
        self.sock.settimeout(timeout)
        key = base64.b64encode(os.urandom(16)).decode("ascii")
        request = (
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            "\r\n"
        )
        self.sock.sendall(request.encode("ascii"))
        self._read_handshake(key)

        self._thread = threading.Thread(target=self._reader, name="ws-reader", daemon=True)
        self._thread.start()

    # -- handshake ---------------------------------------------------------- #

    def _read_handshake(self, key: str) -> None:
        while b"\r\n\r\n" not in self._buf:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise StepFailure("connection closed during websocket handshake")
            self._buf += chunk
        head, _, rest = self._buf.partition(b"\r\n\r\n")
        self._buf = rest
        text = head.decode("latin-1")
        status_line = text.split("\r\n", 1)[0]
        require("101" in status_line, f"unexpected handshake response: {status_line!r}")
        expected = base64.b64encode(
            hashlib.sha1((key + WS_GUID).encode("ascii")).digest()
        ).decode("ascii")
        require(
            f"Sec-WebSocket-Accept: {expected}".lower() in text.lower(),
            "Sec-WebSocket-Accept mismatch",
        )

    # -- framing ------------------------------------------------------------ #

    def _recv_exact(self, count: int) -> bytes:
        while len(self._buf) < count:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise StepFailure("websocket connection closed by peer")
            self._buf += chunk
        data, self._buf = self._buf[:count], self._buf[count:]
        return data

    def _send_frame(self, opcode: int, payload: bytes) -> None:
        mask = os.urandom(4)
        length = len(payload)
        header = bytearray([0x80 | (opcode & 0x0F)])
        if length < 126:
            header.append(0x80 | length)
        elif length <= 0xFFFF:
            header.append(0x80 | 126)
            header += struct.pack(">H", length)
        else:
            header.append(0x80 | 127)
            header += struct.pack(">Q", length)
        header += mask
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        with self._send_lock:
            self.sock.sendall(bytes(header) + masked)

    def send_json(self, obj: dict) -> None:
        self._send_frame(0x1, json.dumps(obj, separators=(",", ":")).encode("utf-8"))

    def send_envelope(self, msg_type: int, payload: dict) -> None:
        self.send_json({"type": msg_type, "payload": payload})

    def _reader(self) -> None:
        try:
            while not self._closed.is_set():
                b0, b1 = self._recv_exact(2)
                opcode = b0 & 0x0F
                masked = bool(b1 & 0x80)
                length = b1 & 0x7F
                if length == 126:
                    length = struct.unpack(">H", self._recv_exact(2))[0]
                elif length == 127:
                    length = struct.unpack(">Q", self._recv_exact(8))[0]
                mask = self._recv_exact(4) if masked else b""
                payload = self._recv_exact(length) if length else b""
                if mask:
                    payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
                if opcode == 0x1:  # text
                    try:
                        self._q.put(json.loads(payload.decode("utf-8")))
                    except (ValueError, UnicodeDecodeError):
                        pass
                elif opcode == 0x9:  # ping -> pong
                    self._send_frame(0xA, payload)
                elif opcode == 0x8:  # close
                    break
        except Exception:  # noqa: BLE001 - reader dies quietly, main thread reports
            pass
        finally:
            self._closed.set()

    # -- receive ------------------------------------------------------------ #

    def recv_type(self, expected: int, timeout: float = 20.0) -> dict:
        """Wait for a message of `expected` type, skipping others (recorded)."""
        deadline = time.time() + timeout
        while True:
            remaining = deadline - time.time()
            if remaining <= 0:
                raise StepFailure(
                    f"timeout waiting for message type {expected}; received types "
                    f"{[m.get('type') for m in self.history]}"
                )
            try:
                message = self._q.get(timeout=remaining)
            except queue.Empty:
                raise StepFailure(
                    f"timeout waiting for message type {expected}; received types "
                    f"{[m.get('type') for m in self.history]}"
                )
            self.history.append(message)
            mtype = message.get("type")
            if mtype == TYPE_ERROR:
                raise StepFailure(f"lobby returned an error: {message.get('payload')}")
            if mtype == expected:
                return message

    def close(self) -> None:
        if self._closed.is_set():
            return
        try:
            self._send_frame(0x8, struct.pack(">H", 1000))
        except Exception:  # noqa: BLE001
            pass
        self._closed.set()
        try:
            self.sock.close()
        except OSError:
            pass


# --------------------------------------------------------------------------- #
# Host-level helpers (process / port inspection)
# --------------------------------------------------------------------------- #


def port_is_open(host: str, port: int, timeout: float = 1.0) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def wait_for_tcp(host: str, port: int, timeout: float = 15.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if port_is_open(host, port, timeout=0.5):
            return
        time.sleep(0.2)
    raise StepFailure(f"lobby did not start listening on {host}:{port} within {timeout}s")


def udp_listener_line(port: int) -> str:
    try:
        proc = subprocess.run(
            ["ss", "-ulnp"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            timeout=CMD_TIMEOUT, text=True,
        )
    except (OSError, subprocess.SubprocessError):
        return ""
    for line in (proc.stdout or "").splitlines():
        if re.search(rf":{port}\b", line):
            return line.strip()
    return ""


def battle_process_line(match_id: str) -> str:
    try:
        proc = subprocess.run(
            ["pgrep", "-af", "phk_battle_server"], stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, timeout=CMD_TIMEOUT, text=True,
        )
    except (OSError, subprocess.SubprocessError):
        return ""
    for line in (proc.stdout or "").splitlines():
        if "phk_battle_server" in line and match_id in line:
            return line.strip()
    return ""


def post_json(url: str, payload: dict, timeout: float = 10.0):
    data = json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(
        url, data=data, headers={"Content-Type": "application/json"}, method="POST"
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(body)
        except ValueError:
            parsed = {"raw": body}
        return exc.code, parsed


# --------------------------------------------------------------------------- #
# Main flow
# --------------------------------------------------------------------------- #


def parse_args(argv):
    script_dir = os.path.dirname(os.path.abspath(__file__))
    gensoulkyo_root = os.path.dirname(script_dir)
    repo_root = os.path.dirname(gensoulkyo_root)
    parser = argparse.ArgumentParser(description="Phantasm Klash end-to-end MVP smoke test")
    parser.add_argument("--gensoulkyo-root", default=gensoulkyo_root)
    parser.add_argument("--battle-dir", default=os.path.join(repo_root, "PhK-BattleServer"))
    parser.add_argument(
        "--battle-bin",
        default=os.path.join(repo_root, "PhK-BattleServer", "build-linux", "phk_battle_server"),
    )
    parser.add_argument("--lobby-port", type=int, default=DEFAULT_LOBBY_PORT)
    parser.add_argument("--ruleset", default=DEFAULT_RULESET)
    parser.add_argument("--skip-build", action="store_true", help="reuse existing binaries")
    parser.add_argument("--keep-logs", action="store_true", help="do not delete the temp log dir")
    return parser.parse_args(argv)


def build_cpp_battle_server(args, reporter: Reporter) -> str:
    def step():
        require(os.path.isdir(args.battle_dir), f"battle dir not found: {args.battle_dir}")
        build_dir = os.path.join(args.battle_dir, "build-linux")
        run_cmd(
            ["cmake", "-S", args.battle_dir, "-B", build_dir, "-G", "Ninja"],
            timeout=BUILD_TIMEOUT,
        )
        _, output = run_cmd(
            ["cmake", "--build", build_dir], timeout=BUILD_TIMEOUT
        )
        require(os.path.isfile(args.battle_bin), f"binary not produced: {args.battle_bin}")
        tail = output.strip().splitlines()[-1] if output.strip() else "built"
        return f"{args.battle_bin} ({tail})"

    return run_step(reporter, "build C++ battle server", step)


def build_go_lobby(args, tmp_dir: str, reporter: Reporter) -> str:
    binary = os.path.join(tmp_dir, "gensoulkyo")

    def step():
        env = dict(os.environ)
        env.setdefault("CGO_ENABLED", "0")
        run_cmd(
            ["go", "build", "-o", binary, "./cmd/gensoulkyo"],
            cwd=args.gensoulkyo_root,
            timeout=GO_BUILD_TIMEOUT,
            env=env,
        )
        require(os.path.isfile(binary), f"lobby binary not produced: {binary}")
        return binary

    return run_step(reporter, "build Go lobby", step)


def main(argv=None) -> int:
    args = parse_args(argv if argv is not None else sys.argv[1:])
    reporter = Reporter()
    host = "127.0.0.1"
    port = args.lobby_port
    lobby_proc = None
    lobby_holder = {"proc": None}
    clients = []
    match_id = None
    tmp_dir = tempfile.mkdtemp(prefix="phk_e2e_")
    lobby_log_path = os.path.join(tmp_dir, "lobby.log")

    print("=" * 72)
    print("Phantasm Klash end-to-end MVP smoke test")
    print(f"  gensoulkyo root : {args.gensoulkyo_root}")
    print(f"  battle binary   : {args.battle_bin}")
    print(f"  lobby endpoint  : {host}:{port}")
    print(f"  temp dir        : {tmp_dir}")
    print("=" * 72)

    def dump_lobby_log() -> None:
        print("-" * 72)
        print(f"lobby log ({lobby_log_path}):")
        try:
            with open(lobby_log_path, "r", encoding="utf-8", errors="replace") as handle:
                lines = handle.read().splitlines()
            for line in lines[-60:]:
                print(f"  | {line}")
        except OSError as exc:
            print(f"  (could not read log: {exc})")
        print("-" * 72)

    try:
        # --- 1. build ------------------------------------------------------ #
        if args.skip_build:
            reporter.ok("build C++ battle server", "skipped (--skip-build)")
            reporter.ok("build Go lobby", "skipped (--skip-build)")
            require(os.path.isfile(args.battle_bin), f"battle binary missing: {args.battle_bin}")
        else:
            build_cpp_battle_server(args, reporter)
            build_go_lobby(args, tmp_dir, reporter)

        lobby_bin = os.path.join(tmp_dir, "gensoulkyo")
        require(os.path.isfile(lobby_bin), f"lobby binary missing: {lobby_bin}")

        # --- 2. start lobby ------------------------------------------------ #
        def start_lobby():
            require(not port_is_open(host, port), f"port {port} is already in use")
            env = dict(os.environ)
            env["GENSOULKYO_BATTLE_SERVER_BIN"] = args.battle_bin
            env["GENSOULKYO_BATTLE_ADVERTISE_HOST"] = host
            env["GENSOULKYO_LOBBY_ENDPOINT"] = f"{host}:{port}"
            env["GENSOULKYO_BATTLE_RULESET"] = args.ruleset
            log_handle = open(lobby_log_path, "w", encoding="utf-8")
            proc = subprocess.Popen(
                [lobby_bin, "-addr", f"{host}:{port}"],
                stdout=log_handle,
                stderr=subprocess.STDOUT,
                env=env,
                cwd=args.gensoulkyo_root,
            )
            lobby_holder["proc"] = proc
            wait_for_tcp(host, port, timeout=15.0)
            return proc

        lobby_proc = run_step(reporter, "start lobby and serve HTTP", start_lobby)

        # --- 3. two WS clients: auth / create / join ----------------------- #
        def connect_and_auth():
            client_a = WSClient(host, port)
            client_b = WSClient(host, port)
            clients.extend([client_a, client_b])
            client_a.send_envelope(
                TYPE_AUTH_REQUEST,
                {"user_id": "e2e_user_a", "platform": "e2e", "client_build": "smoke"},
            )
            client_b.send_envelope(
                TYPE_AUTH_REQUEST,
                {"user_id": "e2e_user_b", "platform": "e2e", "client_build": "smoke"},
            )
            auth_a = client_a.recv_type(TYPE_AUTH_RESPONSE, timeout=10.0)
            auth_b = client_b.recv_type(TYPE_AUTH_RESPONSE, timeout=10.0)
            require(auth_a["payload"].get("user_id") == "e2e_user_a", "client A auth user mismatch")
            require(auth_b["payload"].get("user_id") == "e2e_user_b", "client B auth user mismatch")
            return "both clients authenticated"

        run_step(reporter, "two WS clients connect and authenticate", connect_and_auth)
        client_a, client_b = clients

        room_code_holder = {}

        def create_room():
            client_a.send_envelope(
                TYPE_ROOM_CREATE_REQUEST,
                {"mode_id": "certification", "host_user_id": "e2e_user_a", "loadout": {}},
            )
            response = client_a.recv_type(TYPE_ROOM_CREATE_RESPONSE, timeout=10.0)
            payload = response["payload"]
            require(not payload.get("error"), f"create room error: {payload.get('error')}")
            code = payload.get("room_code")
            require(bool(code), "room_code is empty")
            room_code_holder["code"] = code
            return f"room_code={code}"

        run_step(reporter, "client A creates a room", create_room)

        def join_room():
            client_b.send_envelope(
                TYPE_ROOM_JOIN_REQUEST,
                {"room_code": room_code_holder["code"], "user_id": "e2e_user_b", "loadout": {}},
            )
            response = client_b.recv_type(TYPE_ROOM_JOIN_RESPONSE, timeout=10.0)
            payload = response["payload"]
            require(not payload.get("error"), f"join room error: {payload.get('error')}")
            return f"joined {payload.get('room_code')}"

        run_step(reporter, "client B joins the room by code", join_room)

        # --- 4. both receive MatchStart ------------------------------------ #
        match_starts = {}

        def await_match_start():
            start_a = client_a.recv_type(TYPE_MATCH_START, timeout=40.0)
            start_b = client_b.recv_type(TYPE_MATCH_START, timeout=40.0)
            match_starts["a"] = start_a["payload"]
            match_starts["b"] = start_b["payload"]
            return f"match_id={start_a['payload'].get('match_id')}"

        run_step(reporter, "both clients receive MatchStartMessage", await_match_start)

        start_a = match_starts["a"]
        start_b = match_starts["b"]
        match_id = start_a.get("match_id")

        def check_match_start_fields():
            require(bool(match_id), "match_id is empty")
            require(start_a.get("match_id") == start_b.get("match_id"), "match_id mismatch between clients")
            endpoint = start_a.get("endpoint") or ""
            require(
                re.match(r"^[0-9A-Za-z_.-]+:\d+$", endpoint),
                f"endpoint is not host:port: {endpoint!r}",
            )
            require(start_b.get("endpoint") == endpoint, "endpoint mismatch between clients")
            seed_hex = start_a.get("server_seed_hex") or ""
            require(bool(seed_hex), "server_seed_hex is empty")
            require(int(seed_hex, 16) == start_a.get("server_seed"), "server_seed_hex != server_seed")
            ticket = start_a.get("signed_battle_ticket") or {}
            require(bool(ticket), "signed_battle_ticket is missing")
            require(bool(ticket.get("signature")), "signed_battle_ticket.signature is empty")
            require(
                (ticket.get("ticket") or {}).get("match_id") == match_id,
                "ticket.match_id mismatch",
            )
            require(
                (ticket.get("ticket") or {}).get("endpoint") == endpoint,
                "ticket.endpoint mismatch",
            )
            player_ids = start_a.get("player_ids") or []
            require(len(player_ids) == 2, f"expected 2 player_ids, got {player_ids}")
            require(set(player_ids) == set(start_b.get("player_ids") or []), "player_ids mismatch")
            return (
                f"endpoint={endpoint} seed_hex={seed_hex} "
                f"signature={ticket['signature'][:16]}... players={player_ids}"
            )

        run_step(reporter, "MatchStart fields (endpoint/seed/ticket)", check_match_start_fields)

        endpoint_host, endpoint_port = start_a["endpoint"].rsplit(":", 1)
        udp_port = int(endpoint_port)

        # --- 5. battle process + UDP port ---------------------------------- #
        def check_battle_process():
            line = battle_process_line(match_id)
            require(bool(line), f"no phk_battle_server process found for match {match_id}")
            return line

        run_step(reporter, "spawned battle server process exists", check_battle_process)

        def check_udp_listening():
            deadline = time.time() + 10.0
            line = ""
            while time.time() < deadline:
                line = udp_listener_line(udp_port)
                if line:
                    break
                time.sleep(0.2)
            require(bool(line), f"UDP port {udp_port} is not bound by any listener")
            return line

        run_step(reporter, "battle server UDP port is bound", check_udp_listening)

        def udp_probe():
            probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            probe.settimeout(1.0)
            try:
                probe.sendto(b"\x00", (endpoint_host, udp_port))
                try:
                    data, _ = probe.recvfrom(2048)
                    return f"sent 1 byte, got {len(data)} bytes back"
                except socket.timeout:
                    return "sent 1 byte (no reply expected from idle KCP server)"
            finally:
                probe.close()

        # Best-effort probe: its only failure mode would be an OSError.
        try:
            detail = udp_probe()
            reporter.ok("UDP probe datagram sent", detail)
        except OSError as exc:
            reporter.fail("UDP probe datagram sent", str(exc))

        # --- 6. synthetic result + broadcast + idempotency ----------------- #
        result_body = {
            "match_id": match_id,
            "mode_id": "certification",
            "ruleset_version": start_a.get("ruleset_version") or args.ruleset,
            "match_seed": start_a.get("server_seed") or 0,
            "winner_player_id": start_a["player_ids"][0],
            "winner_tick": 1234,
            "state_hash": "e2e-state-" + hashlib.sha256(match_id.encode()).hexdigest()[:16],
            "players": [
                {"player_id": pid, "damage_dealt": 100 - idx * 10, "boss_current_hp": idx * 50}
                for idx, pid in enumerate(start_a["player_ids"])
            ],
        }
        result_url = f"http://{host}:{port}/internal/battle/result"
        winner = result_body["winner_player_id"]
        loser = start_a["player_ids"][1]

        first_response = {}

        def post_result():
            status, body = post_json(result_url, result_body)
            require(status == 200, f"result POST returned HTTP {status}: {body}")
            require(body.get("ok") is True, f"result not accepted: {body}")
            require(body.get("duplicate") is False, f"first result wrongly flagged duplicate: {body}")
            require(body.get("winner_player_id") == winner, f"winner mismatch: {body}")
            first_response.update(body)
            return f"HTTP {status} ok winner={body.get('winner_player_id')} points={body.get('points')}"

        run_step(reporter, "synthetic result POST accepted", post_result)

        def check_match_result_broadcast():
            msg_a = client_a.recv_type(TYPE_MATCH_RESULT, timeout=15.0)
            msg_b = client_b.recv_type(TYPE_MATCH_RESULT, timeout=15.0)
            for label, message in (("A", msg_a), ("B", msg_b)):
                payload = message["payload"]
                require(payload.get("match_id") == match_id, f"client {label} match_id mismatch")
                require(
                    payload.get("winner_player_id") == winner,
                    f"client {label} winner mismatch: {payload.get('winner_player_id')}",
                )
                points = payload.get("points") or {}
                require(points.get(winner) == 3, f"client {label} winner points != 3: {points}")
                require(points.get(loser) == 1, f"client {label} loser points != 1: {points}")
            return f"winner={winner} points={{'{winner}': 3, '{loser}': 1}}"

        run_step(reporter, "both clients receive MatchResultMessage", check_match_result_broadcast)

        def check_idempotent():
            status, body = post_json(result_url, result_body)
            require(status == 200, f"duplicate POST returned HTTP {status}: {body}")
            require(body.get("duplicate") is True, f"replay not flagged duplicate: {body}")
            require(body.get("winner_player_id") == winner, f"duplicate winner mismatch: {body}")
            return "replay returned duplicate=true (no double payout)"

        run_step(reporter, "replayed result POST is idempotent", check_idempotent)

    except Exception:  # noqa: BLE001 - report, dump logs, clean up
        dump_lobby_log()
    finally:
        # --- 7. cleanup ---------------------------------------------------- #
        for client in clients:
            try:
                client.close()
            except Exception:  # noqa: BLE001
                pass

        if lobby_proc is None:
            lobby_proc = lobby_holder["proc"]
        if lobby_proc is not None and lobby_proc.poll() is None:
            lobby_proc.send_signal(signal.SIGTERM)
            try:
                lobby_proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                lobby_proc.kill()
                try:
                    lobby_proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass

        if match_id:
            subprocess.run(
                ["pkill", "-f", f"phk_battle_server.*{match_id}"],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
        # Any battle server the lobby failed to reap for this run's temp dir.
        time.sleep(0.5)

        try:
            if port_is_open(host, port, timeout=1.0):
                reporter.fail("lobby port released after shutdown", f"{host}:{port} still open")
            else:
                reporter.ok("lobby port released after shutdown", f"{host}:{port} closed")
        except Exception as exc:  # noqa: BLE001
            reporter.fail("lobby port released after shutdown", str(exc))

        if match_id:
            leftover = battle_process_line(match_id)
            if leftover:
                reporter.fail("battle server process reaped", leftover)
            else:
                reporter.ok("battle server process reaped", f"match {match_id} gone")

        if not args.keep_logs:
            shutil.rmtree(tmp_dir, ignore_errors=True)
        else:
            print(f"logs kept at {tmp_dir}")

    print("=" * 72)
    print(f"RESULT: {'FAIL' if reporter.failed else 'PASS'} ({reporter.summary()})")
    print("=" * 72)
    return 1 if reporter.failed else 0


if __name__ == "__main__":
    sys.exit(main())
