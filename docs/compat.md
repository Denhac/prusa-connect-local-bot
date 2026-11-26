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
| `GET /api/v1/auth/accounts` | `items[]` with `id`, `username`, `display_name`, `role` (`viewer`, `operator`, `admin`, or empty) and `created_at`. Read again before every change, which refuses an account holding admin or the bot's own |
| `POST /api/v1/auth/accounts` | `{username, password, display_name, role: "operator"}`. 201 is the account; 409 is a taken name. Needs a sign-in |
| `POST /api/v1/auth/accounts/{id}/password` | `{new_password}`, 8 to 72 bytes. 204. A 5xx is tried again: the server may have set the password and failed to end the old sessions. It ends every session, token and pairing of the account |
| `PUT /api/v1/auth/accounts/{id}/role` | `{role}`: `operator` or `viewer`, and `admin` only from `MakeAdmin` on an account that is an operator now (0.5.0). 204. It takes a sign-in and `configure_resource` on `auth`, and refuses taking admin from the last admin |
| `DELETE /api/v1/auth/accounts/{id}` | 204; 404 is taken as already gone. Only the sweep deletes, after `delete_after` lapsed |
| `GET /api/v1/grants/subject/user:{id}/direct` | `{subject, items[]}`, each item `{object, relation, subject}`: the grants written on single printers for one account, on the printers the caller administers. An org role is not among them. The sweep only logs them |
| `/settings/account` (web app) | the page where a person changes their own password, with the current one. The password window links to it under `public_url` |
| `POST /api/v1/server/backups`, `GET /api/v1/server/backups` | the server's own backups, for `/prusaconnect backup`. 201 is the manifest: `id`, `created_at`, `files[]` with `size`. 404 while `core.upgrade.enabled` is off. In beta.2 an id looks like `20261126T163543Z-0.1.0-beta.2-to-unknown`, not the docs' `backup-…` |
| `prusa-connect backup create -config <file> -json` (CLI) | the same backup, run as the server's user in its working directory by `deploy.sh backup`. Backups live in `<overlay dir>/upgrade/backups/<id>/`: `manifest.json`, `sqlite/*.db`, and `files/<source>/…`, such as `files/module-data/slack-bot/state.json`. The last 5 are kept (`core.upgrade.backup_retention`) |
| `access.check` (wire) | `configure_resource` on `resource_kind`/`auth` for `user:<id>`, with `checks: [configure_resource]` and `check_absent_people: true`: whether a linked admin still administers the server |
| `routes.serve` `GET /link` | the principal the host passes (`routesapi.PrincipalOf`); only `user:` principals get a code |

## Slack (`internal/slack`)

Library: `github.com/slack-go/slack v0.36.0` (`socketmode` included).

| what | used for |
|---|---|
| `auth.test` | before each connection: checks the bot token and gives the workspace's `team_id`. `invalid_auth`, `not_authed`, `account_inactive`, `token_revoked` and `not_allowed_token_type` stop the retries until the settings change |
| `apps.connections.open` | through `socketmode.Client.RunContext`, with the app-level token. Its `RunContext` returns when a connection attempt fails, so `Runner.Run` retries with a wait from 5 s to 5 min |
| Socket Mode envelopes | `slash_commands`, acknowledged at once with `Client.Ack` (Slack allows 3 s). `interactive` and `events_api` are acknowledged and ignored for now |
| Slash command payload | `team_id` (another workspace is refused), `user_id`, `text`, `response_url` |
| `users.info` (`users:read`) | `deleted`, `is_restricted`, `is_ultra_restricted`: refusing deactivated users and guests. Cached 10 minutes |
| `team.profile.get`, `users.profile.get` (`users.profile:read`) | the workspace's field labels (cached an hour), and a user's custom field values by those labels |
| Interactivity | `block_actions` (the confirm buttons, acknowledged at once) and `view_submission` (acknowledged with `response_action: update` to a "Working" window, then `views.update`) |
| `views.open`, `views.update` | windows: `views.open` within 3 s of the command's `trigger_id`. Titles at most 24 characters |
| `conversations.open` (`im:write`) | the DM carrying the link address |
| Slash command escaping | `should_escape: true`: a mention arrives as `<@U…|name>` |
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

## denhac.org (`internal/verify`)

| what | details |
|---|---|
| Slack profile | the custom field labelled `Member` is `Yes`, and `Member Code URL` links to `https://denhac.org/member/<code>`. The code (2 to 16 letters or digits) is taken from the link, and the bot builds the address itself |
| `GET https://denhac.org/member/<code>` | public. 200 is the page, 404 an unknown code. At most one request every 2 s, 15 s, 1 MiB, cached 10 min (as denhac's own `max-age=600`), and redirects only within denhac.org |
| The page | `div.page-content`, then `<h2>User Information</h2>` with a list whose items read `Display Name: …` and `Membership Status: …`, and `<h2>Active Authorizations</h2>` with one item each. A page with no status is a changed layout: "cannot tell". Test pages are in `internal/verify/testdata/denhac/` |
| Qualifying | status `ACTIVE` and the authorization in `verify.authorization`, compared without case |
| Member codes | unique to one person. A second Slack user with the same code is refused and logged |
