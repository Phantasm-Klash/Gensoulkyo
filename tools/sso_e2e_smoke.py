#!/usr/bin/env python3
"""Single-sign-on (session takeover) end-to-end test for Gensoulkyo.

Companion to ``lobby_ws_smoke.py``: it reuses that module's hand-written
RFC6455 client and drives the *real* lobby over a real socket to prove that a
second sign-in for the same identity kicks the first one off the wire.

Why this exists
---------------
``core.Service.LoginExternal`` has always replaced the previous session token,
but the superseded client used to be left holding a dead token with no notice:
its next request simply failed, which is the "multi-device login bug". The fix
pushes ``TypeError(100)`` with ``code == "session_replaced"`` to the old socket
and then closes it. This script asserts that contract against a live server.

Flow::

    A: AuthRequest(user_id=U)          -> AuthResponse(2)   session T_A
    A: BootstrapRequest(3)             -> BootstrapResponse(4)      (T_A valid)
    B: AuthRequest(user_id=U)          -> AuthResponse(2)   session T_B
       A must now receive TypeError(100) code="session_replaced"
       A's socket must then be closed by the server
    B: BootstrapRequest(3)             -> BootstrapResponse(4)      (T_B valid)
    D: AuthRequest(session_token=T_A)  -> AuthResponse(2) with error "unauthorized"
       (proves the superseded token no longer resolves)

Usage::

    python3 tools/sso_e2e_smoke.py --host 127.0.0.1 --port 7350

Exit code is 0 only when every assertion passes.
"""

from __future__ import annotations

import argparse
import os
import queue
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from lobby_ws_smoke import (  # noqa: E402
    TYPE_AUTH_REQUEST,
    TYPE_AUTH_RESPONSE,
    TYPE_BOOTSTRAP_REQUEST,
    TYPE_BOOTSTRAP_RESPONSE,
    TYPE_ERROR,
    Reporter,
    SmokeFailure,
    WSClient,
    payload_of,
    require,
    wait_for_tcp,
)

SESSION_REPLACED_CODE = "session_replaced"


def recv_any(client: WSClient, timeout: float) -> dict:
    """Pull the next text message, recording it in the client's history."""
    try:
        message = client._q.get(timeout=timeout)
    except queue.Empty:
        raise SmokeFailure(
            f"timeout after {timeout:.0f}s waiting for a message; received {client.seen_types()}"
        )
    client.history.append(message)
    return message


def await_closed(client: WSClient, timeout: float) -> bool:
    """True when the reader thread observed the server closing the socket."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if client._closed.is_set():
            return True
        time.sleep(0.05)
    return False


def sign_in(client: WSClient, user_id: str, timeout: float) -> str:
    client.send_envelope(
        TYPE_AUTH_REQUEST,
        1,
        {"session_token": "", "user_id": user_id, "platform": "sso-e2e", "client_build": "sso-e2e"},
    )
    auth = payload_of(client.recv_type(TYPE_AUTH_RESPONSE, timeout))
    require(not auth.get("error"), f"auth error: {auth.get('error')}")
    require(auth.get("session_token"), "auth response missing session_token")
    return auth["session_token"]


def bootstrap(client: WSClient, timeout: float) -> dict:
    client.send_envelope(TYPE_BOOTSTRAP_REQUEST, 2, {})
    boot = payload_of(client.recv_type(TYPE_BOOTSTRAP_RESPONSE, timeout))
    require(not boot.get("error"), f"bootstrap error: {boot.get('error')}")
    return boot


def run_flow(host: str, port: int, path: str, timeout: float, reporter: Reporter) -> None:
    run_id = f"{int(time.time())}-{os.getpid()}"
    user_id = f"sso-e2e-{run_id}"

    old_client = WSClient(host, port, path, timeout)
    new_client = WSClient(host, port, path, timeout)
    probe_client = WSClient(host, port, path, timeout)
    try:
        # -- 1. first device signs in --------------------------------------- #
        token_old = sign_in(old_client, user_id, timeout)
        boot_old = bootstrap(old_client, timeout)
        reporter.ok(
            "first device: AuthRequest(1) -> AuthResponse(2) + Bootstrap(3/4)",
            f"user_id={boot_old.get('profile', {}).get('user_id')}",
        )

        # -- 2. second device signs in for the SAME identity ---------------- #
        token_new = sign_in(new_client, user_id, timeout)
        require(token_new != token_old, "second sign-in must mint a different session token")
        reporter.ok("second device: AuthRequest(1) -> AuthResponse(2) with a new token")

        # -- 3. the first device must be told, then disconnected ------------ #
        pushed = recv_any(old_client, timeout)
        require(
            pushed.get("type") == TYPE_ERROR,
            f"superseded device expected TypeError(100), got {pushed.get('type')}",
        )
        error = payload_of(pushed)
        reporter.check(
            f"superseded device receives TypeError(100) code={SESSION_REPLACED_CODE}",
            error.get("code") == SESSION_REPLACED_CODE,
            f"code={error.get('code')!r} message={error.get('message')!r}",
        )
        require(error.get("code") == SESSION_REPLACED_CODE, "wrong error code pushed to superseded device")
        reporter.check(
            "session_replaced is not retryable",
            error.get("retryable") is not True,
            f"retryable={error.get('retryable')!r}",
        )

        closed = await_closed(old_client, timeout)
        reporter.check(
            "superseded device socket is closed by the server",
            closed,
            "server sent a close frame / closed the TCP connection",
        )
        require(closed, "server did not close the superseded connection")

        # -- 4. the new device is fully usable ------------------------------ #
        boot_new = bootstrap(new_client, timeout)
        reporter.check(
            "new device stays signed in (Bootstrap succeeds)",
            boot_new.get("profile", {}).get("user_id") == user_id,
            f"user_id={boot_new.get('profile', {}).get('user_id')}",
        )

        # -- 5. the superseded token no longer resolves ---------------------- #
        probe_client.send_envelope(
            TYPE_AUTH_REQUEST,
            1,
            {"session_token": token_old, "user_id": "", "platform": "sso-e2e", "client_build": "sso-e2e"},
        )
        probe = payload_of(probe_client.recv_type(TYPE_AUTH_RESPONSE, timeout))
        reporter.check(
            "superseded session token is rejected with unauthorized",
            isinstance(probe.get("error"), dict) and probe["error"].get("code") == "unauthorized",
            f"error={probe.get('error')}",
        )
        require(
            isinstance(probe.get("error"), dict) and probe["error"].get("code") == "unauthorized",
            "superseded token must not resolve to a session",
        )
    finally:
        old_client.close()
        new_client.close()
        probe_client.close()


def parse_args(argv):
    parser = argparse.ArgumentParser(description="Gensoulkyo single-sign-on end-to-end test")
    parser.add_argument("--host", default=os.environ.get("GENSOULKYO_SMOKE_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("GENSOULKYO_SMOKE_PORT", "7350")))
    parser.add_argument("--path", default="/v1/lobby/ws")
    parser.add_argument("--timeout", type=float, default=15.0)
    parser.add_argument("--wait", type=float, default=0.0, help="seconds to wait for the TCP port before connecting")
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv if argv is not None else sys.argv[1:])
    reporter = Reporter()
    print(f"lobby SSO e2e: ws://{args.host}:{args.port}{args.path}", flush=True)
    try:
        if args.wait > 0:
            wait_for_tcp(args.host, args.port, args.wait)
        run_flow(args.host, args.port, args.path, args.timeout, reporter)
    except SmokeFailure as exc:
        reporter.fail("flow", str(exc))
    except OSError as exc:
        reporter.fail("transport", str(exc))

    print(f"lobby SSO e2e result: {reporter.summary()}", flush=True)
    return 1 if reporter.failed else 0


if __name__ == "__main__":
    sys.exit(main())
