#!/usr/bin/env bash
# Fabricates a synthetic environment for the demo recording: fake home,
# fake projects, staged session states (ok/open/stale/recover/lost).
# Nothing from the operator's real data is involved.
set -euo pipefail

DEMO="${DEMO_ROOT:-/tmp/sp-demo}"
SP_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

rm -rf "$DEMO"
mkdir -p "$DEMO/home" "$DEMO/bin"
export HOME="$DEMO/home"
export CODEX_HOME="$HOME/.codex"
export SESSION_PROTECT_CONFIG="$DEMO/config.toml"
# The fake home has no git identity; backups need one.
export GIT_AUTHOR_NAME=demo GIT_AUTHOR_EMAIL=demo@example.invalid
export GIT_COMMITTER_NAME=demo GIT_COMMITTER_EMAIL=demo@example.invalid

cat > "$DEMO/config.toml" <<EOF
# recordings must be hermetic: no launch update prompt over the demo
[update]
check = false

backup_root = "$DEMO/backup"
EOF

slug() { echo "$1" | sed 's/[^a-zA-Z0-9]/-/g'; }

session() { # project-path uuid title turns topic
  local project="$1" id="$2" title="$3" turns="$4" topic="$5"
  local dir="$HOME/.claude/projects/$(slug "$project")"
  mkdir -p "$dir" "$project"
  {
    printf '{"type":"user","timestamp":"2026-08-14T10:00:00Z","cwd":"%s","sessionId":"%s","message":{"role":"user","content":"%s"}}\n' "$project" "$id" "$title"
    for i in $(seq 1 "$turns"); do
      printf '{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"Step %s: reworked the %s path — the %s tests pass and the edge cases around %s are covered."}],"usage":{"input_tokens":%s,"output_tokens":%s,"cache_read_input_tokens":%s}}}\n' "$i" "$topic" "$topic" "$topic" $((900*i)) $((220*i)) $((15000*i))
      printf '{"type":"user","cwd":"%s","sessionId":"%s","message":{"role":"user","content":"looks good, continue with part %s"}}\n' "$project" "$id" "$i"
    done
    printf '{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"Done. All %s parts are merged and green."}],"usage":{"input_tokens":500,"output_tokens":90}}}\n' "$turns"
  } > "$dir/$id.jsonl"
  printf '{"display":"%s","sessionId":"%s","project":"%s","timestamp":%s}\n' "$title" "$id" "$project" "$(date +%s)000" >> "$HOME/.claude/history.jsonl"
}

P1="$HOME/projects/orbit-api"
P2="$HOME/projects/atlas-web"
P3="$HOME/projects/tooling"

session "$P1" "0a1b2c3d-1111-4a4a-8a8a-000000000001" "add jwt refresh flow to the auth service" 6 "auth token"
session "$P1" "0a1b2c3d-1111-4a4a-8a8a-000000000002" "debug the flaky websocket reconnect" 4 "websocket reconnect"
session "$P1" "0a1b2c3d-1111-4a4a-8a8a-000000000003" "rate limiting for the public endpoints" 3 "rate limiter"
session "$P2" "0a1b2c3d-2222-4a4a-8a8a-000000000004" "dark mode for the settings pages" 5 "theme toggle"
session "$P2" "0a1b2c3d-2222-4a4a-8a8a-000000000005" "migrate the build to vite" 3 "vite build"
session "$P3" "0a1b2c3d-3333-4a4a-8a8a-000000000006" "release script for the cli" 2 "release script"

# A lost session: history only, no transcript anywhere.
printf '{"display":"prototype the billing webhooks","sessionId":"0a1b2c3d-9999-4a4a-8a8a-000000000009","project":"%s","timestamp":%s}\n' "$P2" "$(date -v-6d +%s)000" >> "$HOME/.claude/history.jsonl"

# Ages: the open session stays fresh; everything else spreads out.
touch -t "$(date -v-3H +%Y%m%d%H%M)" "$HOME/.claude/projects/$(slug "$P1")/0a1b2c3d-1111-4a4a-8a8a-000000000002.jsonl"
touch -t "$(date -v-3H +%Y%m%d%H%M)" "$HOME/.claude/projects/$(slug "$P1")/0a1b2c3d-1111-4a4a-8a8a-000000000003.jsonl"
touch -t "$(date -v-1d +%Y%m%d%H%M)" "$HOME/.claude/projects/$(slug "$P2")/0a1b2c3d-2222-4a4a-8a8a-000000000004.jsonl"
touch -t "$(date -v-3d +%Y%m%d%H%M)" "$HOME/.claude/projects/$(slug "$P2")/0a1b2c3d-2222-4a4a-8a8a-000000000005.jsonl"
touch -t "$(date -v-5d +%Y%m%d%H%M)" "$HOME/.claude/projects/$(slug "$P3")/0a1b2c3d-3333-4a4a-8a8a-000000000006.jsonl"

# Build the demo binary and take the first backup.
( cd "$SP_SRC" && go build -ldflags "-X github.com/rexovas/session-protect/internal/version.Version=v1.0.1 -X github.com/rexovas/session-protect/internal/version.Channel=release" -o "$DEMO/bin/session-protect" ./cmd/session-protect )
ln -sf "$DEMO/bin/session-protect" "$DEMO/bin/sp"
"$DEMO/bin/session-protect" backup >/dev/null

# One deleted-but-recoverable session, one stale (post-backup writes).
rm "$HOME/.claude/projects/$(slug "$P3")/0a1b2c3d-3333-4a4a-8a8a-000000000006.jsonl"
printf '{"type":"user","cwd":"%s","sessionId":"0a1b2c3d-1111-4a4a-8a8a-000000000003","message":{"role":"user","content":"one more tweak please"}}\n' "$P1" >> "$HOME/.claude/projects/$(slug "$P1")/0a1b2c3d-1111-4a4a-8a8a-000000000003.jsonl"
touch -t "$(date -v-40M +%Y%m%d%H%M)" "$HOME/.claude/projects/$(slug "$P1")/0a1b2c3d-1111-4a4a-8a8a-000000000003.jsonl"

# An open session: a live pid in the registry makes the row gold.
sleep 600 &
KEEPER=$!
mkdir -p "$HOME/.claude/sessions"
printf '{"pid":%s,"sessionId":"0a1b2c3d-1111-4a4a-8a8a-000000000001","cwd":"%s","status":"idle","updatedAt":%s}\n' "$KEEPER" "$P1" "$(date +%s)000" > "$HOME/.claude/sessions/$KEEPER.json"

echo "demo env ready at $DEMO (keeper pid $KEEPER)"
