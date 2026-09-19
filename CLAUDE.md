# About kanban-store

## What it owns

`:8305`. Board and card *placement*, treating **noteboard as the source of truth for card content** — it stores where a card sits, not what it says. Publishes an entity-type registry so agents can resolve entity refs themselves; deliberately dumb, it does not proxy to those services. README is the route table.

## Where this prompt lives

These sections are stored in agent-store as a project prompt collection and rendered, with identical text, to `AGENTS.md` and `CLAUDE.md` at the root of this repo, so that whichever file a harness reads it gets the same thing. Edit them on dash `/files`, or edit either rendered file: the 15-minute scan carries the edit back into the sections and out to the other file. The host prompt keeps one row for this repo with only what an agent elsewhere needs.

# How it works

## A board owns what its jobs run with

A board owns its settings — `default_principal_id`, `default_agent_id` (agent-store's numeric id, never the slug), `default_instance_id`, `default_bundle_id` (bundle-store's numeric id; the dispatcher sends it as `bundle_id` on `POST /sessions`) and a `classifier` object (`vocabulary`, `mail_account_ids`, `hold_new_cards`) — which the scheduler binaries (`email-classifier`, `kanban-dispatcher`, `kanban-scoper`, `kanban-curator`, `demo-worker`) read by `-board-id` instead of taking as flags: **the board owns what a job runs with, the scheduler job owns when it runs.** Each id is checked with its owner on `PATCH /api/boards/{id}` (principal-store, llm-bridge-server via `LLM_BRIDGE_URL`, bundle-store via `BUNDLE_STORE_URL`): 400 when the owner says no, 502 when it cannot answer, nothing written. The default principal is applied by the store itself — a card created on or attached to the board with no assignee takes it (`assigned` event, detail `source=board_default`), and a card with someone on it is left alone. Edited on bridge-ui's `kanban/settings` page. Vocabulary names are owned by `email-classifier` (`-list-vocabularies`), not stored anywhere.

## Tag rules

Tag rules override a board's defaults per card: `PUT /api/boards/{id}/tag-rules` stores an ordered list, each rule naming tags — a card must carry **all** of them, its noteboard tags, matched exactly — and any of the four defaults; per field the first matching rule that sets it wins, blanks fall through to the next matching rule, then to the board. **The precedence lives only in kanban-store** (`model.ResolveEffectiveDefaults`): `GET /api/boards/{id}/cards/{card_id}/effective-defaults`, or `/effective-defaults?tag=…&tag=…`, returns each default with its source (`board`, or `tag_rule` with the rule id and tags); the dispatchers and bridge-ui ask it and never re-implement it. A rule's principal applies only when a card arrives with no assignee, and the `assigned` event names the rule.

## Message triggers

A board carries message triggers: `event_kind` (served by `GET /api/message-trigger-options` — `card_created`, `card_moved`, `card_completed`, `assigned`, `card_held`), an optional `to_column_id` (moves into that column only), an optional `priority_value` (a rung of the board's ladder, by value), a multichat puppet id as recipient, and a Go template — sent through multichat's `POST /api/messages/send`. Edited in the **Message triggers** section of bridge-ui's `kanban/settings` page (`bridge-ui/src/components/BoardMessageTriggersSection.tsx`). A card write never waits on it; each firing writes one `message_deliveries` row, unique on `(trigger_id, event_id)`, with status `sent`, `failed`, `pending` or `not_configured`.

⚠️ **Delivery is off on purpose**: the unit has no `MULTICHAT_URL`, so every firing is recorded `not_configured` with the message it would have sent. To turn it on, put `Environment=MULTICHAT_URL=http://localhost:8402`, `Environment=AUTH_STORE_URL=http://127.0.0.1:8303` and `Environment=AUTH_STORE_TOKEN=…` in a **host-local drop-in**, `~/.config/systemd/user/kanban-store.service.d/multichat.conf` (mode 600) — the same shape as the scheduler's `multichat-send.conf` — then `daemon-reload` and restart. ⚠️ **Not in `systemd/kanban-store.service`**: that template is tracked in the repo, so a token written there gets committed. multichat's token itself is resolved from auth-store provider `multichat` on each send; `MULTICHAT_URL` without `AUTH_STORE_TOKEN` refuses to start. README "Message triggers" is the route table.

## Files on a card

A card's files are kept in **file-store** (`127.0.0.1:8317`), which owns the file — bytes, name, size, type, hash — and decides nothing about who may read it. This store owns `card_attachments`: which card a file hangs on, who put it there, and `visibility`, a note's vocabulary (`internal` when left out). **A file is reached only through its card**: the routes sit under `/api/cards/{id}/attachments`, so the gate has held the caller to the card, and the download checks the file hangs on the card in the path — a file id from another card is 404. **Never add a route that takes a file id alone.** file-store's refusals (400, 413) are relayed as written, its failures are 502 and leave no row, and its download headers — what keeps an upload from running in a browser — pass through unchanged. Purging a card destroys every file file-store holds for it first, removed ones included, or does not happen. Without `FILE_STORE_URL` every attachment route is 503. ⚠️ The token reads every file: it comes from `~/.config/file-store-tokens.env` through the host-local drop-in `kanban-store.service.d/file-store.conf`, never the tracked unit and never the shared principal-gating file. README "Files on a card" is the route table. Holidays, note visibility and hand-logged time entries (`time_logged` events, corrected by `supersedes_event_id`) are in the README too.

# Access and operations

## Who may call it

Every request except `/health` and `OPTIONS` must carry either the service token (`X-Kanban-Store-Service-Token`, internal services only, unrestricted) or `X-Principal-Id`; anything else is **401** (measured 2026-09-18). The principal id is trusted as sent, so callers must reach the store only through a gateway that sets it from a verified identity: llm-bridge-server's `/kanban/` proxy does this for a logged-in user and for an agent session that sends `Authorization: Bearer $LLM_BRIDGE_PRINCIPAL_TOKEN` to `$LLM_BRIDGE_GATEWAY_URL/kanban/…`. Access is per board, from grant-store's `can_view`, `can_edit` and `can_administer` (each includes the one before); an administrator in principal-store is unrestricted. A board or card the principal cannot view is **404**, not 403. A principal-store or grant-store that cannot answer is a 502. README "Who may see which board" has the full rules. ⚠️ README "Security" said "there is no authentication" until 2026-09-18; that was true before the gate and false after it.

# Working in this repo

## Generated TypeScript types

The wire types are rendered to TypeScript, not copied: `./generate-ts.sh` runs tygo over `internal/model` and writes `ts/model.ts`, published source-only as `@kayushkin/kanban-store-types` (`file:../kanban-store/ts`); the Go structs are the source of truth, and a pointer field that is never absent on the wire carries `tstype:"…,required"` so the render does not call it optional. A card's `item` is typed as noteboard's `Item` through `@kayushkin/noteboard-types`, which `ts/package.json` depends on — so `npm install` inside `ts/` must have run on a host before anything imports the package. `CardTimeline`, `EntityCardView` and `MessageTriggerOptions` are named types in Go so the render covers them.
