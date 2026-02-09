# Testing Strategy

Runeshell uses a layered test pyramid:

- Unit tests for package logic (`internal/*`, command helpers)
- Integration tests for hub-agent-client behavior (`integration/`)
- Browser smoke tests with Playwright (`web/e2e/`)

## Local Commands

```bash
make unit-test
make integration-test
make web-test
make test
```

Run browser smoke tests:

```bash
npm install
npm run test:e2e
```

Run all CI-equivalent checks (excluding browser install):

```bash
make ci-test
```

## Coverage Gates

Coverage thresholds are enforced for critical packages by `scripts/check-coverage.sh`.
Current thresholds:

- `./internal/hub`: 100%
- `./internal/agent`: 100%
- `./internal/termserver`: 100%
- `./internal/muxframe`: 100%

Note: Some local Go distributions do not include runtime coverage support. In that
case, coverage gates are skipped locally but remain required in CI.

## Test Writing Rules

- Prefer deterministic tests with bounded timeouts.
- Avoid sleeps unless there is no event-driven alternative.
- Keep networked tests local-only (use `httptest`, ephemeral ports).
- Add regression tests for every bug fix that changes behavior.
- Add integration coverage when changes span hub + agent + web protocol.
