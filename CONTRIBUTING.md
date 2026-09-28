# Contributing

Thanks for taking a look. This is a small, focused CLI — bug reports and pull
requests are welcome, and so is a plain question in an issue.

## Reporting a bug

Include the output of `mcpcli version`, the exact command you ran, and what
you expected instead. `--verbose` logs the HTTP requests (method, URL,
status) to stderr; tokens are never printed. For OAuth problems, the
server's discovery documents help: the `WWW-Authenticate` header of the MCP
endpoint, `/.well-known/oauth-protected-resource` and the authorization
server's `/.well-known/oauth-authorization-server`.

**Never paste tokens, client secrets or tool output containing private data
into a public issue.**

## Pull requests

Before opening one:

```bash
make fmt           # gofmt -w .
make vet           # go vet ./...
make test          # go test ./...
```

CI runs the same three (plus `-race`) on Linux and macOS, so a green local run
usually means a green PR.

House rules:

- **No network in tests.** The suite runs an in-process MCP server (official
  Go SDK) and a fake OAuth authorization server (`internal/oauth/oauthtest`).
  New behaviour gets covered there.
- **One concern per PR.** A bug fix and a refactor in the same diff take three
  times as long to review.
- **Keep the output token-lean.** The default rendering exists so an agent can
  read it without burning context. Extra detail belongs behind `--json`,
  `--fields` or `--full`, not in the default set.
- **Data goes to stdout.** Only errors, hints and `--verbose` logs go to
  stderr, so `x=$(mcpcli ...)` always works.
- **Secrets stay in the profile directory** at mode 0600 and never in logs
  or error messages.
- **Update the docs in the same commit.** Any change to the CLI surface must
  also update `cmd/skill.md` (embedded in the binary, printed by
  `mcpcli skill`, and the single source of truth for command UX) and `README.md`.
- **Conventional commit subjects** — `feat:`, `fix:`, `docs:`, `refactor:`,
  `test:`, `chore:`. Release notes are generated from them.

## Releases

Maintainer-only. Tag and push:

```bash
git tag -a v1.2.3 -m "v1.2.3"
git push origin v1.2.3
```

GoReleaser builds archives for linux/darwin/windows on amd64 and arm64 and
publishes the GitHub release with a generated changelog.
