#!/usr/bin/env bash
set -euo pipefail

# Homebrew Go in some local environments omits full coverage tooling.
if ! go tool | grep -qx 'covdata'; then
  if [[ "${CI:-}" == "true" ]]; then
    echo "coverage tooling incomplete: expected 'go tool covdata'" >&2
    exit 1
  fi
  echo "Skipping coverage gates locally: 'go tool covdata' is unavailable in this Go install." >&2
  exit 0
fi

packages=(
  "./internal/hub"
  "./internal/agent"
  "./internal/termserver"
  "./internal/muxframe"
)
thresholds=(85 85 80 80)

fail=0
for i in "${!packages[@]}"; do
  pkg="${packages[$i]}"
  threshold="${thresholds[$i]}"
  out=$(go test -cover "$pkg") || {
    echo "coverage run failed for $pkg" >&2
    echo "$out" >&2
    exit 1
  }
  pct=$(echo "$out" | awk '/coverage:/{gsub("%","",$5); print $5; exit}')
  if [[ -z "${pct:-}" ]]; then
    echo "unable to parse coverage for $pkg" >&2
    echo "$out" >&2
    exit 1
  fi
  if awk "BEGIN {exit !($pct + 0 >= $threshold)}"; then
    echo "$pkg coverage ${pct}% (threshold ${threshold}%)"
  else
    echo "$pkg coverage ${pct}% is below threshold ${threshold}%" >&2
    fail=1
  fi
done

if [[ $fail -ne 0 ]]; then
  exit 1
fi
