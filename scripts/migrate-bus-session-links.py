#!/usr/bin/env python3
"""One-shot migration: rewrite kanban card_links with entity_type='bus_session'
to entity_type='session', resolving the canonical llm-bridge-server session_id.

Background:
  Until 2026-05-09 (llm-bridge-adapter commit 73c7cb5) the autoworker minted a
  bus_session_id like `autoworker-anthropic-<nanos>` and the adapter mapped it
  to a separate bridge_id `br_<nanos>`. After consolidation the adapter passes
  the bus_session_id through verbatim as the bridge session_id. Kanban links
  for those new sessions are already entity_type='session'. Old (pre-5/9)
  cards still carry entity_type='bus_session' with the autoworker-style ref,
  which needs translating via the adapter's /sessions/by-bus map.

Resolution rules for each existing bus_session link:
  1. Try GET {bridge}/sessions/{ref}. If 200 → ref is already canonical, no-op.
  2. Else try GET {adapter}/sessions/by-bus/{ref}. If 200, use the returned
     session_id as the new entity_ref.
  3. Else log a warning and leave the link alone.

Then for each resolvable link:
  - POST {kanban}/api/cards/{card_id}/links with entity_type='session', the
    resolved ref, and the same label.
  - DELETE {kanban}/api/links/{old_link_id}.

The kanban-store UNIQUE constraint (card_id, entity_type, entity_ref) makes
this idempotent: a second run finds zero bus_session links.

Usage:
  ./migrate-bus-session-links.py            # dry-run, prints plan + counts
  ./migrate-bus-session-links.py --apply    # write changes
"""

import argparse
import urllib.parse
import urllib.request
import json


KANBAN = "http://localhost:8305"
BRIDGE = "http://localhost:8160"
ADAPTER = "http://localhost:8161"


def http_get(url):
    try:
        with urllib.request.urlopen(url, timeout=5) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:
        return -1, str(e).encode()


def http_post(url, body):
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def http_delete(url):
    req = urllib.request.Request(url, method="DELETE")
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def resolve(ref):
    """Return (canonical_session_id, source) or (None, reason)."""
    status, _ = http_get(f"{BRIDGE}/sessions/{urllib.parse.quote(ref, safe='')}")
    if status == 200:
        return ref, "direct"
    status, body = http_get(f"{ADAPTER}/sessions/by-bus/{urllib.parse.quote(ref, safe='')}")
    if status == 200:
        try:
            data = json.loads(body)
            sid = data.get("session_id")
            if sid:
                return sid, "adapter"
            return None, "adapter response missing session_id"
        except Exception as e:
            return None, f"adapter parse error: {e}"
    return None, f"unresolved (direct={status})"


def list_bus_session_links():
    """Return all bus_session card_links.

    Reads directly from the SQLite DB to include links on cards with no
    current placement on any non-archived board (which the board-walk API
    would skip).
    """
    import sqlite3
    import os

    path = os.path.expanduser("~/.kanban-store/kanban-store.db")
    db = sqlite3.connect(path)
    db.row_factory = sqlite3.Row
    rows = db.execute(
        "SELECT id, card_id, entity_ref, label FROM card_links WHERE entity_type='bus_session'"
    ).fetchall()
    db.close()
    return [
        {"link_id": r["id"], "card_id": r["card_id"], "ref": r["entity_ref"], "label": r["label"] or ""}
        for r in rows
    ]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--apply", action="store_true", help="Write changes")
    args = ap.parse_args()

    links = list_bus_session_links()
    print(f"found {len(links)} bus_session links")

    direct, adapter, unresolved = 0, 0, 0
    plan = []
    for l in links:
        new_ref, source = resolve(l["ref"])
        if new_ref is None:
            unresolved += 1
            print(f"  SKIP card={l['card_id'][:8]} ref={l['ref']} — {source}")
            continue
        if source == "direct":
            direct += 1
        else:
            adapter += 1
        plan.append({**l, "new_ref": new_ref, "source": source})

    print(f"resolvable: {len(plan)} (direct={direct} adapter={adapter}) unresolved={unresolved}")

    if not args.apply:
        print("dry-run; pass --apply to migrate")
        return

    ok, fail = 0, 0
    for p in plan:
        body = {
            "entity_type": "session",
            "entity_ref": p["new_ref"],
        }
        if p["label"]:
            body["label"] = p["label"]
        s, resp = http_post(f"{KANBAN}/api/cards/{p['card_id']}/links", body)
        if s not in (201, 409):  # 409 if a session link with that ref already exists (idempotent)
            print(f"  FAIL POST card={p['card_id'][:8]} HTTP {s}: {resp!r}")
            fail += 1
            continue
        s, _ = http_delete(f"{KANBAN}/api/links/{p['link_id']}")
        if s not in (200, 204, 404):
            print(f"  FAIL DELETE link={p['link_id']} HTTP {s}")
            fail += 1
            continue
        ok += 1
    print(f"applied: ok={ok} fail={fail}")


if __name__ == "__main__":
    main()
