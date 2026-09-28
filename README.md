# mcpcli

[![CI](https://github.com/iamnikolie/mcpcli/actions/workflows/ci.yml/badge.svg)](https://github.com/iamnikolie/mcpcli/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/iamnikolie/mcpcli.svg)](https://pkg.go.dev/github.com/iamnikolie/mcpcli)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Any remote [MCP](https://modelcontextprotocol.io) server as a command-line
tool, with OAuth profiles per account.

```bash
mcpcli add wispr-work https://api.wisprflow.ai/connect/mcp
mcpcli login wispr-work                      # browser sign-in, PKCE, dynamic registration
mcpcli wispr-work tools                      # what it offers
mcpcli wispr-work search_meetings query="release review" --format text
```

Built for coding agents (Claude Code, Cursor, scripts): instead of loading a
server's tool schemas into every session, the agent runs `mcpcli` from Bash
and reads token-lean Markdown, JSON or plain text. One static Go binary,
no Node, no runtime.

Sibling of [`exa-cli`](https://github.com/iamnikolie/exa-cli),
[`wispr-cli`](https://github.com/iamnikolie/wispr-cli),
[`fibery-cli`](https://github.com/iamnikolie/fibery-cli),
[`gitlab-cli`](https://github.com/iamnikolie/gitlab-cli),
[`slack-cli`](https://github.com/iamnikolie/slack-cli) and
[`svidoq`](https://github.com/iamnikolie/svidoq): same doctrine — plain text
on stdout, nothing costs context until it is called.

## Why a CLI when the client already speaks MCP

- **Context.** An MCP connector loads every tool schema into every
  conversation. `mcpcli` costs nothing until called; `mcpcli skill <profile>`
  generates a skill file the agent loads only when relevant.
- **Rendering.** MCP returns JSON blobs. `mcpcli` renders lists as tables,
  objects as `key: value`, and lets you pick `--fields` or cap `--max-chars`.
- **Composition.** Pipe to `jq`, loop in shell, run from cron, use from any
  agent or none.
- **One login, many clients.** Tokens live in `~/.mcpcli/<profile>/` and are
  shared by every tool on the machine. Each MCP client would otherwise
  authorize separately.
- **Accounts.** The same server under two profile names is two identities:
  `wispr-me` and `wispr-work`.
- **Headless.** `login --device` signs in on a server with no browser.

## Install

**Homebrew:**

```bash
brew trust iamnikolie/tap   # Homebrew 6 refuses untrusted third-party taps
brew tap iamnikolie/tap
brew install iamnikolie/tap/mcpcli
```

**Prebuilt binary** — download the archive for your platform from
[Releases](https://github.com/iamnikolie/mcpcli/releases), then:

```bash
tar xzf mcpcli_*_darwin_arm64.tar.gz
sudo mv mcpcli /usr/local/bin/
```

**With Go** (1.25+):

```bash
go install github.com/iamnikolie/mcpcli@latest
```

**From source** — `make install` symlinks the binary, so a later `make build`
updates the installed CLI:

```bash
git clone https://github.com/iamnikolie/mcpcli.git
cd mcpcli
make install        # symlink → ~/.local/bin/mcpcli
```

## Profiles

A profile is a server URL plus one way to authenticate:

```bash
mcpcli add wispr-me   https://api.wisprflow.ai/connect/mcp            # OAuth (default)
mcpcli add wispr-work https://api.wisprflow.ai/connect/mcp            # same server, other account
mcpcli add deepwiki   https://mcp.deepwiki.com/mcp --no-auth          # open server
mcpcli add linear     https://mcp.linear.app/mcp --bearer-env LINEAR_TOKEN
mcpcli add internal   https://mcp.example.com/mcp -H "X-Team: infra"  # extra headers
mcpcli profiles
```

Profiles live in `~/.mcpcli/<name>/` (override with `MCPCLI_HOME`):
`config.yaml`, `token.json` (mode 0600) and a cached `tools.json`.

## Login

```bash
mcpcli login wispr-work              # opens the browser
mcpcli login wispr-work --no-browser # prints the URL instead
mcpcli login wispr-work --device     # device-code flow for headless machines
mcpcli logout wispr-work             # forget the token, keep the registration
```

`login` follows the MCP authorization spec: it probes the server, reads the
protected-resource metadata from the 401 challenge (RFC 9728), fetches the
authorization-server metadata (RFC 8414 / OIDC discovery), registers
`mcpcli` as a public client (RFC 7591), and runs the authorization-code flow
with PKCE (S256) on a loopback redirect, sending the RFC 8707 `resource`
indicator. Refresh tokens are used automatically before every request that
needs one. Servers without dynamic registration take `--client-id` (and
`--client-secret` for confidential clients).

## Calling tools

```bash
mcpcli wispr-work                                  # = tools
mcpcli wispr-work tools --full                     # parameters for every tool
mcpcli wispr-work tool search_meetings             # one tool, with a call line
mcpcli wispr-work search_meetings query="pricing" limit=5
mcpcli wispr-work get_meeting meeting_id=abc --format text
mcpcli wispr-work add_task title="Follow up" due:='"2026-10-01"'
mcpcli call wispr-work search_meetings --args @query.json
```

Arguments: `key=value` is coerced to the type the tool's schema declares
(integer, number, boolean, arrays split on commas); `key:=json` passes raw
JSON; `nested.key=value` builds objects; `--args '{...}'`, `@file` or `-`
supplies a JSON base that pairs override.

Output: text results print as is. JSON text or structured content renders
as a Markdown table (a list of objects, or a `{"items": [...]}` envelope) or
as `key: value` lines. `--fields a,b` picks columns, `--json` prints the raw
result, `--format text` only the text parts, `--format csv|tsv` for rows,
`--max-chars N` clips long output. A tool error exits 1 with the message on
stderr.

Also: `info` (server name, version, instructions), `resources`, `read <uri>`,
`prompts`, `prompt <name> k=v`.

## Skills for agents

`mcpcli skill` prints the general reference. `mcpcli skill <profile>`
generates a `SKILL.md` for one server from its live tool list: one line per
tool with required arguments, then full parameters. Drop it into
`~/.claude/skills/<name>/SKILL.md` (or your agent's equivalent) and the tool
descriptions cost context only when the agent decides it needs that server.

```bash
mcpcli skill wispr-work -o ~/.claude/skills/wispr-work/SKILL.md
```

## Limits

- Streamable HTTP only, request/response. The standalone SSE stream for
  server-initiated notifications is not opened. Legacy HTTP+SSE servers and
  stdio servers are out of scope (use your client's config for those).
- Each invocation is a fresh session: initialize, request, close. Two HTTP
  round trips per call.
- OAuth requires the server to publish protected-resource metadata. Servers
  that only take a static token use `--bearer` / `--bearer-env`.

## Development

```bash
make build      # ./mcpcli
make test       # go test ./...   (in-process MCP server + fake OAuth server; no network)
make vet
make fmt
```

Built on the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).

## License

MIT — see [LICENSE](LICENSE).
