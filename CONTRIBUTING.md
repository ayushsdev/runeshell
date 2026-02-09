# Contributing to Runeshell

Thanks for contributing to Runeshell.

## Prerequisites

- Go 1.22+
- Node.js
- tmux

## Local Setup

```bash
make build
./bin/runeshell version
```

## Development Workflow

1. Create a focused branch for your change.
2. Keep changes scoped to a single problem.
3. Add or update tests when behavior changes.
4. Run checks before opening a pull request.

## Required Checks Before PR

```bash
make unit-test
make integration-test
make web-test
make coverage
```

If you changed Go source, run formatting:

```bash
make fmt
```

## Pull Request Guidance

- Describe what changed and why.
- Include manual test notes for behavioral changes.
- Link related issues (for example: `Fixes #123`).
- Keep pull requests small and reviewable when possible.

## Commit and Review Expectations

- Prefer clear, imperative commit messages.
- Do not mix unrelated refactors into feature/fix PRs.
- Be responsive to review feedback and update tests/docs as needed.

## Community and Security

- Follow `CODE_OF_CONDUCT.md`.
- For vulnerability reports, use `SECURITY.md` instead of public issues.
- See `docs/testing.md` for test-layer expectations and coverage policy.
