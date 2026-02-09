#!/usr/bin/env bash
set -euo pipefail

HUB_HOST=${HUB_HOST:-"127.0.0.1"}
HUB_PORT=${HUB_PORT:-8081}
AGENT_ID=${AGENT_ID:-"agent1"}
AGENT_SECRET=${AGENT_SECRET:-"agent-secret"}
SESSION_ID=${SESSION_ID:-"ai"}
TAILNET_ONLY=${TAILNET_ONLY:-0}
TAILSCALE_HTTPS=${TAILSCALE_HTTPS:-1}

if ! command -v tailscale >/dev/null 2>&1; then
  echo "tailscale CLI not found. Install Tailscale first." >&2
  exit 1
fi

cleanup() {
  jobs -p | xargs -r kill >/dev/null 2>&1 || true
}
trap cleanup EXIT

if command -v runeshell >/dev/null 2>&1; then
  RS_FLAGS=(run -addr "${HUB_HOST}:${HUB_PORT}" -auth-mode tailnet -agent-id "$AGENT_ID" -agent-secret "$AGENT_SECRET" -qr=true)
  if [[ "$TAILNET_ONLY" == "1" ]]; then
    RS_FLAGS+=(-tailnet-only)
  fi
  if [[ -n "${BASE_URL:-}" ]]; then
    RS_FLAGS+=(-url "$BASE_URL")
  fi
  exec runeshell "${RS_FLAGS[@]}"
fi

printf "Starting hub (tailnet auth)...\n"
HUB_ADDR="${HUB_HOST}:${HUB_PORT}"
TAILNET_FLAG=""
if [[ "$TAILNET_ONLY" == "1" ]]; then
  TAILNET_FLAG="-tailnet-only"
fi

go run ./cmd/hubd \
  -addr "$HUB_ADDR" \
  -auth-mode tailnet \
  -agent-id "$AGENT_ID" \
  -agent-secret "$AGENT_SECRET" \
  $TAILNET_FLAG \
  >/tmp/hubd.log 2>&1 &

printf "Waiting for hub...\n"
for _ in {1..25}; do
  if curl -s "http://${HUB_ADDR}/" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

printf "Starting agent...\n"
go run ./cmd/agentd \
  -hub "ws://${HUB_ADDR}/ws/agent" \
  -agent-id "$AGENT_ID" \
  -agent-secret "$AGENT_SECRET" \
  >/tmp/agentd.log 2>&1 &

printf "Configuring tailscale serve...\n"
# Expose the local hub to the tailnet (HTTPS by default, or HTTP if TAILSCALE_HTTPS=0).
if [[ "$TAILSCALE_HTTPS" == "1" ]]; then
  tailscale serve --bg --yes --https "$HUB_PORT" >/tmp/tailscale-serve.log 2>&1 || true
else
  tailscale serve --bg --yes --http "$HUB_PORT" >/tmp/tailscale-serve.log 2>&1 || true
fi

DNS_NAME=$(python3 - <<'PY'
import json,subprocess
try:
    raw = subprocess.check_output(['tailscale','status','--json'])
    data = json.loads(raw.decode())
    print(data.get('Self',{}).get('DNSName',''))
except Exception:
    print('')
PY
)

if [[ -n "$DNS_NAME" ]]; then
  if [[ "$TAILSCALE_HTTPS" == "1" ]]; then
    BASE_URL="https://${DNS_NAME}"
  else
    BASE_URL="http://${DNS_NAME}:${HUB_PORT}"
  fi
else
  IP=$(tailscale ip -4 2>/dev/null | head -n 1 || true)
  if [[ -n "$IP" ]]; then
    BASE_URL="http://${IP}:${HUB_PORT}"
  else
    BASE_URL="http://${HUB_ADDR}"
  fi
fi

URL="${BASE_URL}/?mode=hub&agent=${AGENT_ID}&session=${SESSION_ID}"

printf "\nOpen this URL on a tailnet device:\n%s\n\n" "$URL"
if command -v go >/dev/null 2>&1; then
  go run ./cmd/qrprint "$URL"
  printf "\n"
else
  printf "Tip: install Go to print a terminal QR code.\n"
fi
printf "Hub log: /tmp/hubd.log\nAgent log: /tmp/agentd.log\nTailscale serve log: /tmp/tailscale-serve.log\n\n"
printf "Press Ctrl-C to stop.\n"

wait
