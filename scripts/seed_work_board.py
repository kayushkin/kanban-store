#!/usr/bin/env python3
"""Seed a board of synthetic software-team work emails, for exercising the
email classifier and the card action timeline.

Every card here is fiction. Nothing in it came from a real mailbox, no message
it links to exists in mailstack, and the addresses are all under .example. The
point is to give the timeline something to draw: cards that ran over their
limit and cards that met it, long stretches of waiting on somebody else, agent
dispatches, coding updates, notes a person wrote, deadlines already missed and
deadlines still ahead.

The board carries the same four columns and the same priority ladder as the
live "Email" board, so email-classifier can be pointed at it as a sandbox:

    email-classifier -board "Work board" -dry-run

One artefact worth knowing before you read a timeline here. kanban-store stamps
`card_created` at the moment the card is made, and there is no way to backdate
it, so on every seeded card that event sits at the END of the story instead of
the start. That is not a defect in the seed or the store: a classifier filing
week-old mail produces exactly this shape, an email that arrived long before
anything on this host heard about it. The clock figures are unaffected — the
event carries its column's clock state and the totals are walked from the
first event forward.

    ./scripts/seed_work_board.py            # create the board and its cards
    ./scripts/seed_work_board.py --purge    # hard-delete the cards, then the board

Purge is destructive on purpose: it passes ?hard=true, which purges the
underlying noteboard items rather than leaving nine orphan todos behind.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

DEFAULT_KANBAN_URL = os.environ.get("KANBAN_STORE_URL", "http://localhost:8305")
DEFAULT_BOARD_NAME = "Work board"

BOARD_DESCRIPTION = (
    "Synthetic software-team work emails, for classifier and action-timeline "
    "testing. Every card, email and address on this board is made up. "
    "Written by scripts/seed_work_board.py in kanban-store."
)

BUSINESS_HOURS = {
    "tzid": "America/Los_Angeles",
    "days": ["MO", "TU", "WE", "TH", "FR"],
    "start": "09:00",
    "end": "17:00",
}

# The Email board's columns, name for name, so the classifier finds what it
# looks up. budget_clock_state is what landing in the column means for the clock.
COLUMNS = [
    {"name": "No action", "position": 1.0, "color": "#6b7280", "budget_clock_state": "stopped"},
    {"name": "Action needed", "position": 2.0, "color": "#d97706", "budget_clock_state": "running"},
    {"name": "Waiting on reply", "position": 2.5, "color": "#7c3aed", "budget_clock_state": "paused"},
    {"name": "Action completed", "position": 3.0, "color": "#059669",
     "auto_status": "done", "budget_clock_state": "stopped"},
]

# P0 is the top rung and the top rung is the highest priority_value — noteboard
# sorts priority descending. Same ladder as the Email board.
PRIORITY_LEVELS = [
    {"priority_value": 5, "label": "P0", "budget_seconds": 2 * 3600},
    {"priority_value": 4, "label": "P1", "budget_seconds": 8 * 3600},
    {"priority_value": 3, "label": "P2", "budget_seconds": 24 * 3600},
    {"priority_value": 2, "label": "P3", "budget_seconds": 7 * 24 * 3600},
    {"priority_value": 1, "label": "P4", "budget_seconds": 30 * 24 * 3600},
]

# Each card: what the work is, where it sits, what it links to, and what has
# happened to it. Hours are counted back from the moment the seed runs, so a
# freshly seeded board always reads as though the work happened this week.
#
# An event entry is (hours_ago, kind, actor, summary) with an optional fifth
# element forcing the clock state; a note entry is ("note", hours_ago, kind,
# actor, body).
CARDS = [
    {
        "title": "Add cursor-based pagination to /v1/search",
        "column": "Action completed",
        "priority": 4,
        "due_hours": -48,
        "tags": ["cat:engineering", "action:task", "urgency:normal", "work:done"],
        "body": (
            "Offset paging on `/v1/search` falls apart past page 40 and the mobile "
            "release needs stable cursors.\n\n"
            "Keep the offset parameter behind a flag for one release so the old "
            "clients do not break."
        ),
        "sender": "priya.raman@northwind-eng.example",
        "messages": ["1a4f2c19", "1a4f7b30"],
        "events": [
            (120, "email_received", "priya.raman@northwind-eng.example",
             "Can you do this task: replace offset paging on /v1/search before the mobile release?"),
            ("note", 119.5, "status", "vlad",
             "Confirmed the scope with Priya: cursor paging, offset stays behind a flag for one release."),
            (119, "agent_dispatched", "vlad",
             "Handed to claude-code: implement cursor pagination in search-api"),
            ("note", 117, "coding-update", "claude-code",
             "Added an `after` cursor to the query planner, migrated 3 call sites, 11 tests green. "
             "Draft PR #4471."),
            (116.5, "agent_finished", "claude-code", "PR #4471 opened, CI green"),
            (116, "waiting_started", "vlad", "Sent for review to the mobile team"),
            (50, "waiting_ended", "dinesh.okonkwo@northwind-eng.example", "Review approved"),
            ("note", 49, "status", "vlad", "Merged and deployed to staging behind the flag."),
            (48.5, "card_moved", "vlad", "Moved to Action completed", "stopped"),
            (48, "card_completed", "vlad", "Shipped in the 2026-08-22 release"),
        ],
    },
    {
        "title": "Check the status of order ORD-48812 — stuck in the fulfilment webhook",
        "column": "Waiting on reply",
        "priority": 5,
        "due_hours": 4,
        "tags": ["cat:support", "action:investigate", "urgency:high", "work:in-progress"],
        "body": (
            "Payment captured on ORD-48812, no fulfilment event ever arrived.\n\n"
            "Customer ops wants a status answer today."
        ),
        "sender": "ops@northwind-support.example",
        "messages": ["2b8e0447"],
        "events": [
            (26, "email_received", "ops@northwind-support.example",
             "Can you check the status of order ORD-48812? Payment captured, no fulfilment event."),
            ("note", 25.5, "status", "vlad",
             "Traced the webhook: 3 delivery attempts, all 502 from the fulfilment vendor. "
             "The order row itself is consistent."),
            (25, "agent_dispatched", "vlad",
             "Handed to claude-code: pull the webhook delivery log for ORD-48812"),
            (24.6, "agent_finished", "claude-code",
             "Last delivery attempt 2026-08-23T18:41Z, vendor returned 502 with no body"),
            (24.5, "waiting_started", "vlad",
             "Raised ticket VND-9921 with the vendor. The ball is with them."),
        ],
    },
    {
        "title": "Cancel the Redis → Valkey migration spike",
        "column": "Action completed",
        "priority": 3,
        "due_hours": None,
        "tags": ["cat:engineering", "action:cancel", "urgency:normal", "work:done"],
        "body": (
            "Platform decided to stay on managed Redis this quarter, so the spike "
            "stops here.\n\nKeep the benchmark harness — it is the reusable part."
        ),
        "sender": "marcus.feld@northwind-eng.example",
        "messages": ["3c1d9950"],
        "events": [
            (72, "email_received", "marcus.feld@northwind-eng.example",
             "Cancel the Valkey spike — we are staying on managed Redis for this quarter."),
            ("note", 71.8, "status", "vlad",
             "Spike was two days in. Stopping now and keeping the benchmark harness."),
            (71.5, "agent_dispatched", "vlad",
             "Handed to claude-code: archive the spike branch and write up what we measured"),
            (70.5, "agent_finished", "claude-code",
             "Branch archived, findings written to docs/spikes/valkey.md"),
            (70, "card_moved", "vlad", "Moved to Action completed — cancelled by the requester", "stopped"),
            (69.9, "card_completed", "vlad", "Cancelled, not delivered"),
        ],
    },
    {
        "title": "Update the SSO rollout task to include SCIM provisioning",
        "column": "Action needed",
        "priority": 5,
        "due_hours": -12,
        "tags": ["cat:engineering", "action:task", "urgency:high", "work:in-progress"],
        "body": (
            "The SSO rollout now has to cover SCIM user provisioning for Okta, and "
            "a follow-up email added group sync on top of that.\n\n"
            "Deprovisioning is still unhandled and nobody has scoped it."
        ),
        "sender": "priya.raman@northwind-eng.example",
        "messages": ["4d7a1188", "4d7a2291"],
        "events": [
            (8, "email_received", "priya.raman@northwind-eng.example",
             "Can you update the SSO rollout task to include SCIM user provisioning for Okta?"),
            ("note", 7.5, "status", "vlad",
             "Scope grew: SCIM is its own API surface, not a flag on the existing one. Re-estimating."),
            (7, "agent_dispatched", "vlad",
             "Handed to claude-code: scaffold the SCIM /Users endpoint behind a feature flag"),
            (5, "agent_finished", "claude-code", "Scaffold plus 18 tests, PR #4488 (draft)"),
            (4, "email_received", "priya.raman@northwind-eng.example",
             "Follow-up: include group sync in the same rollout."),
            ("note", 3.5, "coding-update", "claude-code",
             "Group sync folded into the same PR. Deprovisioning is still unhandled — it needs a decision "
             "on what happens to orphaned sessions."),
        ],
    },
    {
        "title": "Nightly CI digest — main",
        "column": "No action",
        "priority": None,
        "due_hours": None,
        "tags": ["cat:automated", "action:none", "urgency:low", "work:not-started"],
        "body": (
            "Bucket card. The nightly CI digest lands here so it stops asking for a "
            "decision every morning — one card, many links, no work.\n\n"
            "This is the shape bulk mail takes on a classifier board: the sender "
            "affinity routes it with no model call at all."
        ),
        "sender": "ci-bot@northwind-eng.example",
        "messages": ["5e2b7001", "5e2b7002", "5e2b7003"],
        "events": [
            (72, "email_received", "ci-bot@northwind-eng.example",
             "Nightly build main@a91c4f — 412 passed, 0 failed", "stopped"),
            (48, "email_received", "ci-bot@northwind-eng.example",
             "Nightly build main@c30d81 — 412 passed, 0 failed", "stopped"),
            (24, "email_received", "ci-bot@northwind-eng.example",
             "Nightly build main@f7b219 — 411 passed, 1 flaky (checkout_e2e)", "stopped"),
        ],
    },
    {
        "title": "Rotate the staging Postgres credentials before the 1 Sep audit",
        "column": "Action needed",
        "priority": 4,
        "due_hours": 7 * 24,
        "tags": ["cat:security", "action:task", "urgency:normal", "work:not-started"],
        "body": (
            "Security wants the staging Postgres credentials rotated ahead of the "
            "1 Sep audit.\n\nRotation restarts the app, so it waits for the Thursday "
            "maintenance window."
        ),
        "sender": "security@northwind-eng.example",
        "messages": ["6f3c5512"],
        "events": [
            (30, "email_received", "security@northwind-eng.example",
             "Rotate the staging Postgres credentials before the 1 Sep audit."),
            ("note", 29, "status", "vlad",
             "Rotation needs the app restarted, so this goes in the Thursday window."),
            (28.5, "waiting_started", "vlad",
             "Booked into the Thursday maintenance window. Nothing to do until it opens."),
        ],
        # Held live rather than as a backdated event, so the board shows one
        # genuinely parked card: an agent looking for work must skip it.
        "hold_reason": "Waiting for the Thursday maintenance window",
    },
    {
        "title": "Unfiled email — needs triage",
        "column": "Action needed",
        "priority": None,
        "due_hours": None,
        "tags": ["cat:unknown", "action:triage", "urgency:normal", "work:not-started"],
        "body": (
            "The model returned no card for this message, so it lands here rather "
            "than vanishing.\n\nAn unfiled email is a classifier failure, and this "
            "card sits in Action needed precisely so it is visible."
        ),
        "sender": "no-reply@vendor-portal.example",
        "messages": ["7a9d0033"],
        "events": [
            (6, "email_received", "no-reply@vendor-portal.example",
             "Re: Fwd: (no subject) — model returned no card for this message"),
        ],
    },
    {
        "title": "Draft the incident review for the 14:02 checkout outage",
        "column": "Action needed",
        "priority": 4,
        "due_hours": 6,
        "tags": ["cat:engineering", "action:write", "urgency:high", "work:in-progress"],
        "body": (
            "Checkout was down 14:02–14:37. The review needs a timeline, the "
            "customer-impact numbers and the follow-up actions, circulated by Friday."
        ),
        "sender": "helena.vos@northwind-eng.example",
        "messages": ["8b4e6120"],
        "events": [
            (28, "email_received", "helena.vos@northwind-eng.example",
             "We need the incident review for the 14:02 checkout outage by Friday."),
            (27, "agent_dispatched", "vlad",
             "Handed to claude-code: assemble the outage timeline from logstack"),
            (26, "agent_finished", "claude-code", "Timeline drafted from 4 log sources"),
            ("note", 25.5, "status", "vlad",
             "Timeline looks right. I want customer-impact numbers in it before we circulate."),
            (25, "waiting_started", "vlad", "Waiting on the data team for the refund counts"),
            (6, "waiting_ended", "data-team@northwind-eng.example",
             "Numbers arrived: 812 failed checkouts, 61 refunds"),
            (5, "agent_dispatched", "vlad", "Handed to claude-code: fold the impact numbers into the review"),
            (4, "agent_finished", "claude-code", "Draft ready for review"),
            ("note", 3.5, "status", "vlad", "Reading it through once more before it goes out."),
        ],
    },
    {
        "title": "Bump the build image to Node 22 LTS",
        "column": "Action needed",
        "priority": 2,
        "due_hours": 14 * 24,
        "tags": ["cat:engineering", "action:task", "urgency:low", "work:not-started"],
        "body": "Background chore. The build image is still on Node 20; 22 is LTS now.",
        "sender": "marcus.feld@northwind-eng.example",
        "messages": ["9c5f8844"],
        "events": [
            (100, "email_received", "marcus.feld@northwind-eng.example",
             "Low priority: bump the build image to Node 22 LTS when there is a gap."),
            ("note", 99, "status", "vlad", "Queued behind the SSO work."),
        ],
    },
]

# The fictional mailstack account these messages would live in. The email link
# ref is account-qualified because an IMAP ref is a bare UID, unique only
# within one mailbox.
MAIL_ACCOUNT = "seed-work"


class ApiError(RuntimeError):
    pass


def request(base_url: str, method: str, path: str, body: dict | None = None):
    """One HTTP call to kanban-store. Any non-2xx raises — a seed that half
    succeeded and said nothing would leave a board nobody can trust."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        base_url.rstrip("/") + path,
        data=data,
        method=method,
        headers={"Content-Type": "application/json"} if data else {},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            raw = response.read()
    except urllib.error.HTTPError as exc:
        raise ApiError(f"{method} {path} → {exc.code}: {exc.read().decode(errors='replace')}") from exc
    except urllib.error.URLError as exc:
        raise ApiError(f"{method} {path} → {exc.reason} (is kanban-store running on {base_url}?)") from exc
    return json.loads(raw) if raw else None


def find_board(base_url: str, name: str) -> dict | None:
    for board in request(base_url, "GET", "/api/boards?include_archived=true"):
        if board["name"] == name:
            return board
    return None


def stamp(now: datetime, hours_ago: float) -> str:
    return (now - timedelta(hours=hours_ago)).isoformat().replace("+00:00", "Z")


def seed(base_url: str, board_name: str) -> None:
    if find_board(base_url, board_name) is not None:
        raise ApiError(
            f'a board named "{board_name}" already exists. '
            f"Run with --purge first, or pass --board-name to seed a second one."
        )

    now = datetime.now(timezone.utc)

    board = request(base_url, "POST", "/api/boards",
                    {"name": board_name, "description": BOARD_DESCRIPTION})
    board_id = board["id"]
    print(f'board "{board_name}" → {board_id}')

    request(base_url, "PATCH", f"/api/boards/{board_id}", {"business_hours": BUSINESS_HOURS})
    request(base_url, "PUT", f"/api/boards/{board_id}/priority-levels", {"levels": PRIORITY_LEVELS})

    column_ids = {}
    for spec in COLUMNS:
        column = request(base_url, "POST", f"/api/boards/{board_id}/columns", spec)
        column_ids[spec["name"]] = column["id"]
    print(f"  {len(column_ids)} columns, {len(PRIORITY_LEVELS)} priority levels, business hours set")

    for spec in CARDS:
        create = {
            "title": spec["title"],
            "body": spec["body"],
            "column_id": column_ids[spec["column"]],
            "tags": ["email", "email-classifier", "work-board-seed", "test-data"] + spec["tags"],
        }
        if spec["priority"] is not None:
            create["priority"] = spec["priority"]
        if spec["due_hours"] is not None:
            create["due_at"] = stamp(now, -spec["due_hours"])

        card = request(base_url, "POST", f"/api/boards/{board_id}/cards", create)
        card_id = card["placement"]["card_id"]

        # Links. The Message-ID is what dedups one mail that reached two
        # accounts and what rebuilds a thread; the sender is the affinity the
        # classifier learns to route bulk mail by without calling a model.
        #
        # There is deliberately no 'email' locator link. kanban-store treats one
        # as an arrival and records an email_received event for it — stamped
        # now, because a link carries no occurred_at. On seeded cards that
        # duplicates the backdated arrival below and leaves every finished card
        # reporting a running clock. The locator these messages would have had
        # rides along in the arrival event's detail instead, where it is
        # readable and inert. Nothing is lost: the messages are fiction, so a
        # locator link could never resolve in mailstack anyway.
        for message_id in spec["messages"]:
            request(base_url, "POST", f"/api/cards/{card_id}/links", {
                "entity_type": "email_msgid",
                "entity_ref": f"{message_id}@mail.northwind-eng.example",
                "label": spec["title"],
            })
        request(base_url, "POST", f"/api/cards/{card_id}/links", {
            "entity_type": "email_sender",
            "entity_ref": spec["sender"],
        })

        arrivals = iter(spec["messages"])
        for entry in spec["events"]:
            if entry[0] == "note":
                _, hours_ago, kind, actor, body = entry
                request(base_url, "POST", f"/api/cards/{card_id}/notes", {
                    "body": body, "kind": kind, "actor": actor,
                    "board_id": board_id, "occurred_at": stamp(now, hours_ago),
                })
                continue
            hours_ago, kind, actor, summary = entry[:4]
            event = {
                "kind": kind, "actor": actor, "summary": summary,
                "occurred_at": stamp(now, hours_ago),
            }
            # An email arriving concerns the card wherever it sits, so it is
            # recorded card-wide. Everything else happened on this board.
            if kind == "email_received":
                message_id = next(arrivals, spec["messages"][-1])
                event["detail"] = {
                    "locator": f"{MAIL_ACCOUNT}:{message_id}",
                    "message_id": f"{message_id}@mail.northwind-eng.example",
                    "from": actor,
                }
            else:
                event["board_id"] = board_id
            # card_moved has no default clock state — a move means whatever its
            # destination column means — and a No-action bucket stops the clock
            # on arrival rather than starting it.
            if len(entry) > 4:
                event["clock_state"] = entry[4]
            request(base_url, "POST", f"/api/cards/{card_id}/events", event)

        if spec.get("hold_reason"):
            request(base_url, "POST", f"/api/cards/{card_id}/hold", {"reason": spec["hold_reason"]})

        held = " (held)" if spec.get("hold_reason") else ""
        print(f"  {spec['column']:<18} {spec['title']}{held}")

    print(f"\n{len(CARDS)} cards seeded. Board: {base_url}/api/boards/{board_id}/cards")


def purge(base_url: str, board_name: str) -> None:
    board = find_board(base_url, board_name)
    if board is None:
        raise ApiError(f'no board named "{board_name}"')
    board_id = board["id"]

    view = request(base_url, "GET", f"/api/boards/{board_id}/cards")
    card_ids = [card["placement"]["card_id"]
                for column in view.get("columns") or []
                for card in column.get("cards") or []]
    card_ids += [card["placement"]["card_id"] for card in view.get("orphans") or []]

    for card_id in card_ids:
        request(base_url, "DELETE", f"/api/cards/{card_id}?hard=true")
    request(base_url, "DELETE", f"/api/boards/{board_id}")
    print(f'purged {len(card_ids)} cards and the board "{board_name}"')


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--kanban-url", default=DEFAULT_KANBAN_URL,
                        help=f"kanban-store base URL (default {DEFAULT_KANBAN_URL})")
    parser.add_argument("--board-name", default=DEFAULT_BOARD_NAME,
                        help=f"board to create or purge (default {DEFAULT_BOARD_NAME!r})")
    parser.add_argument("--purge", action="store_true",
                        help="hard-delete the board's cards and the board itself, then exit")
    args = parser.parse_args()

    try:
        if args.purge:
            purge(args.kanban_url, args.board_name)
        else:
            seed(args.kanban_url, args.board_name)
    except ApiError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
