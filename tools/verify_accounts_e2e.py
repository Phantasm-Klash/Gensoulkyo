#!/usr/bin/env python3
"""End-to-end check of the Gensoulkyo e-mail + password account flow.

It drives the real HTTP API of a throwaway lobby instance (port 7399, scratch
database) and captures the verification e-mail with a local SMTP sink, so the
whole path is exercised for real:

    POST /v1/auth/email/code   -> SMTP delivery (captured here)
    POST /v1/auth/register     -> account created, session issued
    POST /v1/auth/login        -> session issued, previous one superseded
    GET  /v1/bootstrap         -> the session is a first-class lobby session

No production data is touched: the caller points it at a scratch database.

Usage: python3 verify_accounts_e2e.py [--port 7399]
"""

from __future__ import annotations

import argparse
import base64
import email.header
import json
import re
import socket
import threading
import urllib.error
import urllib.request
from typing import Any

PASSED = 0
FAILED = 0

# Matches a standalone six-digit run. Plain \b would fail on the Q-encoded
# subject, where the code is glued to an underscore (a regex word character).
CODE_PATTERN = re.compile(r"(?<!\d)(\d{6})(?!\d)")


def check(label: str, ok: bool, detail: str = "") -> None:
    global PASSED, FAILED
    if ok:
        PASSED += 1
        print(f"  PASS  {label}")
    else:
        FAILED += 1
        print(f"  FAIL  {label}{(' -- ' + detail) if detail else ''}")


def extract_code(raw: str) -> str:
    """Pull the verification code out of a delivered message.

    The body is base64 and the subject is RFC 2047 encoded (the brand is
    Chinese), so the code is never greppable in the raw bytes. Decode both and
    search the plain text.
    """
    headers, separator, body = raw.partition("\r\n\r\n")
    if not separator:
        headers, _, body = raw.partition("\n\n")

    candidates: list[str] = []
    if "base64" in headers.lower():
        try:
            candidates.append(base64.b64decode(body.strip().encode()).decode("utf-8", "replace"))
        except Exception:  # noqa: BLE001 - a broken body must not abort the run
            pass
    candidates.append(body)
    for line in headers.splitlines():
        if line.lower().startswith("subject:"):
            value = line.partition(":")[2].strip()
            try:
                candidates.append(str(email.header.make_header(email.header.decode_header(value))))
            except Exception:  # noqa: BLE001
                candidates.append(value)

    for text in candidates:
        match = CODE_PATTERN.search(text)
        if match:
            return match.group(1)
    return ""


class SmtpSink:
    """A minimal SMTP server that records the messages it is handed."""

    def __init__(self, host: str = "127.0.0.1", port: int = 2525) -> None:
        self.host = host
        self.port = port
        self.messages: list[dict[str, str]] = []
        self._lock = threading.Lock()
        self._server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self._server.bind((self.host, self.port))
        self._server.listen(8)
        self._stop = False
        self._thread = threading.Thread(target=self._serve, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._stop = True
        try:
            self._server.close()
        except OSError:
            pass

    def latest_code(self, address: str) -> str:
        with self._lock:
            for message in reversed(self.messages):
                if address.lower() in message["rcpt"].lower():
                    code = extract_code(message["data"])
                    if code:
                        return code
        return ""

    def _serve(self) -> None:
        while not self._stop:
            try:
                conn, _ = self._server.accept()
            except OSError:
                return
            threading.Thread(target=self._handle, args=(conn,), daemon=True).start()

    def _handle(self, conn: socket.socket) -> None:
        conn.settimeout(15)
        reader = conn.makefile("rb")
        sender = ""
        rcpt = ""
        try:
            conn.sendall(b"220 sink.local ESMTP ready\r\n")
            while True:
                line = reader.readline()
                if not line:
                    return
                command = line.decode("utf-8", "replace").strip()
                upper = command.upper()
                if upper.startswith("EHLO"):
                    conn.sendall(b"250-sink.local\r\n250 SIZE 10485760\r\n")
                elif upper.startswith("HELO"):
                    conn.sendall(b"250 sink.local\r\n")
                elif upper.startswith("MAIL FROM"):
                    sender = command.partition(":")[2].strip(" <>")
                    conn.sendall(b"250 2.1.0 Ok\r\n")
                elif upper.startswith("RCPT TO"):
                    rcpt = command.partition(":")[2].strip(" <>")
                    conn.sendall(b"250 2.1.5 Ok\r\n")
                elif upper.startswith("DATA"):
                    conn.sendall(b"354 End data with <CR><LF>.<CR><LF>\r\n")
                    chunks = bytearray()
                    while True:
                        chunk = reader.readline()
                        if not chunk:
                            return
                        if chunk in (b".\r\n", b".\n"):
                            break
                        chunks.extend(chunk)
                    with self._lock:
                        self.messages.append({
                            "from": sender,
                            "rcpt": rcpt,
                            "data": chunks.decode("utf-8", "replace"),
                        })
                    conn.sendall(b"250 2.0.0 Ok: queued\r\n")
                elif upper.startswith("QUIT"):
                    conn.sendall(b"221 2.0.0 Bye\r\n")
                    return
                elif upper.startswith("RSET"):
                    conn.sendall(b"250 2.0.0 Ok\r\n")
                else:
                    conn.sendall(b"250 2.0.0 Ok\r\n")
        except OSError:
            return
        finally:
            try:
                conn.close()
            except OSError:
                pass


def post(base: str, path: str, payload: dict[str, Any], token: str = "") -> tuple[int, dict[str, Any]]:
    body = json.dumps(payload).encode()
    request = urllib.request.Request(base + path, data=body, method="POST")
    request.add_header("Content-Type", "application/json")
    if token:
        request.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.status, json.loads(response.read().decode() or "{}")
    except urllib.error.HTTPError as error:
        raw = error.read().decode()
        try:
            return error.code, json.loads(raw or "{}")
        except json.JSONDecodeError:
            return error.code, {"raw": raw}


def get(base: str, path: str, token: str = "") -> tuple[int, dict[str, Any]]:
    request = urllib.request.Request(base + path, method="GET")
    if token:
        request.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.status, json.loads(response.read().decode() or "{}")
    except urllib.error.HTTPError as error:
        raw = error.read().decode()
        try:
            return error.code, json.loads(raw or "{}")
        except json.JSONDecodeError:
            return error.code, {"raw": raw}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=7399)
    parser.add_argument("--smtp-port", type=int, default=2525)
    args = parser.parse_args()
    base = f"http://127.0.0.1:{args.port}"

    sink = SmtpSink(port=args.smtp_port)
    sink.start()
    print(f"smtp sink listening on 127.0.0.1:{args.smtp_port}")
    print(f"lobby under test: {base}\n")

    print("[1] health + auth config")
    status, health = get(base, "/health")
    check("GET /health is 200", status == 200, str(health))
    status, config = get(base, "/v1/auth/config")
    check("GET /v1/auth/config is 200", status == 200, str(config))
    check("registration is open", config.get("registration_open") is True, str(config))
    check("mail transport is wired", config.get("mail_enabled") is True, str(config))
    check("password algorithm is pbkdf2-sha256",
          config.get("password_algorithm") == "pbkdf2-sha256", str(config))

    print("\n[2] registration is refused without a verification code")
    status, body = post(base, "/v1/auth/register", {
        "username": "e2e_user", "email": "e2e@example.com",
        "password": "wjc11111", "code": "123456",
    })
    check("register without a requested code is rejected",
          body.get("error_code") == "verification_code_required", str(body))

    print("\n[3] request a verification code (real SMTP path)")
    status, body = post(base, "/v1/auth/email/code", {"email": "e2e@example.com"})
    check("POST /v1/auth/email/code is 200", status == 200, str(body))
    check("response normalises the address", body.get("email") == "e2e@example.com", str(body))
    check("response reports a positive expiry", (body.get("expires_in_seconds") or 0) > 0, str(body))
    check("response reports a resend delay", (body.get("resend_in_seconds") or 0) > 0, str(body))

    code = sink.latest_code("e2e@example.com")
    check("the verification e-mail actually reached the SMTP relay", len(code) == 6, f"code={code!r}")
    if len(sink.messages):
        message = sink.messages[-1]
        check("the message is addressed to the player",
              message["rcpt"].lower() == "e2e@example.com", message["rcpt"])
        check("the message body carries the code",
              bool(code) and code in message["data"], message["data"][:200])
        check("the subject is MIME encoded (non-ASCII brand)",
              "subject: =?utf-8?" in message["data"].lower(), message["data"][:400])

    print("\n[4] resend cooldown")
    status, body = post(base, "/v1/auth/email/code", {"email": "e2e@example.com"})
    check("an immediate resend is rate limited",
          body.get("error_code") == "verification_rate_limited", str(body))
    check("the rate-limit answer is HTTP 429", status == 429, str(status))

    print("\n[5] register with the delivered code")
    status, session = post(base, "/v1/auth/register", {
        "username": "e2e_user", "email": "E2E@example.com",
        "password": "wjc11111", "code": code, "display_name": "E2E User",
    })
    check("POST /v1/auth/register is 200", status == 200, str(session))
    check("register returns a session token", bool(session.get("session_token")), str(session))
    check("register returns the account id", bool(session.get("user_id")), str(session))
    check("register keeps the display name", session.get("display_name") == "E2E User", str(session))
    first_token = session.get("session_token", "")

    print("\n[6] the registration session is a real lobby session")
    status, bootstrap = get(base, "/v1/bootstrap", token=first_token)
    check("GET /v1/bootstrap with the new token is 200", status == 200, str(bootstrap))
    check("bootstrap reports the same user", bootstrap.get("user_id") == session.get("user_id"), str(bootstrap))

    print("\n[7] duplicate e-mail / username are refused")
    status, body = post(base, "/v1/auth/email/code", {"email": "e2e@example.com"})
    check("requesting a code for a registered address is refused",
          body.get("error_code") == "account_email_taken", str(body))

    print("\n[8] sign in with e-mail + password")
    status, login = post(base, "/v1/auth/login", {"email": "e2e@example.com", "password": "wjc11111"})
    check("POST /v1/auth/login is 200", status == 200, str(login))
    check("login issues a fresh token", bool(login.get("session_token")), str(login))
    second_token = login.get("session_token", "")

    print("\n[9] single sign-on: the older session is superseded")
    status, body = get(base, "/v1/bootstrap", token=first_token)
    check("the superseded token is rejected", status == 401, f"{status} {body}")
    check("rejection is a bare unauthorized (not session_replaced)",
          body.get("error_code") == "unauthorized", str(body))
    status, _ = get(base, "/v1/bootstrap", token=second_token)
    check("the newest token still works", status == 200, str(status))

    print("\n[10] wrong credentials")
    status, body = post(base, "/v1/auth/login", {"email": "e2e@example.com", "password": "wrong-password"})
    check("a wrong password is 401", status == 401, str(status))
    check("a wrong password is unauthorized", body.get("error_code") == "unauthorized", str(body))
    status, body = post(base, "/v1/auth/login", {"email": "nobody@example.com", "password": "wjc11111"})
    check("an unknown address is indistinguishable (401 unauthorized)",
          status == 401 and body.get("error_code") == "unauthorized", str(body))

    print("\n[11] logout invalidates the token")
    status, body = post(base, "/v1/auth/logout", {}, token=second_token)
    check("POST /v1/auth/logout is 200", status == 200, str(body))
    status, _ = get(base, "/v1/bootstrap", token=second_token)
    check("the token is dead after logout", status == 401, str(status))

    print("\n[12] input validation")
    for payload, expected, label in (
        ({"username": "ab", "email": "x@example.com", "password": "wjc11111", "code": "123456"},
         "account_username_invalid", "short username"),
        ({"username": "e2e_two", "email": "not-an-email", "password": "wjc11111", "code": "123456"},
         "account_email_invalid", "malformed e-mail"),
        ({"username": "e2e_two", "email": "x@example.com", "password": "short", "code": "123456"},
         "account_password_weak", "short password"),
    ):
        _, body = post(base, "/v1/auth/register", payload)
        check(f"{label} is rejected with {expected}", body.get("error_code") == expected, str(body))

    sink.stop()
    print(f"\n===== {PASSED} passed / {FAILED} failed =====")
    return 1 if FAILED else 0


if __name__ == "__main__":
    raise SystemExit(main())
