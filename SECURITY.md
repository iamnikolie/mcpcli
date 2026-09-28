# Security Policy

## Supported versions

The latest release is the supported one. Fixes land on `main` and go out in the
next tag.

## Reporting a vulnerability

Please do **not** open a public issue for a security problem.

Use GitHub's private vulnerability reporting instead:
[Security → Report a vulnerability](https://github.com/iamnikolie/mcpcli/security/advisories/new).
That opens a private advisory visible only to the maintainers.

Include what you did, what happened, and the impact you think it has. Expect a
first response within a week — this is a spare-time project, not a product with
an on-call rotation.

## Scope notes

Some things are known and by design rather than vulnerabilities:

- **Tokens are stored in plain text** in `~/.mcpcli/<profile>/token.json`
  and bearer tokens / client secrets in `config.yaml`, all at mode 0600 in a
  0700 directory — the same posture as `~/.aws/credentials` or `.netrc`.
  Anyone who can read your home directory can act as you on those servers.
  `logout` deletes the token; revoke it at the provider as well if a machine
  is compromised.
- **The loopback redirect** for OAuth listens on `127.0.0.1` on a random port
  (or `--port`) only for the duration of `login`, checks the `state`
  parameter, and uses PKCE so an intercepted code is useless without the
  verifier.
- **Dynamic client registration** creates a public client at the
  authorization server on first login (and again when the redirect port
  changes). That is how the MCP authorization spec expects clients to work.
- **`--verbose` logs request lines** (method, URL, status). Authorization
  headers and bodies are not logged. Authorization URLs printed by
  `login --no-browser` contain a PKCE challenge and state, not secrets.
- **Tool output is rendered as it arrives.** It is written by third parties.
  The CLI does not sanitize it for the terminal, and an agent reading it
  should treat it as untrusted input (prompt injection).
