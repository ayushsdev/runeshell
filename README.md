# Runeshell

Runeshell is a remote terminal bridge for sharing tmux-backed sessions in a browser.
It includes:

- A hub (`runeshell hub`) that serves the web UI and brokers WebSocket traffic
- An agent (`runeshell agent`) that runs terminal sessions locally
- A combined mode (`runeshell run`) that starts both together

## Status

This project is in active development and open to community contributions.

## Features

- Browser terminal UI backed by local tmux sessions
- Agent/hub split for local or remote deployment
- Token auth mode or tailnet-only auth mode
- Session lock/unlock controls
- Shareable URL + terminal QR output

## Requirements

- Go 1.22+
- Node.js (to run web tests via `node --test`)
- tmux (for terminal session attach/use)
- Optional: Tailscale CLI for tailnet workflows

## Quick Start

Build the CLI:

```bash
make build
```

Run hub + agent together:

```bash
./bin/runeshell run
```

The command prints a URL and QR code to open in a browser.

## Common Commands

Run all tests:

```bash
make test
```

Run unit + integration + web checks used in CI:

```bash
make ci-test
```

Run Go tests only:

```bash
make go-test
```

Run web tests only:

```bash
make web-test
```

Run browser smoke tests:

```bash
npm install
npm run test:e2e
```

Format Go code:

```bash
make fmt
```

Install the CLI to your Go bin path:

```bash
make install
```

## CLI Overview

```text
runeshell <command> [args]

Commands:
  run      Start hub + agent together
  hub      Start hub only
  agent    Start agent only
  attach   Attach local tmux session
  lock     Disable web input (admin token required)
  unlock   Re-enable web input (admin token required)
  qr       Print QR code for a URL
  version  Print version
```

## Security Notes

Default values like `-token-secret dev-secret`, `-admin-token dev-admin`, and
`-agent-secret agent-secret` are for local development only.
Set strong secrets for non-local environments.

Report vulnerabilities using the process in `SECURITY.md`.

## Contributing

See `CONTRIBUTING.md` for setup, workflow, and pull request expectations.
Community standards are in `CODE_OF_CONDUCT.md`.

## Additional Docs

- `docs/tailscale.md` for tailnet-first setup
- `docs/testing.md` for the testing pyramid, coverage, and CI checks

## License

Licensed under the MIT License. See `LICENSE`.
