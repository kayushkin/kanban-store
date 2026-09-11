# kanban-store

A kanban service that stores **where a card sits and what has happened to it**.

Boards, columns and placements live here. Card *content* — title, body, tags,
priority, due date, hold state — lives in a separate [noteboard][noteboard]
service, and kanban-store holds only the noteboard item id. One item can sit on
several boards at once; editing it anywhere changes it everywhere, because there
is only ever one copy.

Its history lives here, though. Every action on a card — created, mail attached,
handed to an agent, note written, moved, held, finished — is one row in an
append-only log, and each row carries what that action meant for the card's
clock. That log is what answers *how long has this taken, how much of it was time
the work could actually be done, and is that inside the limit its priority sets*.
See [Time accounting](#time-accounting).

On top of that it keeps three cross-cutting indexes: **card links**, pointing a
card at an external entity (a repo, a machine, an agent session), **card
assignments**, saying which principals are on a card, and **entity tags**, which
tag those same entities without involving a card at all.

It is deliberately dumb. It does not proxy to the services those entities belong
to, and it does not check that an entity ref exists. It publishes a registry of
entity types so a client can resolve refs itself. The one exception is a card
assignment, which is checked against principal-store before it is written — see
[Card assignments](#card-assignments) for why that one reference is different.

[noteboard]: https://github.com/kayushkin/noteboard

## Why the split

A card on a board and a todo in a list are the same object seen from two angles.
Storing the text in the board makes the board the owner, and then every other
view of that work — a todo list, a search, an agent picking up a task — is
either a copy that drifts or a query that has to know about kanban. Keeping the
text in noteboard and only the *position* here means a card can be created,
completed or held by something that has never heard of a board.

The cost is a hard runtime dependency, described in full under
[The noteboard contract](#the-noteboard-contract).

## Requirements

- Go 1.24+
- A C compiler — the SQLite driver is [`mattn/go-sqlite3`][sqlite3], which is cgo
- A running noteboard (or anything implementing the seven endpoints below)

[sqlite3]: https://github.com/mattn/go-sqlite3

## Running it

```sh
make build
KANBAN_NOTEBOARD_URL=http://localhost:8191 ./bin/kanban-store
```

| Variable | Default | Meaning |
|---|---|---|
| `KANBAN_PORT` | `8305` | Port to listen on |
| `KANBAN_DB` | `$HOME/.kanban-store/kanban-store.db` | SQLite file; created with its schema on first run |
| `KANBAN_NOTEBOARD_URL` | `http://localhost:8191` | Base URL of the noteboard service |
| `PRINCIPAL_STORE_URL` | `http://127.0.0.1:8314` | Base URL of principal-store, asked once per card assignment whether the principal exists |

`systemd/kanban-store.service` is a `--user` unit. `deploy.sh` reads the binary
path, port and database path back *out of* that unit rather than restating them,
builds, runs the test suite and an end-to-end smoke on a throwaway database,
installs, restarts, and rolls back to a timestamped backup if the new binary
will not answer.

⚠️ **`deploy.sh` will not complete a first-ever deploy against an empty
database.** Its final assertion is that `/health` reports more than zero boards,
which exists to catch a binary that silently opened the wrong database file.
On a fresh install that assertion is a false alarm and the deploy rolls back.
Create a board first, or drop that check.

## Security

**There is no authentication and no authorization.** Every endpoint is open to
anyone who can reach the port, and `Access-Control-Allow-Origin` is `*`
(`internal/api/api.go:57`). The server binds every interface, not loopback
(`cmd/kanban-store/main.go:43`).

That is a deliberate fit for one trusted host behind a firewall, and it is
**unsafe to expose to a network you do not control**. Put it behind a reverse
proxy that authenticates, or bind it to loopback, before it can be routed to.

## HTTP API

All request and response bodies are JSON.

### Health

| Method | Path | Notes |
|---|---|---|
| `GET` | `/health` | `{"status":"ok","counts":{…}}` — row counts per table |

### Boards

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/boards` | `?include_archived=true` to include archived boards |
| `POST` | `/api/boards` | `{"name":…,"description":…}`; `name` required |
| `GET` | `/api/boards/{id}` | |
| `PATCH` | `/api/boards/{id}` | Any of `name`, `description`, `archived`, `business_hours`, `default_principal_id`, `default_agent_id`, `default_instance_id`, `default_bundle_id`, `classifier` — see [Board settings](#board-settings) |
| `DELETE` | `/api/boards/{id}` | |

### Columns

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/boards/{id}/columns` | In `position` order |
| `POST` | `/api/boards/{id}/columns` | `{"name":…,"position":…,"color":…,"wip_limit":…,"auto_status":…}` |
| `POST` | `/api/boards/{id}/columns/reorder` | `{"columns":[{"id":…,"position":…}]}`; returns the reordered set |
| `GET` `PATCH` `DELETE` | `/api/columns/{id}` | |
| `GET` | `/api/columns/{id}/cards` | One column a page at a time: `?limit=&offset=`, with the column's `total` |

`position` is a float, so inserting between two columns means averaging their
positions rather than renumbering the rest.

`auto_status`, when set to `open`, `done` or `archived`, patches the noteboard
item's status whenever a card is created in or moved into that column. This is
how a "Done" column completes the underlying todo.

`wip_limit` is enforced on create, attach and any move that crosses into the
column — over the limit returns **409**. Moving a card *within* a column it
already occupies is not checked, so a full column can still be reordered.

`budget_clock_state` is what landing in this column means for the card's clock:
`running` (the work is ours to do), `paused` (someone else has the ball) or
`stopped` (it is finished). Unset means a move here says nothing about the clock
and leaves it where it was — the honest default for a column nobody has
classified, and the reason moving between two unclassified columns never invents
a change.

### Cards

A card is a noteboard item plus a placement. Board-scoped operations:

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/boards/{id}/cards` | The assembled board: columns, each with its cards in order, plus `orphans`. `?limit=` caps **each column** |
| `POST` | `/api/boards/{id}/cards` | Creates the noteboard item **and** places it; `title` and `column_id` required |
| `PUT` | `/api/boards/{id}/cards/{cardID}` | Attaches an *existing* noteboard item; 404 if that item does not exist |
| `DELETE` | `/api/boards/{id}/cards/{cardID}` | Detaches from this board only; the item survives |

Card-scoped operations, across every board the card is on:

| Method | Path | Notes |
|---|---|---|
| `PATCH` | `/api/cards/{id}` | Forwarded to noteboard unchanged — edit title, body, tags, anything |
| `DELETE` | `/api/cards/{id}` | Reversible; `?hard=true` purges the item and drops every placement |
| `POST` | `/api/cards/{id}/move` | `{"board_id":…,"column_id":…,"position":…}` |
| `GET` | `/api/cards/{id}/placements` | Every board this card appears on |
| `POST` | `/api/cards/{id}/hold` | `{"reason":…}` — park the work |
| `POST` | `/api/cards/{id}/unhold` | Release it |

**Hold is the stop/play button, and it lives on the noteboard item, not on the
board.** That is what lets a card be parked in *any* column instead of only by
being dragged into a designated gate, and what makes the pause bind on the
noteboard discovery path — where an agent looks for work and never sees a board
at all. A held card stays visible on its board, since a board is the surface a
human uses to find parked work and resume it.

Held-ness is inherited down noteboard's `parent_id`, and so is the
`auto_hold_at_usd` spend ceiling passed through on create. A sub-card created
without `parent_id` escapes both.

### Card links

| Method | Path | Notes |
|---|---|---|
| `GET` `POST` | `/api/cards/{id}/events` | The action log. `POST {"kind":…,"clock_state":…,"occurred_at":…,"actor":…,"summary":…}` |
| `GET` `POST` | `/api/cards/{id}/notes` | Status updates and summaries written onto the card |
| `GET` | `/api/cards/{id}/timeline` | `?board_id=` — every action in order, with the time between them and the totals |
| `DELETE` | `/api/notes/{noteID}` | Removes the note's text; its `note_added` event stays |
| `GET` `PUT` | `/api/boards/{id}/priority-levels` | The board's priority ladder |
| `GET` `POST` | `/api/cards/{id}/links` | `{"entity_type":…,"entity_ref":…,"label":…,"occurred_at":…,"clock_state":…}` |
| `DELETE` | `/api/links/{linkID}` | |
| `GET` | `/api/entities/{type}/{ref}/cards` | Reverse lookup: every card linked to this entity, oldest link first |

`entity_type` is not validated against the registry and `entity_ref` is never
resolved. `(card_id, entity_type, entity_ref)` is unique, which makes repeated
linking idempotent.

The reverse lookup returns parallel `card_id`/`item` pairs so a caller can spot
orphans — a `null` item means the noteboard item is gone but the link is not.

### Card assignments

Who is on a card. An assignment is its own fact about the card and not a card
link: a link's `label` is a display name and its uniqueness is per entity ref,
and neither is what "assigned" means. Principals are owned by the
`principal-store` service; kanban-store stores the id it minted
(`principal_000001`) and never the name. There is no role — version one answers
"who is assigned", and owner versus reviewer is a later question.

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/cards/{id}/assignments` | `[{"card_id":…,"principal_id":…,"assigned_by":…,"created_at":…}]`, oldest first |
| `PUT` | `/api/cards/{id}/assignments/{principal_id}` | Idempotent: **201** + the row when this call created it, **200** + the unchanged row when it was already there. `assigned_by` is `?actor=` |
| `DELETE` | `/api/cards/{id}/assignments/{principal_id}` | **204**; **404** if the principal was not on the card |
| `GET` | `/api/assignments?principal_id=…` | Reverse lookup: every card this principal is on, across every board, oldest first. **400** without the parameter |

Assignments ride along on every card view — `assignments` on each card in
`GET /api/boards/{id}/cards` and `GET /api/columns/{id}/cards` — and the field is
omitted when nobody is on the card.

**This is the one write on which kanban-store checks a reference against the
service that owns it.** On `PUT`, `principal_id` must match `^principal_\d{6,}$`
(**400** otherwise, before anything is called), and then
`GET {PRINCIPAL_STORE_URL}/principals/{principal_id}` is asked, with a 3-second
timeout:

- **404** from principal-store → **400** `principal_id principal_000099 does not exist in principal-store`
- `disabled_at` set → **400** `principal_id … is disabled in principal-store`
- unreachable, timed out, or any other non-200 → **502** `principal-store check failed: …` carrying the transport error verbatim, and **the row is not written**

A link to a session that has gone is a dangling pointer a reader can notice; an
assignment to a principal that does not exist is a silently wrong row forever,
reported by the reverse lookup as work for someone who is not there. Refusing
the write when the check cannot run is the point, not a limitation — the store
never accepts an assignment because it could not ask.

Assigning and unassigning are actions on the card and join its timeline as
`assigned` / `unassigned` events: `summary` is the principal id, `detail` is
`{"principal_id":…}`, and neither carries a board because who is on the card is
true wherever it sits. Only the **201** path of a `PUT` logs one. Neither moves
the clock — see [What each action means for the clock](#what-each-action-means-for-the-clock).

### Entity tags

Tag any `(entity_type, entity_ref)` pair without involving a card — mark a
session `p0`, a machine `lab`.

| Method | Path | Notes |
|---|---|---|
| `GET` `POST` | `/api/entities/{type}/{ref}/tags` | `{"tag":…}` |
| `DELETE` | `/api/entities/{type}/{ref}/tags/{tag}` | |
| `GET` | `/api/tags` | Every tag with its count; `?tag=…&entity_type=…` searches instead |

### Discovery and search

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/entity-types` | The registry: `[{"type":…,"service":…,"search":…}]` |
| `GET` | `/api/search?q=…` | Full-text, delegated to noteboard; `&limit=`, `&board_id=`, `&on_board=true` |

`/api/entity-types` is how a client discovers where to resolve a ref it finds in
a link or a tag: which service owns that type, and what to append a query to. An
empty `service` means kanban knows the type but no upstream can autocomplete it
— `repo` is a filesystem path, `git_repo` a remote URL. An empty `search` on a
type that *has* a service means that service has no search route, so advertising
one would promise a lookup that does not exist. Edit
`internal/config/entity_types.go` to match your own stack; the list shipped here
is the author's.

`/api/search` passes the query to noteboard and, with `board_id` or
`on_board=true`, keeps only results that are actually placed somewhere.

## Time accounting

Three numbers, from one log.

A card's events are walked in order, and **the time between one event and the
next belongs to the state that event put the card into**. Summing the `running`
stretches gives the time the work was actually available to be done; summing
everything gives wall-clock elapsed; the difference is time spent waiting on
somebody else.

The worked example, which lives as a test:

| At | Action | Clock |
|---|---|---|
| 09:00 | mail arrives on a P0 card | running |
| 09:30 | replied, waiting on legal | paused |
| 09:30 **+24h** | legal answers | running |
| +1h | answered the client, done | stopped |

**25.5 hours elapsed. 1.5 hours on the budget clock. A 2-hour limit, met.**
Judging that card on elapsed time alone would have called it thirteen times over
its limit.

Nothing is stored but the events. Every figure is recomputed on read, so there
are no totals to drift out of step with the log, and reclassifying a column
changes what happens next rather than rewriting what already did — each event
keeps the clock state that was true when it happened.

### What each action means for the clock

Everything that puts work in front of us runs the clock (`card_created`,
`card_attached`, `email_received`, `agent_dispatched`, `agent_finished`,
`note_added`, `card_unheld`, `waiting_ended`); everything that hands the ball over
pauses it (`card_held`, `waiting_started`); only finishing stops it
(`card_completed`, `card_detached`). A move takes its state from the destination
column, because that is where a column's classification is for.

`assigned` and `unassigned` say who is on the card, not whether the work is
runnable, so they carry the clock forward unchanged: each is recorded with the
state the card is already in, and a paused card stays paused and a finished card
stays finished through either. "Already in" means the card's most recent action
on any board; for a card with no history yet — most cards here predate the log —
it is the classification of the column the card sits in, and only a card with no
history in an unclassified column is recorded `running`, the same answer the
store gives any first action on such a card. Like `card_moved`, the two kinds
have no default of their own — posting one by hand to `/events` without a
`clock_state` is a **400**.

`kind` is deliberately open — record an action this service has never heard of and
it is kept. What an unknown kind **cannot** do is guess its own clock state, so
one without an explicit `clock_state` is a **400** rather than a silently invented
budget figure.

Attaching mail or a session is an action and joins the timeline. `email_msgid` is
the same arrival under its RFC identity and would double-count it, `email_sender`
is a learned affinity rather than an event, and a repo or machine link is a fact
about the card — none of the three is logged.

`occurred_at` on a link backdates that action, for the same reason the field
exists on an event: a classifier reads a mailbox on a cadence, so the mail it
files arrived before anything here heard about it. Without it, attaching
week-old mail reported it as arriving now, and a card built by filing a backlog
carried a timeline that began the moment it was filed.

It does **not** move the link's own `created_at`. When the link was recorded and
when the thing happened are two different facts, and events here already keep
them apart as `recorded_at` and `occurred_at`.

`clock_state` overrides what the arrival means for the clock. The link type
already implies one — mail arriving is work landing in front of you, so it runs
the clock — and that is right for a card someone works and wrong for a bucket.
A hundred build digests filed on one No-action card are not a hundred arrivals
of work, and letting them run the clock reports a card nobody has ever touched
as having consumed weeks of budget.

Sending either field with a link type that records no action is a **400**, not a
no-op. Backdating a `repo` link is a caller misunderstanding what the link is,
and answering 201 would let it believe it had moved something on a timeline it
never touched.

### Priority ladders

A board's ladder maps a noteboard priority onto a name and a time limit:

```sh
curl -X PUT localhost:8305/api/boards/$BOARD/priority-levels -d '{"levels":[
  {"priority_value":5,"label":"P0","budget_seconds":7200},
  {"priority_value":4,"label":"P1","budget_seconds":28800},
  {"priority_value":3,"label":"P2","budget_seconds":86400},
  {"priority_value":2,"label":"P3","budget_seconds":604800},
  {"priority_value":1,"label":"P4","budget_seconds":2592000}]}'
```

**P0 is the top rung, and the top rung is the HIGHEST `priority_value`.** noteboard
sorts priority descending and every list view in the stack depends on that, so the
P-number is a label on a rung, not the number in the database.

⚠️ **`priority_value` 0 is reserved and rejected with a 400.** Zero is where
noteboard leaves every card nobody has ranked — the large majority of them — so a
level defined there would promote the entire unranked backlog to most-urgent. An
unranked card gets no rung, no label and no limit.

Each board sets its own ladder, and **a board with no levels ignores priorities
entirely**: its cards carry no limit, whatever their stored priority.

### Business hours

A board may declare a working week, and then reports the same elapsed figures a
second way alongside the wall-clock ones — never instead of them:

```sh
curl -X PATCH localhost:8305/api/boards/$BOARD -d '{"business_hours":
  {"tzid":"America/Los_Angeles","days":["MO","TU","WE","TH","FR"],"start":"09:00","end":"17:00"}}'
```

`tzid` is mandatory and never defaulted, for the reason noteboard's recurrence
rules demand one: an offset is not a zone, and hours anchored to one drift an hour
twice a year. A board without hours reports no business figures at all rather than
a guessed nine-to-five. Send `{"business_hours":{}}` to clear them.

### Board settings

A board carries the defaults a dispatcher or classifier used to take as flags
on a cron job, so the board is the one place the answer lives and the scheduler
owns only *when* a job runs:

```sh
curl -X PATCH localhost:8305/api/boards/$BOARD -d '{
  "default_principal_id": "principal_000004",
  "default_agent_id":     "17",
  "default_instance_id":  "inst-cc-local",
  "default_bundle_id":    "6",
  "classifier": {"vocabulary":"work","mail_account_ids":["demo-work"],"hold_new_cards":true}}'
```

| Field | Owner asked before the write | Read by |
|---|---|---|
| `default_principal_id` | principal-store (`PRINCIPAL_STORE_URL`) — must exist and not be disabled | kanban-store itself, see below |
| `default_agent_id` | llm-bridge-server (`LLM_BRIDGE_URL`) `GET /agents` — agent-store's **numeric id**, never the slug, which is renameable | the dispatcher that spawns sessions for the board's cards |
| `default_instance_id` | llm-bridge-server `GET /instances/{id}` | the same dispatcher |
| `default_bundle_id` | bundle-store (`BUNDLE_STORE_URL`) `GET /bundles/{id}` — the **numeric id**, not the bundle's name | the dispatcher, which sends it as `bundle_id` on llm-bridge-server's `POST /sessions`; the spawn resolves it through bundle-store and provisions the bundle's tools |
| `classifier.vocabulary` | nobody here — email-classifier owns its vocabularies and refuses a board naming one it lacks (`email-classifier -list-vocabularies`) | email-classifier |
| `classifier.mail_account_ids` | nobody here — mailstack is behind a token this store does not hold; the classifier checks them at run time. Explicit, never "every account" | email-classifier |
| `classifier.hold_new_cards` | — | email-classifier |

An owner that says the id does not exist is a **400** and an owner that could
not be asked is a **502**; nothing is written on either. An empty string clears
an id and `{"classifier":{}}` clears the classifier; a cleared setting is absent
from the board on the wire, not an empty string. Omitting a field leaves it
alone.

**The default principal is applied by kanban-store, not by the caller.** When
a card is created on the board or attached to it and has no assignee, the
board's default goes on it — as an assignment row and an `assigned` event whose
detail says `"source":"board_default"`, with `assigned_by` the creating actor.
A card that already has someone on it is left alone: a default fills a blank
and never overrides a person's choice. The default is re-checked with
principal-store on every application, before the noteboard item is created, so
a person disabled since the setting was made refuses the card with a 400 that
names `default_principal_id` rather than filing new work to someone who has
left.

### Paging a board

Every column carries a `total`, so a client can say *showing 25 of 6,466* rather
than presenting a page as the whole column. `?limit=` on the board view caps each
column; `/api/columns/{id}/cards?limit=&offset=` fetches the rest.

Without a limit the board view still returns everything, so existing callers are
unaffected — but on this host's largest board that is **12 MB and 1.6 seconds per
read**, on a page that polls every fifteen seconds.

⚠️ **Paging is in stored order, which is not the order a board displays.** What a
client sorts by — priority, due date, title — lives in noteboard, so ordering a
whole board here would mean fetching every item on it, which is the cost paging
exists to avoid. A client showing a page therefore sorts what it has, and has to
say so on screen. Sorting the full column server-side needs a batch item read on
noteboard (`GET /api/items?ids=…`), which does not exist yet — see [The noteboard
contract](#the-noteboard-contract).

## The noteboard contract

kanban-store calls **seven** noteboard endpoints. Anything that answers these can
stand in for noteboard — point `KANBAN_NOTEBOARD_URL` at it. Items are treated
as opaque JSON and passed through untouched, so an implementation may carry any
extra fields it likes; the only one kanban-store reads is `id`.

| Called as | Endpoint | Expects | Used for |
|---|---|---|---|
| `CreateItem` | `POST /api/items` | **201**, the created item with an `id` | Creating a card |
| `GetItem` | `GET /api/items/{id}` | **200**, the item; **404** if missing | Assembling a board, verifying an attach |
| `PatchItem` | `PATCH /api/items/{id}` | **200**, the updated item | `PATCH /api/cards/{id}`, and `auto_status` on move |
| `DeleteItem` | `DELETE /api/items/{id}` | **200** | Deleting a card |
| `DeleteItem(hard)` | `DELETE /api/items/{id}?hard=true` | **200** | Purging a card |
| `HoldItem` | `POST /api/items/{id}/hold` | **200**, the item | Parking work |
| `UnholdItem` | `POST /api/items/{id}/unhold` | **200**, the item | Releasing it |
| `Search` | `GET /api/search?q=…&include_held=true&limit=…` | **200**, an array of items | `/api/search` |

Behaviors it relies on:

- **`POST /api/items` accepts** `type`, `title`, `body`, `tags`, `priority`,
  `list_id`, `due_at`, `parent_id`, `hold`, `hold_reason`, `auto_hold_at_usd`,
  and returns the stored item. Cards are created with `type: "todo"`.
- **`status` takes `open`, `done` or `archived`** — those are the values
  `auto_status` patches, and the only ones a column will accept.
- **A plain `DELETE` is reversible**: it hides the item from reads without
  destroying the row, and does not change its status. kanban-store leaves the
  placements in place on a plain delete precisely because the delete can be
  undone, and drops them only on `?hard=true`. Until a restore, the card still
  holds its slot with a `null` item.
- **`include_held=true` reveals held items in search.** kanban-store always sets
  it: a board must be able to show the work parked on it, or a card could never
  be un-parked.
- **There is no batch item endpoint.** `GetItems` fans out concurrent single
  GETs, capped at 8, and returns results in input order with `null` for each
  404. If you implement your own backend, one round trip per card on a board is
  the load to expect.

## Development

```sh
make test            # go test ./...
./scripts/e2e-smoke.sh   # boots the real binary against a stub noteboard
./deploy.sh          # vet, test, smoke, install, restart, verify, roll back on failure
```

The end-to-end smoke starts the compiled binary on a throwaway database with a
stub noteboard, then exercises the board lifecycle over real HTTP. It is what
catches the failures `go build` cannot: a Go 1.22+ `ServeMux` route conflict
compiles green and panics at boot.

## License

MIT — see [LICENSE](LICENSE).
