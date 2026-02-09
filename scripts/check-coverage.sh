#!/usr/bin/env bash
set -euo pipefail

# Keep coverage cache in a writable location for local and CI parity.
export GOCACHE="${GOCACHE:-/tmp/gocache}"

# Some Go distributions are missing runtime coverage support.
# Probe the exact functionality this script requires before enforcing gates.
coverage_probe_pkg="./internal/muxframe"
if ! coverage_probe_out=$(go test -cover "$coverage_probe_pkg" 2>&1); then
  if echo "$coverage_probe_out" | grep -Eq 'runtime/coverage|internal/coverage/cfile|cannot find package'; then
    if [[ "${CI:-}" == "true" ]]; then
      echo "coverage tooling unavailable in this Go install; cannot run coverage gates in CI." >&2
      echo "$coverage_probe_out" >&2
      exit 1
    fi
    echo "Skipping coverage gates locally: coverage tooling is unavailable in this Go install." >&2
    exit 0
  fi
  echo "coverage probe failed for $coverage_probe_pkg" >&2
  echo "$coverage_probe_out" >&2
  exit 1
fi

packages=(
  "./internal/hub"
  "./internal/agent"
  "./internal/termserver"
  "./internal/muxframe"
)
thresholds=(100 100 100 100)

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
