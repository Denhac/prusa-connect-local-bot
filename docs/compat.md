# What the module relies on from each side

When something breaks after the server, Slack, the Pi camera bots or denhac.org
change, this page says what the module depends on, and which package holds the code.

**Update this page in the same commit as code that starts relying on something
new.** `make compat-live` (phase 1) checks the server's part against a running server.

## The prusa-connect server

Tested with **0.1.0-beta.2**, with the SDK `github.com/prusa3d/prusa-connect/sdk
v0.1.0-beta.2`. Before 1.0 the server reports breaking changes but does not block
them. Read the `[sdk]` lines of each release's `docs/CHANGELOG.md`.

### The module wire (`internal/app`)

| what | details |
|---|---|
| Handshake | one line on stdout, from `wire.Handshake{Protocol: wire.ProtocolMajor, MinMinor, MaxMinor, Address: wire.StdioAddress}`. Then stdout carries only the wire |
| `plugin.info` | `Name` `slack-bot` and `Version` from `module.yaml`. `Serves` names `plugin.configure`, and `routesapi.Describe` adds `routes.serve` with `openapi.json` |
| `plugin.configure` | called once, after `plugin.info`. The settings are the manifest defaults with the operator's values over them. **A key whose default is empty and that nobody set is left out.** Text, duration, URL and secret values arrive as strings, int and bool as JSON values, list and object as JSON. `wirerpc.ConfigureGranted` decodes strictly: a key `settings.Sent` does not have refuses the start. An error refuses the start, and the operator reads it on the instance |
| Settings change | the host restarts the module, so `plugin.configure` comes once per process |
| `plugin.ping` | answered by the SDK. Three misses in a row and the module is restarted |
| `plugin.shutdown` | the host closes the connection when it answers, and kills the process group 5 s after the call began. systemd may send SIGTERM directly |
| `routes.serve` | `GET /status`, through `routesapi.Serve`. A GET needs `view_resource` on the instance |

### Calls the module makes (`internal/connect`)

| what | details |
|---|---|
| `device.list` | as the module itself (no principal). Needs `observe`, which `-approve` grants as the viewer role on the organization. Pages of at most `deviceapi.MaxPageLimit` (200) |

### The manifest (`module.yaml`)

| what | details |
|---|---|
| Required keys | `name`, `kind`, `version`, `license`, `contract_major`, and `openapi` for a module with routes |
| Options | a `required` option left empty fails the install with no retry (`RESOURCE_PLUGIN_SETTINGS`), so every option here has a default |
| Parsing | `manifest.Parse` with `gopkg.in/yaml.v3`, as the server does. `manifest_test.go` parses it the same way |
| Release | the manifest at the root, `<name>_<goos>_<goarch>` per platform, and `checksums.txt` in sha256sum's format listing every other file |

## Slack (`internal/slack`)

Library: `github.com/slack-go/slack v0.36.0` (`socketmode` included).

| what | used for |
|---|---|
| `auth.test` | before each connection: checks the bot token and gives the workspace's `team_id`. `invalid_auth`, `not_authed`, `account_inactive`, `token_revoked` and `not_allowed_token_type` stop the retries until the settings change |
| `apps.connections.open` | through `socketmode.Client.RunContext`, with the app-level token. Its `RunContext` returns when a connection attempt fails, so `Runner.Run` retries with a wait from 5 s to 5 min |
| Socket Mode envelopes | `slash_commands`, acknowledged at once with `Client.Ack` (Slack allows 3 s). `interactive` and `events_api` are acknowledged and ignored for now |
| Slash command payload | `team_id` (another workspace is refused), `user_id`, `text`, `response_url` |
| `users.info` (`users:read`) | `deleted`, `is_restricted`, `is_ultra_restricted`: refusing deactivated users and guests. Cached 10 minutes |
| `response_url` | the answer to a command, as `{"response_type":"ephemeral", "text", "blocks"}` |
| Block Kit | `header`, `section` (plain_text text, mrkdwn fields, at most 10 a section) and `context` |

## The Pi camera API

Not used yet (phase 1). The contract is the sister project's `docs/api.md`, v1.

## denhac.org

Not used yet (phase 2).
