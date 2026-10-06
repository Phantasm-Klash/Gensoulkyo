#!/usr/bin/env python3
"""Lobby WebSocket protocol smoke test for Gensoulkyo.

This is a standalone, dependency-free smoke test (Python standard library only,
including a hand-written minimal RFC6455 client -- no ``pip install`` needed).

It connects to an already-running Gensoulkyo server and drives the real lobby
protocol end to end using the JSON envelope contract::

    {"type": <int>, "seq": <int>, "payload": {...}}

Flow::

    client A: AuthRequest(1)      -> AuthResponse(2)
              BootstrapRequest(3) -> BootstrapResponse(4)
              RoomCreateRequest(5)-> RoomCreateResponse(6) + RoomState(10)
    client B: AuthRequest(1)      -> AuthResponse(2)
              BootstrapRequest(3) -> BootstrapResponse(4)
              RoomJoinRequest(7)  -> RoomJoinResponse(8)
    both:     RoomState(10) and MatchStart(11)
              MatchStart must carry a non-empty endpoint, a server_seed and a
              signed battle ticket.

Usage::

    python3 tools/lobby_ws_smoke.py --host 127.0.0.1 --port 7351
    python3 tools/lobby_ws_smoke.py --wait          # wait for the port first

Exit code is 0 only when every assertion passes.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import queue
import socket
import struct
import sys
import threading
import time

WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

# Lobby message types (mirror runtime/lobbyws/protocol.go).
TYPE_AUTH_REQUEST = 1
TYPE_AUTH_RESPONSE = 2
TYPE_BOOTSTRAP_REQUEST = 3
TYPE_BOOTSTRAP_RESPONSE = 4
TYPE_ROOM_CREATE_REQUEST = 5
TYPE_ROOM_CREATE_RESPONSE = 6
TYPE_ROOM_JOIN_REQUEST = 7
TYPE_ROOM_JOIN_RESPONSE = 8
TYPE_ROOM_STATE = 10
TYPE_MATCH_START = 11
TYPE_ERROR = 100

TYPE_NAMES = {
    TYPE_AUTH_REQUEST: "AuthRequest",
    TYPE_AUTH_RESPONSE: "AuthResponse",
    TYPE_BOOTSTRAP_REQUEST: "BootstrapRequest",
    TYPE_BOOTSTRAP_RESPONSE: "BootstrapResponse",
    TYPE_ROOM_CREATE_REQUEST: "RoomCreateRequest",
    TYPE_ROOM_CREATE_RESPONSE: "RoomCreateResponse",
    TYPE_ROOM_JOIN_REQUEST: "RoomJoinRequest",
    TYPE_ROOM_JOIN_RESPONSE: "RoomJoinResponse",
    TYPE_ROOM_STATE: "RoomState",
    TYPE_MATCH_START: "MatchStart",
    TYPE_ERROR: "Error",
}


class SmokeFailure(Exception):
    """Raised when an assertion or transport step fails."""


class Reporter:
    """Collects PASS/FAIL results and prints them as the flow advances."""

    def __init__(self) -> None:
        self.passed = 0
        self.failed = 0
        self._lines: list[str] = []

    def ok(self, name: str, detail: str = "") -> None:
        self.passed += 1
        line = f"  PASS  {name}" + (f"  ({detail})" if detail else "")
        print(line, flush=True)
        self._lines.append(line)

    def fail(self, name: str, detail: str = "") -> None:
        self.failed += 1
        line = f"  FAIL  {name}" + (f"  ({detail})" if detail else "")
        print(line, flush=True)
        self._lines.append(line)

    def check(self, name: str, condition: bool, detail: str = "") -> None:
        if condition:
            self.ok(name, detail)
        else:
            self.fail(name, detail)

    def summary(self) -> str:
        return f"{self.passed} passed, {self.failed} failed"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SmokeFailure(message)


# --------------------------------------------------------------------------- #
# Minimal RFC6455 WebSocket client (hand-written, stdlib only)
# --------------------------------------------------------------------------- #


class WSClient:
    """A tiny masked WebSocket client sufficient for the lobby JSON protocol."""

    def __init__(self, host: str, port: int, path: str = "/v1/lobby/ws", timeout: float = 15.0):
        self.host = host
        self.port = port
        self.history: list[dict] = []
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
                raise SmokeFailure("connection closed during websocket handshake")
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
                raise SmokeFailure("websocket connection closed by peer")
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

    def send_envelope(self, msg_type: int, seq: int, payload: dict) -> None:
        self.send_json({"type": msg_type, "seq": seq, "payload": payload})

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
        """Wait for a message of ``expected`` type, skipping and recording others."""
        deadline = time.time() + timeout
        while True:
            remaining = deadline - time.time()
            if remaining <= 0:
                raise SmokeFailure(
                    f"timeout waiting for {TYPE_NAMES.get(expected, expected)}; "
                    f"received {self.seen_types()}"
                )
            try:
                message = self._q.get(timeout=remaining)
            except queue.Empty:
                raise SmokeFailure(
                    f"timeout waiting for {TYPE_NAMES.get(expected, expected)}; "
                    f"received {self.seen_types()}"
                )
            self.history.append(message)
            mtype = message.get("type")
            if mtype == TYPE_ERROR:
                raise SmokeFailure(f"lobby returned an error: {message.get('payload')}")
            if mtype == expected:
                return message

    def seen_types(self) -> list[str]:
        return [TYPE_NAMES.get(m.get("type"), m.get("type")) for m in self.history]

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
# Helpers
# --------------------------------------------------------------------------- #


def wait_for_tcp(host: str, port: int, timeout: float) -> None:
    deadline = time.time() + timeout
    last_error: Exception | None = None
    while time.time() < deadline:
        try:
            with socket.create_connection((host, port), timeout=2.0):
                return
        except OSError as exc:  # noqa: PERF203
            last_error = exc
            time.sleep(0.25)
    raise SmokeFailure(f"server {host}:{port} not reachable within {timeout:.0f}s: {last_error}")


def payload_of(message: dict) -> dict:
    payload = message.get("payload")
    require(isinstance(payload, dict), f"message payload must be an object: {payload!r}")
    return payload


def auth_client(client: WSClient, user_id: str, reporter: Reporter, label: str) -> str:
    client.send_envelope(
        TYPE_AUTH_REQUEST,
        1,
        {"session_token": "", "user_id": user_id, "platform": "smoke", "client_build": "smoke"},
    )
    auth = payload_of(client.recv_type(TYPE_AUTH_RESPONSE, 15))
    require(not auth.get("error"), f"{label} auth error: {auth.get('error')}")
    require(auth.get("user_id") == user_id, f"{label} auth user mismatch: {auth.get('user_id')!r}")
    require(auth.get("session_token"), f"{label} auth response missing session_token")
    reporter.ok(f"{label} AuthRequest(1) -> AuthResponse(2)", f"user_id={auth.get('user_id')}")

    client.send_envelope(TYPE_BOOTSTRAP_REQUEST, 2, {})
    boot = payload_of(client.recv_type(TYPE_BOOTSTRAP_RESPONSE, 15))
    require(not boot.get("error"), f"{label} bootstrap error: {boot.get('error')}")
    profile = boot.get("profile") or {}
    require(profile.get("user_id") == user_id, f"{label} bootstrap profile mismatch: {profile}")
    reporter.ok(f"{label} BootstrapRequest(3) -> BootstrapResponse(4)", f"ruleset={boot.get('ruleset_version')}")
    return user_id


def run_flow(host: str, port: int, path: str, timeout: float, reporter: Reporter) -> None:
    run_id = f"{int(time.time())}-{os.getpid()}"
    host_user = f"smoke-host-{run_id}"
    guest_user = f"smoke-guest-{run_id}"

    host_client = WSClient(host, port, path, timeout)
    guest_client = WSClient(host, port, path, timeout)
    try:
        # -- client A: auth + bootstrap ------------------------------------- #
        auth_client(host_client, host_user, reporter, "host")

        # -- client A: create room ------------------------------------------ #
        host_client.send_envelope(
            TYPE_ROOM_CREATE_REQUEST,
            3,
            {"mode_id": "certification", "room_code": "", "loadout": {"stage_id": "starlit_lanes", "character_id": "balanced"}},
        )
        created = payload_of(host_client.recv_type(TYPE_ROOM_CREATE_RESPONSE, 15))
        require(not created.get("error"), f"create room error: {created.get('error')}")
        room_code = created.get("room_code")
        require(room_code, f"create room response missing room_code: {created}")
        reporter.ok("host RoomCreateRequest(5) -> RoomCreateResponse(6)", f"room_code={room_code}")

        host_room = payload_of(host_client.recv_type(TYPE_ROOM_STATE, 10))
        reporter.check(
            "host RoomState(10) after create",
            host_room.get("room_code") == room_code and len(host_room.get("players") or []) == 1,
            f"room_code={host_room.get('room_code')} players={len(host_room.get('players') or [])}",
        )
        require(host_room.get("room_code") == room_code, "host room state room_code mismatch")
        require(len(host_room.get("players") or []) == 1, "host room state must have exactly one player")
        require(bool((host_room.get("players") or [{}])[0].get("host")), "creator must be flagged as host")

        # -- client B: auth + bootstrap ------------------------------------- #
        auth_client(guest_client, guest_user, reporter, "guest")

        # -- client B: join room -------------------------------------------- #
        guest_client.send_envelope(
            TYPE_ROOM_JOIN_REQUEST,
            3,
            {"room_code": room_code, "user_id": guest_user, "player_id": "", "loadout": {"stage_id": "starlit_lanes", "character_id": "balanced"}},
        )
        joined = payload_of(guest_client.recv_type(TYPE_ROOM_JOIN_RESPONSE, 15))
        require(not joined.get("error"), f"join room error: {joined.get('error')}")
        require(joined.get("room_code") == room_code, f"join room_code mismatch: {joined.get('room_code')}")
        reporter.ok("guest RoomJoinRequest(7) -> RoomJoinResponse(8)", f"room_code={joined.get('room_code')}")

        # -- both clients: RoomState(10) with two players ------------------- #
        host_room2 = payload_of(host_client.recv_type(TYPE_ROOM_STATE, 10))
        guest_room2 = payload_of(guest_client.recv_type(TYPE_ROOM_STATE, 10))
        reporter.check(
            "both clients receive RoomState(10) with 2 players",
            host_room2.get("room_code") == room_code
            and guest_room2.get("room_code") == room_code
            and len(host_room2.get("players") or []) == 2
            and len(guest_room2.get("players") or []) == 2,
            f"host={len(host_room2.get('players') or [])} guest={len(guest_room2.get('players') or [])}",
        )
        require(len(host_room2.get("players") or []) == 2, "host room state must show both players")
        require(len(guest_room2.get("players") or []) == 2, "guest room state must show both players")

        # -- both clients: MatchStart(11) ----------------------------------- #
        host_start = payload_of(host_client.recv_type(TYPE_MATCH_START, 30))
        guest_start = payload_of(guest_client.recv_type(TYPE_MATCH_START, 30))

        require(host_start.get("match_id"), "host MatchStart missing match_id")
        require(guest_start.get("match_id") == host_start.get("match_id"), "MatchStart match_id mismatch between clients")
        reporter.ok("both clients receive MatchStart(11)", f"match_id={host_start.get('match_id')}")

        for label, start in (("host", host_start), ("guest", guest_start)):
            require(start.get("endpoint"), f"{label} MatchStart missing endpoint")
            require(isinstance(start.get("server_seed"), int), f"{label} MatchStart missing server_seed")
            seed_hex = start.get("server_seed_hex")
            require(
                isinstance(seed_hex, str) and len(seed_hex) == 16,
                f"{label} MatchStart server_seed_hex invalid: {seed_hex!r}",
            )
            expected_hex = format(start["server_seed"] & 0xFFFFFFFFFFFFFFFF, "016x")
            require(seed_hex == expected_hex, f"{label} server_seed_hex {seed_hex} != expected {expected_hex}")
            ticket = start.get("signed_battle_ticket")
            require(isinstance(ticket, dict), f"{label} MatchStart missing signed_battle_ticket")
            require(ticket.get("signature"), f"{label} signed ticket missing signature")
            inner = ticket.get("ticket") or {}
            require(inner.get("match_id") == start.get("match_id"), f"{label} signed ticket match_id mismatch")
            require(inner.get("endpoint") == start.get("endpoint"), f"{label} signed ticket endpoint mismatch")

        reporter.check(
            "MatchStart(11) carries endpoint + server_seed + signed_battle_ticket",
            True,
            f"endpoint={host_start.get('endpoint')} seed={host_start.get('server_seed')} "
            f"alg={host_start['signed_battle_ticket'].get('signature_alg')}",
        )
        reporter.check(
            "MatchStart(11) player_ids has both players",
            len(host_start.get("player_ids") or []) == 2 and len(guest_start.get("player_ids") or []) == 2,
            f"player_ids={host_start.get('player_ids')}",
        )
    finally:
        host_client.close()
        guest_client.close()


def parse_args(argv):
    parser = argparse.ArgumentParser(description="Gensoulkyo lobby WebSocket protocol smoke test")
    parser.add_argument("--host", default=os.environ.get("GENSOULKYO_SMOKE_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("GENSOULKYO_SMOKE_PORT", "7350")))
    parser.add_argument("--path", default="/v1/lobby/ws")
    parser.add_argument("--timeout", type=float, default=15.0)
    parser.add_argument("--wait", type=float, default=0.0, help="seconds to wait for the TCP port before connecting")
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv if argv is not None else sys.argv[1:])
    reporter = Reporter()
    print(f"lobby WS smoke: ws://{args.host}:{args.port}{args.path}", flush=True)
    try:
        if args.wait > 0:
            wait_for_tcp(args.host, args.port, args.wait)
            reporter.ok("server port reachable", f"{args.host}:{args.port}")
        run_flow(args.host, args.port, args.path, args.timeout, reporter)
    except SmokeFailure as exc:
        reporter.fail("flow", str(exc))
    except OSError as exc:
        reporter.fail("transport", str(exc))

    print(f"lobby WS smoke result: {reporter.summary()}", flush=True)
    return 1 if reporter.failed else 0


if __name__ == "__main__":
    sys.exit(main())
