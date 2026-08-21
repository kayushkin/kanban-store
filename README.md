# kanban-store

A kanban service that stores **where a card sits**, not what it says.

Boards, columns and placements live here. Card *content* — title, body, tags,
priority, due date, hold state — lives in a separate [noteboard][noteboard]
service, and kanban-store holds only the noteboard item id. One item can sit on
several boards at once; editing it anywhere changes it everywhere, because there
is only ever one copy.

On top of that it keeps two cross-cutting indexes: **card links**, pointing a
card at an external entity (a repo, a machine, an agent session), and **entity
tags**, which tag those same entities without involving a card at all.

It is deliberately dumb. It does not proxy to the services those entities belong
to, and it does not check that an entity ref exists. It publishes a registry of
entity types so a client can resolve refs itself.

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
| `PATCH` | `/api/boards/{id}` | Any of `name`, `description`, `archived` |
| `DELETE` | `/api/boards/{id}` | |

### Columns

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/boards/{id}/columns` | In `position` order |
| `POST` | `/api/boards/{id}/columns` | `{"name":…,"position":…,"color":…,"wip_limit":…,"auto_status":…}` |
| `POST` | `/api/boards/{id}/columns/reorder` | `{"columns":[{"id":…,"position":…}]}`; returns the reordered set |
| `GET` `PATCH` `DELETE` | `/api/columns/{id}` | |

`position` is a float, so inserting between two columns means averaging their
positions rather than renumbering the rest.

`auto_status`, when set to `open`, `done` or `archived`, patches the noteboard
item's status whenever a card is created in or moved into that column. This is
how a "Done" column completes the underlying todo.

`wip_limit` is enforced on create, attach and any move that crosses into the
column — over the limit returns **409**. Moving a card *within* a column it
already occupies is not checked, so a full column can still be reordered.

### Cards

A card is a noteboard item plus a placement. Board-scoped operations:

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/boards/{id}/cards` | The assembled board: columns, each with its cards in order, plus `orphans` |
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
| `GET` `POST` | `/api/cards/{id}/links` | `{"entity_type":…,"entity_ref":…,"label":…}` |
| `DELETE` | `/api/links/{linkID}` | |
| `GET` | `/api/entities/{type}/{ref}/cards` | Reverse lookup: every card linked to this entity, oldest link first |

`entity_type` is not validated against the registry and `entity_ref` is never
resolved. `(card_id, entity_type, entity_ref)` is unique, which makes repeated
linking idempotent.

The reverse lookup returns parallel `card_id`/`item` pairs so a caller can spot
orphans — a `null` item means the noteboard item is gone but the link is not.

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

## The noteboard contract

kanban-store calls **seven** noteboard endpoints through **eight** client methods — the
table below has eight rows because a plain `DELETE` and `DELETE …?hard=true` are the same
endpoint with a different query. Seven is the number to implement; count
`http.NewRequest` in `internal/noteboard/client.go` to check it. Anything that answers
these can stand in for noteboard — point `KANBAN_NOTEBOARD_URL` at it. Items are treated
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
