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
| Device state paths | `/state` (the machine's word: `IDLE`, `PRINTING`, `ATTENTION`, …), `/tools/0/nozzle_temp`, `/tools/0/nozzle_target_temp`, `/temperature/bed`, `/temperature/bed_target`. Also `Issues` and `Dialog` on the device |
| `device.jobs` | the print on the bed and the one before it. `display_name`, else the base of `path`, is the file shown. `time_remaining` is seconds |
| `device.job_history` | per printer (`id` set), `from` a moment, newest first. A job carries no device id, so it is asked per printer |
| `bus.subscribe` | as the module itself, the 8 types in `connect.Types()`, a 1 s window. The cursor is kept in memory: the server's replay is 32 events or 32 KiB per origin and lost when the server restarts, after which a window carries `resync: true` |

### Events (`internal/connect/events.go`, test data in `testdata/server/0.1.0-beta.2/events/`)

| type | read |
|---|---|
| every device event | the printer is the envelope's `subject`, `device:<id>` (`event.ParseOrigin`). The payloads also carry `device_id`, except the job events |
| `device.job.started` | `event.JobStarted`: `job_id`, `display_name`, `path`, `started` |
| `device.job.settled` | `event.JobSettled`: as above, plus `ended`, `outcome` (finished, failed, cancelled, unknown), `time_printing_s` and `filament_used_mm` with their `_known` flags |
| `device.issues.changed` | `issues` (`reason`, `severity`, `detail`) and `dialog` (`title`, `text`, `buttons`), the whole list each time |
| `device.state.changed` | `event.StateChanged`: `to` |
| `device.presence.changed` | `connected` |
| `device.arrived`, `device.departed` | the type alone |
| `device.record.changed` | `name` |
| Issue reasons | `awaiting_operator` (error) means a person is needed. `departed` and `disconnected` are warnings for a printer that is simply off, so they post nothing |

### The manifest (`module.yaml`)

| what | details |
|---|---|
| Required keys | `name`, `kind`, `version`, `license`, `contract_major`, and `openapi` for a module with routes |
| Options | a `required` option left empty fails the install with no retry (`RESOURCE_PLUGIN_SETTINGS`), so every option here has a default |
| Parsing | `manifest.Parse` with `gopkg.in/yaml.v3`, as the server does. `manifest_test.go` parses it the same way |
| Release | the manifest at the root, `<name>_<goos>_<goarch>` per platform, and `checksums.txt` in sha256sum's format listing every other file |

### The HTTP API, as `slackbot` (`internal/serverhttp`)

| what | details |
|---|---|
| `POST /api/v1/auth/login` | `{username, password}`. 200 answers `expires_at` (a session lasts 7 days) and sets the cookie `pcl_session`, whose value is the session token. A refusal is a problem document; too many attempts from one address answer 429 |
| Bearer session | the token is sent as `Authorization: Bearer`, which the server reads before its cookie, and it still counts as a sign-in. It is replaced an hour before `expires_at`, and once after a 401 (a password change ends every session) |
| `POST /api/v1/auth/logout` | when the module stops |
| `GET /api/v1/cameras` | `items[]` with `name`, `device_id` (the printer, empty for none) and `snapshot` (whether it serves stills). Read again every 5 minutes |
| `GET /api/v1/cameras/{camera}/snapshot` | `200 image/jpeg` with `Last-Modified`. `204` is no frame yet, `404` an unknown camera, `502` a camera the server could not read. Over 10 minutes old is treated as live view on |

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
| `chat.postMessage` (`chat:write`) | status messages, and replies with `thread_ts`. About one a second per channel; a `429` is waited out once, as its `Retry-After` says |
| `chat.update` | the start message, rewritten with the outcome when the print ends |
| `files.getUploadURLExternal`, `files.completeUploadExternal` (`files:write`) | camera stills, through slack-go's `UploadFileContext`, into a thread or the channel |

## The Pi camera API (`internal/camera`)

The contract is the sister project's `docs/api.md`, v1.

| what | details |
|---|---|
| `GET /v1/cameras/{id}/still` | `wait=10&max_age=30` for `snap`, `max_age=60` for a status message's still. `200` with `Content-Type: image/jpeg`; `503` is live view on (the bot says so); anything else is a failure |
| Addresses | from the `cameras` setting. Private, loopback and link-local addresses only, checked when the connection is made, never Tailscale's `100.64.0.0/10` or `fd7a:115c:a1e0::/48`. No redirects, at most 8 MiB, 15 s |
| Egress | the Pi hosts cannot be named in `module.yaml`: its `egress` grammar takes a host or a `string`/`url` option, and `cameras` is an object. Egress is not enforced at run time |

## denhac.org

Not used yet (phase 2).
