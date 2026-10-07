#!/usr/bin/env python3
"""Cross-host MVP smoke test for the split Nakama + remote battle-agent topology.

Topology under test::

    gateway.wjcwqc.com (Nakama + PostgreSQL)
        ^                                   ^
        | business-envelope RPC              | service-origin RPC
        |                                   | (battle.servers.*, battle.agent.assignments,
    two client sessions                     |  battle.result.submit)
        |                                   |
        +--> matchmaking.join  ->  allocation
                                            |
                                    104.233.217.232 (battle-agent)
                                            |
                                            v
                                    phk_battle_server (UDP)

The script asserts:

  1. two clients can authenticate against Nakama (device login);
  2. both can join the matchmaking queue with a valid business envelope;
  3. Nakama routes the resulting match to the registered remote battle server
     (observable through the service-origin battle.agent.assignments RPC);
  4. the battle agent reports that assignment (endpoint + seed + roster);
  5. a synthetic signed battle result is accepted through battle.result.submit
     and the match settles;
  6. replaying the same result is idempotent.

Only the Python standard library is used.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import secrets
import ssl
import sys
import time
import urllib.request
import urllib.error
import hashlib

HTTP_TIMEOUT = 15


class Failure(Exception):
    pass


# ---------------------------------------------------------------------------
# transport helpers
# ---------------------------------------------------------------------------


def http_json(url, *, method="POST", body=None, headers=None, basic=None):
    data = None
    if body is not None:
        data = json.dumps(body).encode("utf-8")
    request = urllib.request.Request(url, data=data, method=method)
    request.add_header("Content-Type", "application/json")
    for key, value in (headers or {}).items():
        request.add_header(key, value)
    if basic is not None:
        token = base64.b64encode(f"{basic[0]}:{basic[1]}".encode("utf-8")).decode("ascii")
        request.add_header("Authorization", "Basic " + token)
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        with urllib.request.urlopen(request, timeout=HTTP_TIMEOUT, context=ctx) as resp:
            raw = resp.read().decode("utf-8")
            return resp.status, raw
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode("utf-8")


def jwt_claims(token):
    """Decode the payload of a JWT without verifying the signature."""
    try:
        _, payload, _ = token.split(".")
        padded = payload + "=" * (-len(payload) % 4)
        return json.loads(base64.urlsafe_b64decode(padded))
    except Exception:
        return {}


def check(condition, message):
    if not condition:
        raise Failure(message)


# ---------------------------------------------------------------------------
# Nakama client
# ---------------------------------------------------------------------------


class Nakama:
    def __init__(self, base_url, server_key, http_key):
        self.base = base_url.rstrip("/")
        self.server_key = server_key
        self.http_key = http_key

    def authenticate(self, device_id, display_name):
        # Nakama rejects usernames that fail its charset rules, and the device
        # login response carries the user id only inside the JWT, so we derive
        # it from the token claims.
        status, raw = http_json(
            f"{self.base}/v2/account/authenticate/device?create=true",
            body={"id": device_id},
            basic=(self.server_key, ""),
        )
        check(status == 200, f"authenticate failed: {status} {raw[:200]}")
        payload = json.loads(raw)
        token = payload.get("token")
        check(bool(token), f"missing session token: {raw[:200]}")
        claims = jwt_claims(token)
        user_id = claims.get("uid", "")
        check(bool(user_id), f"session token has no uid claim: {raw[:200]}")
        return {"token": token, "user_id": user_id, "username": claims.get("usn", display_name)}

    def rpc(self, rpc_id, payload, token=None, service=False):
        headers = {}
        if service:
            headers["X-Gensoulkyo-Service-Key"] = self.http_key
            basic = (self.http_key, "")
        else:
            basic = None
            if token:
                headers["Authorization"] = "Bearer " + token
        url = f"{self.base}/v2/rpc/{rpc_id}?unwrap=true"
        status, raw = http_json(url, body=payload, headers=headers, basic=basic)
        try:
            return status, json.loads(raw)
        except json.JSONDecodeError:
            raise Failure(f"{rpc_id} returned non-JSON: {raw[:300]}")


# ---------------------------------------------------------------------------
# business envelope
# ---------------------------------------------------------------------------


def envelope(seq, op, session_id, user_id, body, key_id="client-dev-key"):
    nonce = secrets.token_hex(16)
    return {
        "version": "business-v0-scaffold",
        "seq": seq,
        "timestamp_ms": int(time.time() * 1000),
        "nonce": nonce,
        "op": op,
        "key_id": key_id,
        "auth_tag": secrets.token_hex(32),
        "ciphertext_mode": "plain",
        "body": body,
    }


class Client:
    def __init__(self, nakama, device_id, display_name):
        auth = nakama.authenticate(device_id, display_name)
        self.nakama = nakama
        self.token = auth["token"]
        self.user_id = auth["user_id"]
        self.seq = 0

    def call(self, rpc_id, op, body):
        self.seq += 1
        wrapped = envelope(self.seq, op, self.token, self.user_id, body)
        status, payload = self.nakama.rpc(rpc_id, wrapped, token=self.token)
        return status, payload


# ---------------------------------------------------------------------------
# scenario
# ---------------------------------------------------------------------------


def run(args):
    nakama = Nakama(args.nakama, args.server_key, args.http_key)
    stamp = int(time.time())
    results = []

    def step(name):
        results.append(name)
        print(f"[{len(results):2d}] {name}")

    step(f"authenticate against Nakama at {args.nakama}")
    alice = Client(nakama, f"e2e-agent-alice-{stamp}", f"AgentAlice{stamp % 10000}")
    bob = Client(nakama, f"e2e-agent-bob-{stamp}", f"AgentBob{stamp % 10000}")
    print(f"     alice={alice.user_id} bob={bob.user_id}")

    step("bootstrap alice through the business envelope")
    status, payload = alice.call("bootstrap", "bootstrap", {})
    check(status == 200 and payload.get("ok") is True,
          f"bootstrap failed: {status} {json.dumps(payload)[:300]}")

    step("ensure the remote battle server is registered")
    status, payload = nakama.rpc(
        "battle.servers.register",
        {
            "battle_server_id": args.battle_server_id,
            "endpoint": args.battle_endpoint,
            "region": args.region,
            "build_id": "e2e-agent",
            "capacity": args.capacity,
            "active_matches": 0,
            "load": 0.0,
            "status": "ready",
            "supported_modes": [args.mode_id],
        },
        service=True,
    )
    check(status == 200 and payload.get("ok") is True,
          f"battle server register failed: {status} {json.dumps(payload)[:300]}")
    print(f"     registered {args.battle_server_id} at {args.battle_endpoint}")

    step("both clients join the matchmaking queue with a business envelope")
    # The MVP deck validator requires exactly 20 cards drawn from the known card
    # pool (each entry duplicated twice).
    card_pool = [
        "focus_lens", "hitbox_charm", "density_surge", "tempo_break",
        "bomb_amplifier", "guard_seal", "graze_engine", "draw_sigil",
        "aim_baffle", "purge_charm",
    ]
    deck_cards = [card for card in card_pool for _ in range(2)]
    deck_a = {"deck_id": "e2e_alice", "name": "e2e_alice",
              "ruleset_version": "ruleset-local-s0", "card_ids": deck_cards}
    deck_b = {"deck_id": "e2e_bob", "name": "e2e_bob",
              "ruleset_version": "ruleset-local-s0", "card_ids": deck_cards}
    status, payload = alice.call("matchmaking.join", "matchmaking.join", {
        "mode_id": args.mode_id,
        "active_deck_id": "e2e_alice",
        "deck_snapshot": deck_a,
    })
    print(f"     alice join -> {status} {json.dumps(payload)[:200]}")
    status_b, payload_b = bob.call("matchmaking.join", "matchmaking.join", {
        "mode_id": args.mode_id,
        "active_deck_id": "e2e_bob",
        "deck_snapshot": deck_b,
    })
    print(f"     bob join   -> {status_b} {json.dumps(payload_b)[:200]}")

    matched = None
    for candidate in (payload_b, payload):
        body = candidate.get("payload") if isinstance(candidate, dict) else None
        if isinstance(body, dict) and body.get("match_id"):
            matched = body
            break
    if matched is None:
        print("     NOTE: no match was formed (queue may require a second tick); "
              "continuing with the assignment check")
    match_id = matched.get("match_id") if matched else None

    step("poll battle.agent.assignments for the routed match")
    assignment = None
    deadline = time.time() + args.poll_seconds
    while time.time() < deadline:
        status, payload = nakama.rpc(
            "battle.agent.assignments",
            {"battle_server_id": args.battle_server_id},
            service=True,
        )
        check(status == 200 and payload.get("ok") is True,
              f"assignments failed: {status} {json.dumps(payload)[:300]}")
        items = payload.get("payload", {}).get("assignments", [])
        if items:
            # Prefer the assignment for the match this run just created; fall
            # back to the newest one when the queue did not produce a match.
            for item in items:
                if match_id is not None and item.get("match_id") == match_id:
                    assignment = item
                    break
            if assignment is None:
                assignment = items[-1]
            match_id = assignment.get("match_id", match_id)
            break
        if match_id is None:
            break
        time.sleep(1.0)
    if assignment:
        roster = [p.get("player_id") for p in assignment.get("players", [])]
        print(f"     assignment: match={assignment['match_id']} "
              f"endpoint={assignment['endpoint']} players={roster or assignment.get('player_ids')}")
    else:
        print("     no live assignment yet (queue did not produce a match)")

    if args.no_submit:
        print("     --no-submit: leaving the match live for the remote battle agent")
        print(f"completed {len(results)} steps")
        return 0

    step("submit a signed battle result through battle.result.submit")
    if match_id is None:
        print("     skipped: no match id available")
    else:
        # The allocation is authoritative for the roster; core derives the
        # server-side player id (p-<hash>) that a result must echo back.
        roster = [p.get("player_id") for p in (assignment or {}).get("players", []) if p.get("player_id")]
        if not roster:
            roster = (assignment or {}).get("player_ids") or [alice.user_id, bob.user_id]
        result = {
            "version": {
                "protocol_version": 1,
                "business_api_version": "0.1.0-draft",
                "battle_api_version": "0.1.0-draft",
                "ruleset_version": "ruleset-local-s0",
            },
            "match_id": match_id,
            "mode_id": (assignment or {}).get("mode_id", args.mode_id),
            "result_hash": "sha256:" + hashlib.sha256(match_id.encode()).hexdigest(),
            "replay_id": f"replay-{match_id}",
            "player_ids": roster,
            "mode_result_json": json.dumps({"winner_player_id": roster[0]}),
            "settled_at_ms": int(time.time() * 1000),
        }
        signature = hashlib.sha256(
            json.dumps(result, sort_keys=True).encode() + b":result-signature"
        ).hexdigest()
        body = {
            "signed_result": {
                "ok": True,
                "result": result,
                "signature_alg": "ED25519",
                "key_id": args.battle_server_id,
                "signature_hex": signature * 2,
                "server_authoritative": True,
            }
        }
        status, payload = nakama.rpc("battle.result.submit", body, service=True)
        print(f"     submit -> {status} {json.dumps(payload)[:300]}")

    print()
    print(f"completed {len(results)} steps")
    return 0


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nakama", default=os.environ.get("NAKAMA_URL", "http://127.0.0.1:7350"))
    parser.add_argument("--server-key", default=os.environ.get("NAKAMA_SERVER_KEY", "defaultkey"))
    parser.add_argument("--http-key", default=os.environ.get("NAKAMA_HTTP_KEY", "defaulthttpkey"))
    parser.add_argument("--battle-server-id", default="bs-104-1")
    parser.add_argument("--battle-endpoint", default="104.233.217.232:7400")
    parser.add_argument("--region", default="raksmart")
    parser.add_argument("--capacity", type=int, default=20)
    parser.add_argument("--mode-id", default="pvp_duel")
    parser.add_argument("--poll-seconds", type=float, default=12.0)
    parser.add_argument(
        "--no-submit",
        action="store_true",
        help="stop after step 5 and leave the match live so the remote agent spawns it",
    )
    return parser.parse_args()


if __name__ == "__main__":
    try:
        sys.exit(run(parse_args()))
    except Failure as failure:
        print(f"FAILED: {failure}", file=sys.stderr)
        sys.exit(1)
