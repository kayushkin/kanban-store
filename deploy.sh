#!/usr/bin/env bash
# Build, install and restart the live kanban-store service, then prove it answers.
#
# The unit is installed FROM systemd/kanban-store.service, and every path this
# script needs (binary, port, database) is read back OUT of that unit rather
# than restated here. There is deliberately no second copy of those values to
# drift: if the unit and this script ever disagree, it is because someone edited
# the unit, and this script follows.
#
# kanban-store.service is a --user unit, so no sudo is involved.
#
# Usage: ./deploy.sh
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
UNIT_SRC="$REPO_DIR/systemd/kanban-store.service"
UNIT_DIR="$HOME/.config/systemd/user"
UNIT_NAME="kanban-store.service"
BACKUP_DIR="$HOME/.local/share/kanban-store-deploy-backup"

# systemctl --user needs these to reach the user manager. Without them it prints
# NOTHING and exits 0 — silence that reads like success. Set them if absent.
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=$XDG_RUNTIME_DIR/bus}"

step() { printf '\n==> %s\n' "$*"; }
fail() { echo "DEPLOY FAILED: $*" >&2; exit 1; }

for bin in go curl jq systemctl; do
  command -v "$bin" >/dev/null 2>&1 || fail "required tool '$bin' not on PATH"
done
[ -f "$UNIT_SRC" ] || fail "missing unit template: $UNIT_SRC"

step "read the unit template — it is the source of truth for these values"
# Resolve systemd's %h specifier ourselves; everything below has to agree with
# what systemd will actually exec.
unit_value() { sed -n "s|^$1=||p" "$UNIT_SRC" | tail -1 | sed "s|%h|$HOME|g"; }
unit_env()   { sed -n "s|^Environment=$1=||p" "$UNIT_SRC" | tail -1 | sed "s|%h|$HOME|g"; }

BIN_PATH="$(unit_value ExecStart | awk '{print $1}')"
WORK_DIR="$(unit_value WorkingDirectory)"
PORT="$(unit_env KANBAN_PORT)"
DB_PATH="$(unit_env KANBAN_DB)"
[ -n "$BIN_PATH" ] || fail "unit has no ExecStart"
[ -n "$PORT" ]     || fail "unit declares no KANBAN_PORT — refusing to guess"
[ -n "$DB_PATH" ]  || fail "unit declares no KANBAN_DB — refusing to guess (an unset one would silently open a DIFFERENT database)"
echo "    binary:   $BIN_PATH"
echo "    port:     $PORT"
echo "    database: $DB_PATH"
BASE="http://127.0.0.1:$PORT"

step "preflight"
# systemd reports a missing WorkingDirectory and a missing binary with the same
# nameless 'result: resources' failure, then crash-loops on it. Name which.
[ -d "$WORK_DIR" ] || fail "WorkingDirectory does not exist: $WORK_DIR"
[ -f "$DB_PATH" ]  || echo "    note: $DB_PATH does not exist yet — it will be created empty"
echo "    WorkingDirectory exists: $WORK_DIR"

step "build (cgo go-sqlite3 — a C compiler is required; see ./Makefile)"
cd "$REPO_DIR"
go vet ./...
make test
STAGED="$REPO_DIR/bin/kanban-store"
make build
[ -x "$STAGED" ] || fail "make build produced no binary at $STAGED"
echo "    built: $(ls -lh "$STAGED" | awk '{print $5}')"

step "boot-and-answer smoke on a throwaway DB, before touching the live one"
# The binary has to prove it can open a database and serve real board/column/card
# state BEFORE it gets installed. `go build` passing says nothing about either
# (a Go 1.22+ ServeMux route conflict compiles green and panics at boot).
./scripts/e2e-smoke.sh >/dev/null || fail "e2e smoke failed — not installing. Run ./scripts/e2e-smoke.sh to see why."
echo "    smoke passed"

step "install"
mkdir -p "$BACKUP_DIR" "$(dirname "$BIN_PATH")" "$UNIT_DIR"
# A fixed-name backup is not a rollback point: verification necessarily runs
# AFTER the new binary is installed, so a re-run of a failed deploy would copy
# the freshly-installed BAD binary over the only good one. Timestamp it and
# never overwrite, and remember exactly which file the live binary came from so
# rollback restores THAT one, not whatever the last run happened to leave.
BACKUP_PATH=""
if [ -f "$BIN_PATH" ]; then
  BACKUP_PATH="$BACKUP_DIR/kanban-store.$(date +%Y%m%d-%H%M%S)"
  cp -p "$BIN_PATH" "$BACKUP_PATH"
  echo "    previous binary backed up to $BACKUP_PATH"
fi
# cp over a running binary fails ETXTBSY. Stage alongside, then rename — which
# is atomic, so there is no window where $BIN_PATH is half-written.
cp "$STAGED" "$BIN_PATH.new"
chmod +x "$BIN_PATH.new"
mv -f "$BIN_PATH.new" "$BIN_PATH"
install -m 0644 "$UNIT_SRC" "$UNIT_DIR/$UNIT_NAME"
systemctl --user daemon-reload
echo "    installed $BIN_PATH and $UNIT_DIR/$UNIT_NAME"

# Restore the pre-deploy binary and bring the old service back. Called when the
# new binary will not answer — a broken deploy must not be left live.
rollback() {
  [ -n "$BACKUP_PATH" ] || { echo "    no prior binary to roll back to (first deploy)"; return; }
  echo "    rolling back to $BACKUP_PATH" >&2
  cp "$BACKUP_PATH" "$BIN_PATH.rb"; chmod +x "$BIN_PATH.rb"; mv -f "$BIN_PATH.rb" "$BIN_PATH"
  systemctl --user restart "$UNIT_NAME" || true
}

step "restart $UNIT_NAME"
systemctl --user restart "$UNIT_NAME"

step "wait for it to answer on $BASE/health"
# Poll. The service opens SQLite and runs migrations before it binds, and that
# is not a fixed cost — a sleep long enough today is a flake tomorrow.
READY=0
for _ in $(seq 1 60); do
  if ! systemctl --user is-active --quiet "$UNIT_NAME"; then
    systemctl --user status "$UNIT_NAME" --no-pager -l | tail -20 >&2
    rollback
    fail "$UNIT_NAME died on startup (see status above) — rolled back"
  fi
  if curl -fsS -o /dev/null --max-time 2 "$BASE/health" 2>/dev/null; then READY=1; break; fi
  sleep 0.25
done
[ "$READY" = "1" ] || { rollback; fail "$UNIT_NAME never answered on $BASE/health within ~15s — rolled back"; }

step "verify the live service"
HEALTH="$(curl -fsS "$BASE/health")"
echo "    /health: $HEALTH"
[ "$(jq -r '.status' <<<"$HEALTH")" = "ok" ] || { rollback; fail "/health is not ok: $HEALTH — rolled back"; }

# Zero boards means the binary opened some OTHER database — a fresh empty one it
# just created. That is the failure this assertion exists for: it would
# otherwise look like a perfectly healthy deploy.
BOARDS="$(jq -r '.counts.boards' <<<"$HEALTH")"
if ! { [ "$BOARDS" -gt 0 ]; } 2>/dev/null; then
  rollback
  fail "/health reports $BOARDS boards — the service is serving an EMPTY database, not $DB_PATH — rolled back"
fi
echo "    serving $BOARDS boards from $DB_PATH"

printf '\n==> DEPLOYED — kanban-store %s is live on %s\n' "$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo '(no git)')" "$BASE"
[ -n "$BACKUP_PATH" ] && echo "    rollback: cp $BACKUP_PATH $BIN_PATH && systemctl --user restart $UNIT_NAME"
