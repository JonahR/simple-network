#!/usr/bin/env bash
# serve-latest.sh: always run the newest commit of a branch that builds and passes tests.
#
# It watches a branch (default: main). When the branch moves, it checks the new
# commit out into a private worktree (.run/checkout), builds every binary under
# cmd/, and runs the tests. If both pass, it restarts the services on the new
# build. If either fails, it keeps serving the last good build and says why.
#
# It never reads your working tree, so half-finished edits from people or agents
# can't break what is running.
#
# Usage:   scripts/serve-latest.sh        (or: make serve)
# Stop:    Ctrl-C, or make serve-stop from another terminal
# Config:  SERVE_BRANCH=main  SERVE_INTERVAL=3  SERVE_SKIP_TESTS=0  SERVE_RUN_DIR=<repo>/.run
#          serve.env (optional, in the served commit) sets env vars for the services.
set -uo pipefail

BRANCH="${SERVE_BRANCH:-main}"
INTERVAL="${SERVE_INTERVAL:-3}"
SKIP_TESTS="${SERVE_SKIP_TESTS:-0}"

# Resolve the main repository even when this script is run from a worktree.
COMMON_DIR="$(git rev-parse --path-format=absolute --git-common-dir)" || exit 1
ROOT="$(dirname "$COMMON_DIR")"
RUN="${SERVE_RUN_DIR:-$ROOT/.run}"
CHECKOUT="$RUN/checkout"
LOGS="$RUN/logs"
mkdir -p "$RUN/builds" "$LOGS"

PIDFILE="$RUN/serve.pid"
if [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  echo "serve-latest is already running (pid $(cat "$PIDFILE")). Stop it with: make serve-stop" >&2
  exit 1
fi
echo $$ > "$PIDFILE"

SERVING=""      # commit currently running
LAST_FAILED=""  # newest commit that failed, so we don't retry it every tick
PIDS=()

say() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }

stop_services() {
  local pid
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null
  done
  for pid in "${PIDS[@]:-}"; do
    [[ -n "$pid" ]] && wait "$pid" 2>/dev/null
  done
  PIDS=()
}

# start_services <build dir> <commit>: start every binary in the build dir.
start_services() {
  local dir="$1" sha="$2" bin name
  PIDS=()
  for bin in "$dir"/*; do
    [[ -x "$bin" && -f "$bin" ]] || continue
    name="$(basename "$bin")"
    (
      # serve.env supplies defaults; variables already set in the environment win.
      if [[ -f "$dir/serve.env" ]]; then
        while IFS= read -r line || [[ -n "$line" ]]; do
          [[ "$line" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]] || continue
          key="${line%%=*}"
          [[ -n "${!key+x}" ]] || eval "export $line"
        done < "$dir/serve.env"
      fi
      export SERVE_COMMIT="$sha"
      cd "$dir" && exec "$bin"
    ) >>"$LOGS/$name.log" 2>&1 &
    PIDS+=("$!")
  done
  sleep 1
  local pid
  for pid in "${PIDS[@]:-}"; do
    kill -0 "$pid" 2>/dev/null || return 1
  done
  return 0
}

cleanup() {
  say "stopping"
  stop_services
  rm -f "$PIDFILE"
  exit 0
}
trap cleanup INT TERM

# deploy <commit>: build and test it; on success swap the running services.
deploy() {
  local sha="$1" short="${1:0:7}" out="$RUN/builds/$1" subject
  subject="$(git -C "$ROOT" log -1 --format=%s "$sha")"
  say "new commit $short: $subject"

  if [[ ! -d "$CHECKOUT" ]]; then
    git -C "$ROOT" worktree add --detach "$CHECKOUT" "$sha" >/dev/null 2>&1 || { say "could not create $CHECKOUT"; return 1; }
  fi
  git -C "$CHECKOUT" checkout --detach --force --quiet "$sha" && git -C "$CHECKOUT" clean -fdq

  rm -rf "$out" && mkdir -p "$out"
  say "  building ./cmd/..."
  if ! (cd "$CHECKOUT" && go build -o "$out/" ./cmd/...) >"$LOGS/build.log" 2>&1; then
    say "  ✗ build failed, still serving ${SERVING:0:7}. Details: .run/logs/build.log"
    sed 's/^/      /' "$LOGS/build.log" | head -15
    rm -rf "$out"; LAST_FAILED="$sha"; return 1
  fi
  if [[ "$SKIP_TESTS" != "1" ]]; then
    say "  testing ./..."
    if ! (cd "$CHECKOUT" && go test ./...) >"$LOGS/test.log" 2>&1; then
      say "  ✗ tests failed, still serving ${SERVING:0:7}. Details: .run/logs/test.log"
      grep -E '^(--- FAIL|FAIL|panic:)|_test.go:[0-9]+' "$LOGS/test.log" | sed 's/^/      /' | head -15
      rm -rf "$out"; LAST_FAILED="$sha"; return 1
    fi
  fi
  [[ -f "$CHECKOUT/serve.env" ]] && cp "$CHECKOUT/serve.env" "$out/serve.env"

  local previous="$SERVING"
  stop_services
  if start_services "$out" "$sha"; then
    SERVING="$sha"; LAST_FAILED=""
    printf '%s\n' "$sha" > "$RUN/serving"
    say "  ✓ now serving $short. Refresh your browser. Logs: .run/logs/"
    # Keep this build and the previous one; drop older ones.
    ls -1t "$RUN/builds" | tail -n +3 | while read -r old; do rm -rf "$RUN/builds/$old"; done
  else
    say "  ✗ $short built but a service exited on start. Details: .run/logs/"
    stop_services
    LAST_FAILED="$sha"
    if [[ -n "$previous" && -d "$RUN/builds/$previous" ]]; then
      start_services "$RUN/builds/$previous" "$previous" && say "  rolled back to ${previous:0:7}"
    fi
    return 1
  fi
}

say "serving the latest good commit of '$BRANCH' (checking every ${INTERVAL}s, Ctrl-C to stop)"
while true; do
  head="$(git -C "$ROOT" rev-parse --verify --quiet "$BRANCH^{commit}")"
  if [[ -n "$head" && "$head" != "$SERVING" && "$head" != "$LAST_FAILED" ]]; then
    deploy "$head"
  fi
  sleep "$INTERVAL" & wait $!
done
