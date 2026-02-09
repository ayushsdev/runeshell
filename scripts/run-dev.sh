#!/usr/bin/env bash
set -euo pipefail

HUB_HOST=${HUB_HOST:-"127.0.0.1"}
HUB_PORT=${HUB_PORT:-8081}
TOKEN_SECRET=${TOKEN_SECRET:-"dev-secret"}
ADMIN_TOKEN=${ADMIN_TOKEN:-"dev-admin"}
AGENT_ID=${AGENT_ID:-"agent1"}
AGENT_SECRET=${AGENT_SECRET:-"agent-secret"}
SESSION_ID=${SESSION_ID:-"ai"}

cleanup() {
  jobs -p | xargs -r kill >/dev/null 2>&1 || true
}
trap cleanup EXIT

PORT=$(go run ./cmd/pickport -preferred "$HUB_PORT")
HUB_ADDR=":${PORT}"

if [[ "$PORT" != "$HUB_PORT" ]]; then
  printf "Port %s busy; using %s instead.\n" "$HUB_PORT" "$PORT"
fi

printf "Starting hub...\n"
go run ./cmd/hubd \
  -addr "$HUB_ADDR" \
  -token-secret "$TOKEN_SECRET" \
  -admin-token "$ADMIN_TOKEN" \
  -agent-id "$AGENT_ID" \
  -agent-secret "$AGENT_SECRET" \
  >/tmp/hubd.log 2>&1 &

HUB_HTTP="http://${HUB_HOST}${HUB_ADDR}"
HUB_WS="ws://${HUB_HOST}${HUB_ADDR}"

printf "Waiting for hub...\n"
for _ in {1..25}; do
  if curl -s "$HUB_HTTP/" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

printf "Starting agent...\n"
go run ./cmd/agentd \
  -hub "${HUB_WS}/ws/agent" \
  -agent-id "$AGENT_ID" \
  -agent-secret "$AGENT_SECRET" \
  >/tmp/agentd.log 2>&1 &

sleep 0.5

TOKEN_JSON=$(curl -s -X POST "${HUB_HTTP}/api/ws-token" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -d "{\"agent_id\":\"${AGENT_ID}\",\"session_id\":\"${SESSION_ID}\",\"write\":true}")

TOKEN=$(echo "$TOKEN_JSON" | sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
if [[ -z "$TOKEN" ]]; then
  echo "Failed to mint token. Response: $TOKEN_JSON"
  exit 1
fi

URL="${HUB_HTTP}/?mode=hub&agent=${AGENT_ID}&token=${TOKEN}"
printf "\nOpen this URL:\n%s\n\n" "$URL"

printf "Hub log: /tmp/hubd.log\nAgent log: /tmp/agentd.log\n\n"
printf "Press Ctrl-C to stop.\n"

wait
