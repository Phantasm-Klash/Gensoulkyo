#!/usr/bin/env python3
"""Fetch the newest e-mail verification code sent to an address.

The lobby mails registration codes through the production SMTP relay
(`smtp.ym.163.com`) from `no-reply@wjcwqc.com`. That mailbox is readable over
IMAP with the same credential, which is what makes an *automated* registration
test possible without weakening the server: the code really does travel by
e-mail, it is just read back programmatically.

The mailbox accepts plus-addressing, so each test account can use a distinct
address (`no-reply+pk1@wjcwqc.com`) and still land in this one inbox.

Usage:
    python3 fetch_email_code.py <address> [--timeout 90] [--since-epoch N]

Prints the 6-digit code on stdout, or exits 1 with a message on stderr.
"""

from __future__ import annotations

import argparse
import base64
import email.header
import imaplib
import re
import ssl
import sys
import time

IMAP_HOST = "imap.ym.163.com"
IMAP_PORT = 993
IMAP_USER = "no-reply@wjcwqc.com"
IMAP_PASSWORD = "wjc11111"

CODE_PATTERN = re.compile(r"(?<!\d)(\d{6})(?!\d)")


def decode_message(raw: bytes) -> list[str]:
    """Return every plain-text view of a message worth searching.

    The lobby sends a base64 body and an RFC 2047 subject (the brand is
    Chinese), so the code is never greppable in the raw bytes. A plain
    `\\b\\d{6}\\b` also fails on the Q-encoded subject, where the code is glued to
    an underscore and `_` is a regex word character.
    """
    headers, separator, body = raw.partition(b"\r\n\r\n")
    if not separator:
        headers, _, body = raw.partition(b"\n\n")

    candidates: list[str] = []
    if b"base64" in headers.lower():
        try:
            candidates.append(base64.b64decode(b"".join(body.split())).decode("utf-8", "replace"))
        except Exception:  # noqa: BLE001 - a broken body must not abort the poll
            pass
    candidates.append(body.decode("utf-8", "replace"))
    for line in headers.decode("utf-8", "replace").splitlines():
        if line.lower().startswith("subject:"):
            value = line.partition(":")[2].strip()
            try:
                candidates.append(str(email.header.make_header(email.header.decode_header(value))))
            except Exception:  # noqa: BLE001
                candidates.append(value)
    return candidates


def extract_code(raw: bytes) -> str:
    for text in decode_message(raw):
        match = CODE_PATTERN.search(text)
        if match:
            return match.group(1)
    return ""


def fetch(address: str, timeout: float) -> str:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            client = imaplib.IMAP4_SSL(IMAP_HOST, IMAP_PORT, ssl_context=ssl.create_default_context(), timeout=20)
            try:
                client.login(IMAP_USER, IMAP_PASSWORD)
                client.select("INBOX")
                _, data = client.search(None, "ALL")
                ids = data[0].split()
                for message_id in reversed(ids[-8:]):
                    _, message = client.fetch(message_id, "(BODY.PEEK[])")
                    raw = message[0][1]
                    if address.encode() not in raw:
                        continue
                    code = extract_code(raw)
                    if code:
                        return code
            finally:
                try:
                    client.logout()
                except Exception:  # noqa: BLE001
                    pass
        except Exception as error:  # noqa: BLE001 - transient IMAP failures are expected
            print(f"imap retry: {error}", file=sys.stderr)
        time.sleep(5)
    return ""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("address")
    parser.add_argument("--timeout", type=float, default=90)
    args = parser.parse_args()

    code = fetch(args.address, args.timeout)
    if not code:
        print(f"no verification code arrived for {args.address} within {args.timeout:.0f}s", file=sys.stderr)
        return 1
    print(code)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
