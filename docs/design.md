# Design: prusa-connect-local-bot, a Slack module for prusa-connect local

Status: draft 5, 2026-11-25. Reviewed twice by an independent agent (draft 1 whole,
draft 2's accounts, rebuild and deploy). Each finding was checked against the source
before it was taken in. The user's decisions of the same day are worked in.

Sister project: prusaConnectCamera (the Pi camera bots). Server source:
`prusa3d/prusa-connect` (paths below prefixed `srv:`), at tag `v0.1.0-beta.2`.
Repository: `git@github.com:Denhac/prusa-connect-local-bot.git`. The Go module path is
`github.com/denhac/prusa-connect-local-bot`, in lower case, because the server requires
each path element to be a DNS label. GitHub treats both spellings as one repository.
Licence: MIT (see "Licence").

## Summary

- A **service module** for the self-hosted prusa-connect server, written in **Go**
  against the server's published extension SDK. The server starts it, supervises it,
  and holds its settings and secrets.
- It talks to Slack over **Socket Mode**, an outbound WebSocket, so the server needs no
  public address.
- It **posts status to #bots**, the members-only channel: prints that start and end
  (with a camera still), printers waiting for a person, and printers that drop off
  mid-print.
- **It owns every non-admin account, on request only.**
  - A denhac member whose member page shows them ACTIVE with the "3D Printer User"
    authorization can create an **operator** account and reset its password with
    `/prusaconnect`. No admin is involved.
  - A daily sweep turns the accounts of former members to **viewer**, and deletes them
    after 90 days.
  - "Turning an account off" only changes its role to `viewer`. The person can still
    sign in and look, but cannot operate a printer.
  - Admins can **look up** who an account, a member code or a Slack user is.
  - Admins can **suspend** an account, and the suspension holds even while the person
    is still a member.
  - Admin accounts themselves are managed by hand, and the bot never touches them.
- Each outside system has exactly one package that knows it: prusa-connect, Slack,
  the Pi camera API and denhac.org.

## Requirements

From the user, 2026-11-22 to 2026-11-25:

1. Runs **inside prusa-connect local as an extension**, with the preferred extension
   library (Go), or in Rust if the extension system is language-agnostic.
2. Posts **status information to #bots**. Only members can see #bots, and non-members
   have #public.
3. **Accounts:**
   - Every account except admin accounts is created and managed by the bot.
   - Every such account is an **operator**.
   - The bot acts **on request only**: no account is created unasked, and nobody is
     sent a message unasked.
   - **Who qualifies:** the Slack profile shows "Member: Yes", and the page behind the
     profile's "Member Code URL" (`https://denhac.org/member/<code>`) shows
     `Membership Status: ACTIVE` and the authorization **"3D Printer User"**.
   - Members cannot change those Slack fields; denhac's own software sets them.
   - **Former members** are found by a slow poll (daily or monthly). Their account
     moves to `viewer`, and is **deleted after 90 days** of that. denhac takes away
     their badge access first, so a local-only account is not urgent.
   - **Admin lookup** (added by the user): an admin can find which member code, Slack
     user and account belong together, starting from any one of them.
   - **Admin suspension:** an admin may turn an account off for a safety issue before a
     ban is assessed. It moves the role to `viewer`, and it keeps holding while the
     member page still says ACTIVE. Suspensions are made on request, never
     automatically.
   - **Turning off changes only the role.** The person can still sign in as a viewer,
     and their password is left alone.
   - **No audit channel.**
   - **Later:** access to particular printers from further authorizations, such as
     "Multi Tool 3D Printer User".
4. Camera pictures come from the Pi bots' pull API (sister project
   `docs/design/multi-target.md`, requirement 4).
5. **Maintainable** when either side changes: the server and SDK are `0.x` beta, and
   Slack and denhac.org change on their own schedules.
6. No product traffic over Tailscale addresses (sister project rule).
7. **Workflow:** the user reviews and makes every commit. Work stops at milestones for
   that.

## Facts the design rests on

Each was read in the source or checked live on 2026-11-25.

### The server and its modules

- **Versions.** The test server runs **0.1.0-beta.2**; the user upgraded it on
  2026-11-25. Beta.2 adds what this design uses:
  - the event type `device.state.changed`
  - `Device.Ready` and `Device.Accepts`
  - the manifest blocks `routes.public`, `routes.staged` and `routes.headers`
- **The SDK is published.** `proxy.golang.org` serves
  `github.com/prusa3d/prusa-connect/sdk@v0.1.0-beta.2`, which declares Go 1.26.11.
  The server's guide still says to build against a checkout. A normal `require` works.
- **The wire is language-neutral**, but Go is the only library (`srv:sdk/wire`,
  `sdk/wirerpc`). A Rust module would reimplement the wire and every host client, and
  follow their beta changes by hand.
- **Supervision.**
  - The host pings every 15 s. It restarts a module after three missed pings, or after
    2 minutes of timed-out calls.
  - Three failures in an hour switch the module off.
  - The start budget is 30 s. The default memory budget is 128 MiB, with `GOMEMLIMIT`
    at 85 %.
  - On stop, the host calls `plugin.shutdown`, closes the connection when it answers,
    and kills the process group 5 s after the call began
    (`srv:internal/extension/wirehost/launch.go`). systemd may also send SIGTERM.
  - A settings change restarts the module.
- **Settings.**
  - `options` in `module.yaml`. A `secret` is masked, and other settings are readable
    by anyone holding `view_resource`.
  - A required option left empty makes the install **fail with no retry**
    (`RESOURCE_PLUGIN_SETTINGS`). So every option here is optional, and the module idles
    until it is configured.
- **Who a module acts as.**
  - It acts as itself, `sink:slack-bot`, from the manifest's `kind` and `name`, within
    its approved `permissions`.
  - It acts for a person only while that person's request to the module's own routes
    is open.
  - It has no sandbox.
- **What a module can call.**
  - `device.*`, `bus.subscribe/emit`, `state.*`, `storage.*`, `access.check/people`,
    `camera.accept`, `routes.call` and `ui.hide`
  - There is **no account call and no camera read**.
- **`access.check`.** With `checks: [configure_resource]` and
  `check_absent_people: true`, the module can ask whether `user:X` holds
  `configure_resource` on `resource_kind`/`auth`. That is the server's own test for an
  account administrator.
- **Events are live, not a log.**
  - Resuming works only within an in-memory window of 32 events or 32 KiB per origin.
  - A server restart, or a reader past its 1 MiB queue, gives a window with
    `Resync: true`. Upstream's `extensions/simple-queue` keeps its cursor in memory and
    rebuilds after a Resync.
- **Module routes.** A GET needs `view_resource` on the instance, which every org viewer
  holds. Routes need an OpenAPI document.
- **`egress`** entries name a host, an IP with ports, or a `string`/`url` option. They
  are shown at install and **not enforced at run time**.
- **The data directory** comes with `needs.data: true` once the install is approved.
  The record store needs a `resources.db` backend, and wizard installs have none.
- **Installing.**
  - `prusa-connect module add` on a running server needs a **server restart**.
  - An install or update from the Extensions page or `POST /api/v1/modules` needs none.
  - An update keeps the previous release, and is **rolled back by the server** if the
    new one is not ready within a minute.
  - Anonymous readiness lists no instances.
- **Required manifest keys:** `kind`, `name`, `version`, `license`, `contract_major: 1`
  and `openapi`.

### Accounts on the server

- **The endpoints** take account **ids**: list, create, delete, set password and set
  role. **There is no suspend.** List rows carry `created_at`.
- **Roles.** `viewer`, `operator` and `admin`; create and set-role can both write
  `admin`. A `viewer` can observe printers and read storages. The server protects the
  last account and the last admin.
- **Create, delete, set role and reset need a sign-in.**
  - A sign-in is a password session, or a Prusa Account token paired to a person.
  - Any API credential is refused with `SESSION_REQUIRED`, including an `administer`
    token of up to 365 days and beta.2's page sessions. The check is on the credential
    kind (`srv:internal/app/credentials.go`, `SignedIn`).
- **A password reset** sets the password, then ends every session, API credential and
  pairing of the account.
  - If that second step fails, the call answers 500 and **the old sessions stay
    alive** (`srv:internal/app/accounts.go`, `ResetPassword`).
  - The authorization graph is read on every request, so a role change takes effect on
    live sessions at once.
- **Login.** `POST /api/v1/auth/login` answers `expires_at` and sets the session in the
  `pcl_session` cookie. A session lasts **7 days**. Too many attempts from one address
  answer 429.
- **A session sent as `Authorization: Bearer` works,** and it still counts as a sign-in.
  This was checked on the Pi on 2026-11-25 with `slackbot`: listing accounts answered
  200, and a role change answered 204, which a sign-in-gated call gives only to a
  sign-in. So the bot needs no cookie jar.
- **What the admin password is.** Whoever holds `configure_resource` on auth can write
  `admin`, and an admin can install modules. **The bot's password is full control of
  the server.**
- ADR-20260910-cqq rule 6: a module "never holds a token".
- Single-printer grants (`POST /api/v1/grants/device/{id}`) need no sign-in. That
  matters for per-printer access later.

### denhac.org member pages (fetched 2026-11-25)

- `GET https://denhac.org/member/<code>` is public: WordPress on WP Engine behind
  Cloudflare.
- A known code answers 200, with `<div class="page-content">` holding:
  - `<h2>User Information</h2>` and a list with "Display Name: …" and
    "Membership Status: **ACTIVE**"
  - `<h2>Active Authorizations</h2>` and a list with "3D Printer User" and
    "Multi Tool 3D Printer User"
- An unknown code answers **404**.
- `cache-control: max-age=600, stale-if-error=86400`. A change can take 10 minutes to
  show. During an outage at denhac, Cloudflare may serve a copy up to a day old.
- What a former member's page shows (404, or another status) is not yet known. Both
  mean "does not qualify".

### The Pi camera API (sister project `docs/api.md`)

- `GET http://<pi>:8555/v1/cameras/{id}/still?max_age=&wait=` returns a JPEG.
- It answers 503 while someone watches live.
- It has no password, and only private-network clients may call it.
- Its `rtsp` field showed a Tailscale address on one Pi. The bot never uses that field.

### Slack (from documentation; checked in phase 0)

- **Socket Mode.**
  - An app token (`xapp-…`, `connections:write`) opens the WebSocket.
  - Commands and interactions arrive on it, and each must be acknowledged within 3 s.
  - The client reconnects when Slack closes the socket.
- **Answering.**
  - `response_url` answers a command ephemerally, with no channel membership needed.
  - A modal needs a `trigger_id` within 3 s. It can be updated with `views.update`, and
    it is never part of a channel's history.
- **Profiles.** Custom profile fields come from `users.profile.get`
  (`users.profile:read`), and `team.profile.get` maps their ids to labels.
- **Sending.** Files go through `files.getUploadURLExternal` and
  `files.completeUploadExternal`. `chat.postMessage` allows about one message a second
  per channel.
- **Setting up.** An app can be created from a manifest file, so its scopes and
  command are written down in the repository.

## Decisions

| # | decision | why | rejected |
|---|---|---|---|
| D1 | A **service module** | The user asked for it. The host supervises it and keeps its secrets. It reads the fleet with no token to renew | A separate systemd service |
| D2 | **Go**, `require github.com/prusa3d/prusa-connect/sdk v0.1.0-beta.2`, the version the Pi runs | The published SDK and its tests. An upgrade is a version bump, and a wire change fails the build | Rust over a hand-written wire. A submodule and a `replace` line |
| D3 | **Slack Socket Mode** | Outbound only, which fits a LAN-only server | HTTP events, which need the server on the internet |
| D4 | **`slack-go/slack`**, pinned, only in `internal/slack` | The mature Go client, kept to one package | A hand-written client |
| D5 | Fleet and events through the **wire, as the module itself** (`observe`) | No token; the operator approves the permission | Server HTTP with a token |
| D6 | **The fleet is the truth and events are signals.** Rebuild from `device.*` on start and on Resync. Deduplicate on natural keys | Replay is small and does not survive a server restart | A saved cursor and event-id ring |
| D7 | Account changes through the **HTTP auth API, signed in as a dedicated bot admin**. The password is a `secret` option, and the session travels as `Bearer` | The only credential the server accepts for these calls on beta.2. The exception to rule 6 stays in one package | Admin API token (refused by kind). Page session (refused) |
| D8 | **denhac decides who qualifies**, behind `domain.Verifier`, in `internal/verify`. Checked at every signup and reset, and by the sweep | Site policy, which changes on its own schedule. One package knows the fields and the page | Checks spread through the command handlers |
| D9 | **Turning an account off = set its role to `viewer`, and nothing else.** It is used for an admin's suspension and for a former member. The person can still sign in and look, but cannot operate. On a member account, `viewer` marks the account as off, on the server itself | The user's choice. No suspend call exists. The graph is read on every request, so live sessions and tokens lose operator rights at once. It is one server call, so it cannot be left half done. The account id, and with it the print history, survives | Also scrambling the password. Deleting at once. Withdrawing every role |
| D10 | **Former members are found by a sweep**, by default daily. Two failed checks at least 20 h apart turn the account off. After 90 days off for that reason, it is deleted. At most `sweep_max_changes` changes of each kind per sweep | The user asked for a slow poll and the 90 days. Two observations ride out a denhac glitch. The per-sweep limit keeps a broken page from turning off everyone at once, with no audit channel needed to stop it | Real-time profile events. A cap that waits for an admin's confirmation |
| D11 | **One account writer.** Every change to an account goes through one goroutine, and is written to a journal before it starts and finished on restart | Commands, the sweep and admins can target the same account. A crash between writing a suspension and making the server call must not lose it | A lock per Slack user |
| D12 | **Admins are recognised by the server.** An admin links once, and every admin action asks `access.check` | One source of truth | A list of Slack ids |
| D13 | **Members only.** Printer status, jobs and stills answer only members and admins. They post into a channel only in #bots | #bots is the members' channel. #public is not | Anyone in the workspace |
| D14 | **No audit channel.** Every account action is a log line (`component=audit`) in the server's journal. The person concerned and the admin who acted see the result in their answers | The user decided against an audit channel | An audit channel |
| D15 | Camera stills from the **Pi camera API v1** | No camera read on the wire, and that API was built for this | Server periodic capture, which writes a file per interval with no retention |
| D16 | State in **one JSON file in the data directory**, written atomically | Always present, small and readable | The record store, or a database |

## Architecture

```
       Slack (wss + https)          denhac.org (https)       Pi camera bots (LAN :8555)
              ▲                            ▲                            ▲
┌─────────────┼───────────── module process (sink:slack-bot) ───────────┼────────────┐
│   internal/slack ── Directory (profile fields) ──► internal/verify    │            │
│   socket · web API · modals                         fields + page     │            │
│              ▲                                         ▲     internal/camera       │
│              │ Message / Command / Event               │ Verifier    ▲             │
│              ▼                                         ▼             │ StillSource │
│   internal/domain  pure: commands, policy, notification reducer, account lifecycle │
│      ▲ Fleet, Events, Access        ▲ Accounts           ▲ Store                   │
│      ▼                              ▼                    ▼                         │
│   internal/connect            internal/accounts     internal/store                 │
│   deviceapi · busapi ·        /api/v1/auth/*,       data-dir JSON + journal        │
│   accessapi · routesapi       bearer session                                       │
└──────▲──────────────────────────────┼──────────────────────────────────────────────┘
  wire (stdin/stdout)          server_url (https, or loopback)
       └────────── prusa-connect server ────┘
```

### What each package may know

| package | knows | must not know |
|---|---|---|
| `internal/domain` | commands, policy, notification rules, the account lifecycle, authorizations → access, the message model | Slack, the SDK, HTTP, denhac, files |
| `internal/slack` | Socket Mode, the Web API, Block Kit, modals, rate limits, file upload, profile lookups | the SDK, the server, denhac |
| `internal/verify` | denhac's Slack fields (by label), the member page and how to read it, what "qualifies" means | Slack's client (through `Directory`), Connect |
| `internal/connect` | the SDK's `deviceapi`, `busapi`, `accessapi`, `routesapi` and payloads | Slack, the auth API |
| `internal/serverhttp` | the server's HTTP API as `slackbot`: `/api/v1/auth/login` and `/logout`, the bearer session, `/api/v1/cameras…`, and in phase 2 `/accounts…`, problem documents | Slack, the SDK |
| `internal/camera` | the Pi camera API v1 | Slack, the server |
| `internal/store` | the state file, the journal, atomic writes, the schema number | everything else |
| `internal/settings` | option keys, types, validation | how they are used |
| `internal/app` | building and running the above | protocol details |

`internal/arch/imports_test.go` enforces the table with `go list -deps`.

### The ports `internal/domain` defines

```go
type Fleet interface {          // internal/connect
    Printers(ctx) ([]Printer, error)            // with Issues, Dialog, Ready
    Jobs(ctx, deviceID) (current, last *Job, err error)
    History(ctx, since time.Time) ([]Job, error)
}
type Events interface {         // internal/connect
    Run(ctx, handle func(ctx, batch []Event, resync bool) error) error
}
type Access interface {         // internal/connect
    IsServerAdmin(ctx, subject string) (bool, error)
}
type Accounts interface {       // internal/accounts. Ids only
    List(ctx) ([]Account, error)                // id, username, role, created_at
    Create(ctx, username, display string, pw Secret) (Account, error) // always operator
    SetPassword(ctx, id string, pw Secret) error // a 5xx is retried
    SetRole(ctx, id string, role Role) error     // operator or viewer only
    Delete(ctx, id string) error
    // No call can write admin. Each call re-reads the account and refuses one that
    // holds admin.
}
type Directory interface {      // internal/slack
    Profile(ctx, slackID string) (SlackProfile, error) // custom fields by label
}
type Verifier interface {       // internal/verify
    // Errors mean "cannot tell", never "no".
    Check(ctx, slackID string) (Membership, error)
    CheckCode(ctx, code string) (Membership, error) // the sweep, by stored code
}
// Membership{Code, DisplayName string; Active bool; Authorizations []string}
type Chat interface {           // internal/slack
    Post / Reply / Update / Respond(responseURL) / OpenModal / UpdateModal
}
type StillSource interface {    // internal/camera
    Still(ctx, deviceID string, fresh bool) (JPEG, error)
}
```

**Access policy.** `domain/access.go` turns a `Membership` into the access it gives,
from the `access_rules` setting. Today it holds one rule:
`{"authorization": "3D Printer User", "grant": "org:operator"}`. Per-printer access
later adds rules such as `{"authorization": "Multi Tool 3D Printer User", "grant": "device:<printer id>:operator"}`.

When that happens, turning an account off must withdraw those printer grants as well.
The change touches the domain and `internal/accounts`, not `internal/verify`.

### What runs

One `errgroup`, cancelled by `plugin.shutdown`, SIGTERM or SIGINT.

| goroutine | does |
|---|---|
| wire | `conn.Serve`. Its handlers return quickly, never wait on the network, and never hold a lock across I/O |
| events | the subscription. It hands each batch to the reducer through a bounded channel. If the channel is full it drops the batch and asks for a resync |
| reducer | owns notification state and turns events and rebuilds into messages |
| slack socket | connects, reconnects, acknowledges at once, and dispatches commands |
| poster | the single outbound queue, at about one message a second per channel |
| commands | one goroutine per command, at most 8 at once, each with a timeout. Account work is handed to the writer |
| account writer | **the only code that changes accounts.** It works through the journal one entry at a time |
| sweep | every `sweep_interval`, it queues a check of each member |

**On shutdown:** flush the state file (it is written on every change anyway), close the
Slack connection within at most 2 s, and return. All of it fits in the host's 5 s.
There is no "stopping" message: a settings edit restarts the module, so it would post
on every edit. The start message after a gap says the same, and only when it matters.

## Slack

**The app** is owned by the user, and created from `slack/app-manifest.yaml` in this
repository:
- Socket Mode on, interactivity on, one slash command (`/prusaconnect`), and no event
  subscriptions.
- Bot scopes: `commands`, `chat:write`, `files:write`, `users:read`,
  `users.profile:read` and `im:write`.
- The bot token and the app token are each their own `secret` option.
- The bot is invited to #bots.

**Every payload is checked first.** One from another workspace is refused (the bot learns its own `team_id` from `auth.test`, so there is no setting for it),
and so is a guest, unless `allow_guests` is set.

**Who is a member, for reading.** One of these must hold, in this order:
1. the Slack user has a member account that is on
2. the Slack user is a linked admin
3. they qualify now (the answer is cached for 10 minutes, as long as denhac's own cache)

**Commands.** One table in `internal/domain/commands.go` holds each command's words,
who may run it and its help text. `help` and a completeness test come from it.

| command | who | answer |
|---|---|---|
| `help` | anyone | ephemeral |
| `whoami` | anyone | ephemeral: the account, its state, and whether you qualify (and why not) |
| `signup` | qualifies | a modal: "creating your account…", then the username and password, once. A person whose account lapsed gets it turned back on instead, with their old password |
| `password` | qualifies, has a member account, not suspended | a modal with the new password. It warns that every session and app pairing ended. It also turns back on an account that was off because membership had lapsed |
| `status [printer]` | members | ephemeral. With `--here` in #bots, posted there |
| `jobs [printer]` | members | ephemeral: the last 10 prints |
| `snap <printer>` | members, in #bots | acknowledged at once, then a fresh still in #bots. Elsewhere: "use this in #bots" |
| `link` / `link <code>` | anyone with a Connect account | proves which Connect account is yours (see "Linking") |
| `suspend <@user\|username> [reason]` | admins | confirm, then suspend. With no argument, it lists the suspensions |
| `unsuspend <@user\|username>` | admins | confirm, then lift |
| `lookup <@user\|username\|member code>` | admins | ephemeral: the member record behind any of the three (see "Lookup") |
| `bot` | anyone | the module's health |

**Answers.**
- Command answers go through `response_url`.
- **Passwords appear only in modals.**
- If the person closed the modal before the password arrived, the bot answers
  ephemerally, without the password: "run `password` to get a new one".
- `internal/slack/render.go` alone turns `domain.Message` into Block Kit, and golden
  files hold the JSON for every kind.

## Status to #bots

| signal | message | rules |
|---|---|---|
| a print starts | "▶ **MK4S-1** started `bracket.bgcode`, about 2 h 10 min" | one top-level message per `job_id` |
| a print ends | "✅ finished / ❌ failed / ⏹ cancelled in 2 h 04 min, 31 g", with a still | a thread reply, and the parent is updated. If there is no parent, a new message |
| the printer needs a person (`awaiting_operator`, or an attention state in `device.state.changed`) | "⚠ **MK4S-1** needs a person: *Filament runout*", with the dialog text | only the reasons in `notify.issue_reasons`, after 30 s; cleared in the thread |
| the printer drops off **mid-print** | "MK4S-1 went offline during a print" / "is back" | only while a job is open, after 2 min |
| the module started | "Bot 0.1.0 started on prusa-connect 0.1.0-beta.2. Not running from 14:02 to 14:31" | only after a gap of more than 2 min or a version change. A settings edit restarts the module silently |

The rules live in `internal/domain/notify.go`, a reducer that does no I/O: events, fleet
snapshots and the clock go in, and posts, replies and updates come out. The setting
`issue_reasons` (default `["awaiting_operator"]`) says which issue reasons mean "needs a
person". A per-signal on/off switch is left out until someone asks for one.

**Until phase 2 checks membership,** `status`, `jobs` and `snap` answer only when typed in
#bots. Only members can see that channel.

### Rebuild (D6)

**When it runs:** on start, and on every `resync`.

**Steps:**
1. Start buffering incoming events.
2. Read the fleet:
   - `device.list` (names, presence, `Issues`, `Dialog`)
   - `device.jobs` for each printer
   - `device.job_history` from **the time of the last event applied**. That is not the
     heartbeat, which keeps moving during a resync without a restart.
3. Compare with the state file and post what changed:
   - an open thread whose job has ended gets its ending
   - a print that ran entirely during the gap gets one line
   - a print running now with no thread gets its start message
   - an open issue with no thread gets its message
   - an issue that has cleared gets its thread closed
4. Apply the buffered events.

**Deduplication uses natural keys:** `job_id` plus "started" or "settled", and printer
plus issue reason.

**State kept:** `last_applied` (the time of the last event applied), `last_alive`
(written every minute, for the start message), the last 200 `job_threads`, and
`open_issues`. The cursor lives in memory.

## Accounts

### The kinds of account

| kind | how it is recognised | the bot may |
|---|---|---|
| **admin** | holds the `admin` role | read it, and recognise its owner through `link`. Never change it. Each change re-reads the role first |
| **member** | created by the bot, or claimed through `link`, and recorded in the state file | everything below |
| **drift** | anything else: a non-admin account the bot neither made nor had claimed | log it once a day, and never touch it |

A member is keyed by its denhac member code. It records the Slack user, the account
id, the username, and how it arrived (`created` or `claimed`).

### A member account's states

| state | role | how it gets there | how it leaves |
|---|---|---|---|
| **on** | `operator` | `signup` or a claim. From lapsed, `signup` or `password` once the person qualifies again | lapsing, or an admin's `suspend` |
| **lapsed** | `viewer` | the sweep: two failed checks at least 20 h apart | `signup` or `password` when the person qualifies again. **Deleted after 90 days** in this state |
| **suspended** | `viewer` | an admin's `suspend` | only an admin's `unsuspend` |

**Off is not locked out.**
- A `viewer` can still sign in and see printers, cameras and stored files, but can
  send no command.
- Their sessions, API tokens and app pairings stay, now with viewer rights only.
- The password is never changed by turning an account off or on.

- A suspension is kept by member code and wins over everything else. While it holds,
  `signup`, `password` and claiming through `link` are all refused, even when denhac
  shows ACTIVE.
- If a suspended person also lapses, the account is deleted after 90 days, but the
  suspension record stays. Signing up again when they rejoin is refused until an admin
  lifts it.
- `unsuspend` returns the account to **on** if the person qualifies, and to **lapsed**
  if not.

### Checking membership (`internal/verify`)

1. Read the Slack profile through `Directory`.
   - Labels are resolved to field ids once, through `team.profile.get`, and the
     result is logged.
   - "Member" (`verify.member_field`) must be `Yes`.
2. Read the code from "Member Code URL" (`verify.code_field`).
   - It must match `https://denhac.org/member/<code>`, where the code is 2 to 16
     letters or digits.
   - **The bot builds the URL itself from the code.** It never fetches the profile's
     link as written.
3. `GET https://denhac.org/member/<code>`, with a 15 s timeout, at most 1 MiB, no
   redirects off denhac.org, and requests paced at least 2 s apart.
4. Parse it with `golang.org/x/net/html`. Find `div.page-content`, then read the list
   under "User Information" and the list under "Active Authorizations".
5. The result:

   | the page | means |
   |---|---|
   | 200, `Membership Status` is `ACTIVE`, and "3D Printer User" is listed | qualifies |
   | 200 with another status, or without the authorization, or a 404 | does not qualify |
   | anything else, including a page whose blocks cannot be found | **cannot tell**. It is an error, never a "no" |

6. **One member code has one account.** The member code, not the Slack user or the
   name, is what an account belongs to. See "One account per member code" for what
   a second request does.

**The username** starts from denhac's Display Name: lowercase, `[a-z0-9._-]`, at most
32 characters. Member codes never appear in a username.
- **Free:** it is used, and the person is not asked.
- **Taken:** the signup modal says "**exampleuser** is taken" and offers a choice:
  - the next free numbered name (`exampleuser-2`, `exampleuser-3`, …), chosen by default, so
    one click accepts it
  - a name the person types
- **Empty after cleaning:** the modal asks the person to choose, suggesting their Slack
  username, cleaned the same way.
- **A typed name** follows the same rules. It is checked when the modal is submitted:
  the shape, at most 32 characters, not taken, not reserved. A refusal shows in the
  modal beside the field, and the person tries again.
- **Reserved names** are refused: `admin_username`, `admin`, `root`, `slackbot`, and any
  name starting with `admin`.
- The account's display name on the server is denhac's Display Name as written.
- The member record keeps `based_on`, the denhac Display Name the username came from.
  A rename (below) is noticed when denhac's name differs from `based_on`, not from
  the username, so a name the person chose is not mistaken for a rename.

Tests are table tests over saved copies of the page in `testdata/denhac/`: active,
inactive, no authorization, 404, and a changed layout.

### Signup

`/prusaconnect signup`:
1. A modal opens at once.
2. **Check membership** and any suspension. If the person does not qualify or is
   suspended, show why. If their member code already has an account, follow "One
   account per member code" below.
3. **Write the journal entry:** `{op: signup, slack, code, username, at}`.
4. Call create with role `operator` and a 12-character password from `crypto/rand` (about 69 bits).
   - A 409 (someone took the name between the modal and the submit) reopens the
     choice in the modal, with the next free numbered name chosen.
5. Store the member record, and close the entry.
6. Show the username and password in the modal, once. Log an audit line without the
   password.

**After a crash**, the entry is finished on start:
- An account with that exact username, created no earlier than the entry, holding
  `operator` and belonging to nobody, is taken as the bot's own. The person is told to
  run `password` when they next use the bot.
- Anything else is logged. **No account is ever adopted because of its name.**

### One account per member code

Each member code has at most one member account. The server has **no rename call**
(beta.2: list, create, delete, password, role). So a new username can only come from
a new account, and the old one is then deleted. Everything here happens **on
request**, in `signup`; nothing renames or replaces an account by itself.

When `signup` finds the member code already has an account:

| the case | what happens |
|---|---|
| Same Slack user, and the username denhac's Display Name gives is the account's username | nothing changes. If the account lapsed, it is turned back on (role `operator`, password unchanged). Otherwise: "You already have **name**. Use `password` to reset it" |
| Same Slack user, and the **Display Name changed** (it differs from `based_on`) | the modal asks: "Your denhac name is now **new**. Replace your account **old** with a new one?" It says the old account and its sign-ins end, and that prints it made stay recorded under the old name. With confirm, the account is **replaced**, and the new username is chosen as for any signup, including the choice when it is taken. Without it, nothing changes |
| **Another Slack user** with the same member code | **refused**, and the account is left alone. Member codes are unique to one person (user, 2026-11-25), so this is a fault in the member data: it is logged, and the person is told to ask an admin |

**Replacing an account, through the writer:**
1. Write the journal entry `{op: replace, code, old account id, new username, slack}`.
2. Create the new account with role `operator` and a new password.
3. Move the member record to the new account id (and to the new Slack user, if it
   changed).
4. Delete the old account. Its sessions, tokens and pairings end with it.
5. Close the entry, then show the new username and password in the modal, once.

**Rules:**
- A suspended member code is never replaced. The suspension holds, and `signup` is
  refused.
- An account holding `admin` is never deleted, even when it shares the code. That is
  reported in the log instead.
- After a crash, an open entry is finished on start:
  - a new account that exists gets the member record
  - the old account is then deleted
  - so the member never ends with no account, and is never left with two
- The rate limit counts a replacement as one signup.

The cost of a replacement: print history and audit lines on the server stay with the
old, deleted account id, and the person signs in with the new name and password.

### Password reset

`/prusaconnect password`:
1. Check membership again, and refuse if suspended.
2. The person must have a member account.
3. Write the journal entry.
   - If the account is lapsed, set the role to `operator` first.
   - Then set the password. A 5xx is retried, because the server may already have
     changed the password while old sessions still live.
4. Show the new password in the modal, and log an audit line.

At most 3 signups and resets per person per 24 h, with the counters kept in the state
file.

### Linking

One flow does two jobs: it lets an admin be recognised, and it lets a member claim an
account that was there before the bot.

1. `/prusaconnect link`. The bot DMs a link to the module route
   `GET {public_url}/api/v1/modules/<namespace>/link?for=<nonce>`. The nonce is
   single-use, lasts 10 minutes, and is bound to that Slack user.
2. Opened in a browser signed in to Connect, the page accepts `user:` principals only.
   It shows a one-time code and says: "This links Slack user **@member** to Connect
   account **member**. Enter it only if you are @member."
3. `/prusaconnect link <code>`. It is accepted only from that Slack user, with at most
   5 wrong tries, and confirmed with a button. Then:
   - **An admin account** is stored as an admin link. It is used only so the bot can
     ask `access.check` before each admin action.
   - **A non-admin account**: the person must qualify, their member code must not be
     suspended, and the account must belong to nobody else. It becomes a member
     (`claimed`), and its role is set to `operator`.

### Lookup (admin only)

`/prusaconnect lookup <who>` finds a member from any one of the three handles and
shows the rest. It is for an admin who knows a person by one name and needs another:
"who is this account?", "which account is this member code?", or "what is this Slack
user called on the printer server?".

- `<who>` is a Slack mention (`@someone`), a Connect username, or a member code. The bot
  tries them in that order.
- **It needs an admin**, checked with `access.check` like `suspend`.
- **It answers ephemerally**, to the admin alone, and is never posted to a channel.
- **It reads, and changes nothing.** It reads the state file, the account list on the
  server, and the Slack user's display name. It does not contact denhac.org; the last
  sweep's result is shown instead.
- **Every lookup is logged** (`component=audit`): who asked, and what they asked for.

The answer shows:
- the member code
- the Slack user: a mention, and their display name now
- the Connect username, and the account's display name and role on the server
- the state (on, lapsed since …, suspended by … for …), and how it arrived (created or
  claimed)
- `based_on`, the denhac name the username came from
- the last sweep check and its result

When the name belongs to no member, it says what the name is instead:
- an admin account
- drift (a non-admin account the bot does not hold)
- a suspension record without an account
- or nothing at all

### Suspension (admin only)

**Why it exists:** an admin may turn an account off for a safety issue before a ban is
assessed. The suspension holds even while denhac still shows the person ACTIVE. It is
never automatic; an admin runs it.

`suspend` is confirmed with a button and checked with `access.check`. Then it goes
through the writer:
1. Write the journal entry **and** the suspension record (member code, which admin,
   the reason, the time) before touching the server.
2. Set the role to `viewer`. Live sessions and tokens lose operator rights at once.
3. Close the entry. On restart, an open entry is carried out again from step 2, which
   does nothing if the role is already `viewer`.

The admin's answer confirms each step. An audit line is logged.

### The sweep

**When:** once every `sweep_interval` (default 24 h; `720h` for monthly). It never
reacts to Slack, and it sends nobody a message.

**For each member, by stored code:**
- **Cannot tell** (an error) → nothing changes, and it is counted in `bot`.
- **Qualifies** → any earlier failed check is cleared. A lapsed account is **not**
  turned back on here, because that waits for the person's own `signup` or
  `password`.
- **Does not qualify** → recorded with its time.
  - On the second such result at least 20 h after the first, the account is turned
    **off** as lapsed (role `viewer`, through the writer).
- **Lapsed for more than 90 days** (`delete_after`) → the account is deleted, and the
  member record goes. A suspension record stays.

**Limits:** at most `sweep_max_changes` (default 5) accounts turned off, and at most
5 deleted, per sweep. The rest wait for the next sweep, oldest first.
- A real wave of lapses drains over a few days.
- A broken page, or a renamed authorization, turns off at most 5 accounts a day. The
  log and `bot` show it.

**The same sweep also:**
- logs **drift**: non-admin accounts the bot does not hold, and hand-written grants on
  single printers, read from `/api/v1/grants/subject/{subject}/direct`
- handles **changes made by hand** on member accounts:
  - promoted to `admin`: the account has left the bot's care. The record is dropped,
    and it is logged
  - demoted to `viewer` while on: recorded as an admin suspension
  - restored to `operator` while lapsed or suspended: set back to `viewer`, and logged

### The bot's admin account, plainly

- The bot uses one admin account (`admin_username`, default `slackbot`).
  - **It exists on the test server.** It was created on 2026-11-25 with the user's admin
    account, over loopback on the server's own machine.
  - Its password was generated on the Pi and is stored only there, in
    `/etc/prusa-connect-local-bot/admin_password` (root, mode 600). At install it is
    copied into the module's `admin_password` secret setting, and it never passes
    through a developer's machine.
  - `internal/accounts` signs in when needed, keeps the session until shortly before
    `expires_at` (7 days), and sends it as `Bearer`.
- **`server_url`.**
  - Default `http://127.0.0.1:8080`.
  - Once certbot TLS is on: `https://<server's LAN name>:8080`, or loopback
    with the certificate's name as the TLS server name.
  - Plain `http` to anything but loopback is refused, and so are Tailscale addresses
    and `*.ts.net`.
- **This password is full control of the server.** So the bot limits itself:
  - The `Accounts` port cannot write `admin`, and refuses any account that holds it.
  - Deletion happens only in the sweep, after 90 days lapsed.
  - Every change is journaled and logged, and the server logs every action of
    `slackbot`.
- **If sign-in fails** (401, or 429 `LOGIN_LIMITED`), account work stops. `bot` and every
  account command say so until sign-in works again.
- **To rotate the password:** reset it in the web app, then paste the new one on the
  Extensions page.
- **Upstream could remove this password** with a wire capability for account
  administration, approved by the operator like `list_people`. If it arrives, only
  `internal/accounts` changes.

## Camera stills

The user chose the server's own cameras as the source (2026-11-25), with a printer's Pi
as an optional override.

**From the server (the default):**
- `internal/serverhttp` signs in as `slackbot` and reads `GET /api/v1/cameras`. Each
  camera's `device_id` says which printer it watches, so nothing is configured per
  printer. The list is read again every 5 minutes, and at once after a 404.
- A picture is `GET /api/v1/cameras/{camera}/snapshot`, the newest frame the camera's Pi
  pushed, normally at most about 15 s old.
- **Its limits:**
  - There is no fresher one to wait for.
  - A picture more than 10 minutes old is not posted. Live view pauses the pushes,
    and the bot says the camera has no recent picture.
  - A 204 means no picture yet.
  - With no `admin_password`, this source is off.

**From a Pi (an override, per printer):**
- The `cameras` setting maps a printer to its Pi's still address, such as
  `{"<printer id>": "http://<Pi LAN address>:8555/v1/cameras/cam0/still"}`. That Pi
  is then asked first, and the server is asked if it fails.
- `snap` asks with `wait=10&max_age=30`, so it gets a fresh picture, and a print's end
  asks with `max_age=60`.
- **The guard is applied when the connection is made,** in `net.Dialer.Control`:
  private addresses only, never `100.64.0.0/10` or `fd7a:115c:a1e0::/48`, no redirects,
  at most 8 MiB, and a 15 s timeout.

## The manifest (sketch)

```yaml
kind: sink
name: slack-bot
version: 0.1.0
license: MIT
summary: Posts printer status to Slack and gives denhac members their accounts.
contract_major: 1
openapi: openapi.json
require:
  core: ">=0.1.0-beta.2 <0.2.0"
  core-device.list: "*"
  core-device.jobs: "*"
  core-device.job_history: "*"
  core-bus.subscribe: "*"
  core-access.check: "*"
permissions: [observe]
checks: [configure_resource]
check_absent_people: true
needs:
  data: true
  memory_mib: 96            # measured in phase 0
egress:
  - {host: slack.com, why: "Slack Web API and opening Socket Mode.", sends: "Status, answers, stills."}
  - {host: "*.slack.com", why: "The Socket Mode WebSocket and file uploads.", sends: "Acknowledgements and stills."}
  - {host: denhac.org, why: "Reads a member's public status page.", sends: "The member code."}
  - {option: server_url, ports: [8080], why: "Signs in as the bot's account to manage member accounts.", sends: "The bot's password, account changes."}
options:   # all optional: the module idles, reporting what is missing, until set
  - {key: slack_bot_token, type: secret, default: "", summary: "Bot token, xoxb-…"}
  - {key: slack_app_token, type: secret, default: "", summary: "App token for Socket Mode, xapp-…"}
  - {key: status_channel, type: string, default: "", summary: "Channel ID of #bots"}
  - {key: public_url, type: url, default: "", summary: "Where people open Connect (no Tailscale address)"}
  - {key: server_url, type: url, default: "http://127.0.0.1:8080", summary: "Where the bot signs in"}
  - {key: admin_username, type: string, default: "slackbot", summary: "The bot's admin account"}
  - {key: admin_password, type: secret, default: "", summary: "Its password. Empty turns account features off"}
  - {key: allow_guests, type: bool, default: "false", summary: "Accept commands from Slack guests"}
  - {key: verify, type: object, default: '{"member_field":"Member","code_field":"Member Code URL","member_url":"https://denhac.org/member/"}', summary: "Where the membership facts are"}
  - {key: access_rules, type: list, default: '[{"authorization":"3D Printer User","grant":"org:operator"}]', summary: "Which authorization gives which access"}
  - {key: sweep_interval, type: duration, default: "24h", summary: "How often every member is checked"}
  - {key: sweep_max_changes, type: int, default: "5", summary: "Most accounts turned off, and most deleted, in one sweep"}
  - {key: delete_after, type: duration, default: "2160h", summary: "How long an account stays lapsed before it is deleted (90 days)"}
  - {key: cameras, type: object, default: "{}", summary: "Printer id to Pi camera still address"}
  - {key: notify, type: object, default: "{}", summary: "Per signal: on/off; issue_reasons"}
```

Routes: `GET /link` (the code page) and `GET /status` (health). Both need
`view_resource`.

**The namespace** comes from the module path `github.com/denhac/prusa-connect-local-bot`,
which gives `com.github.aux.denhac.prusa-connect-local-bot`. It must be fixed before
the first install. Changing it later means a reinstall and a new data directory.

## State

`<data dir>/state.json` is rewritten on every change: to a temporary file, synced,
then renamed. Two owners write it, the status loop and the account service. Each saves
only its own part (`status` and `accounts`), encoded while that owner holds its own
lock, so neither can race the other. Since 0.3.0 the file is schema 2:
`{"schema": 2, "status": {…}, "accounts": {…}}`. Schema 1 (0.2.x, with the status
fields at the top level) is read and converted.

```json
{
  "schema": 1,
  "last_alive": "…", "last_applied": "…",
  "members": {"ABCD": {"slack": "U012ABC", "account_id": "…", "username": "exampleuser",
              "via": "created", "based_on": "Example User", "since": "…", "state": "on",
              "failed_checks": ["…"], "lapsed_at": null}},
  "suspensions": {"ABCD": {"by": "U0ADMIN", "reason": "…", "at": "…"}},
  "admins": {"U0ADMIN": {"subject": "user:…", "linked": "…"}},
  "journal": [{"id": "…", "op": "suspend", "account_id": "…", "step": 1, "at": "…"}],
  "limits": {"U012ABC": ["…"]},
  "job_threads": {"<job id>": {"channel": "C…", "ts": "…", "posted": ["started"]}},
  "open_issues": {"<device>/<reason>": {"ts": "…"}}
}
```

- `schema` is one number. An unknown number stops the start with a clear error, and
  there are no migrations until one is needed.
- Nonces and link codes stay in memory.
- **The members, suspensions and journal are what matter.**
  - `docs/operations.md` says how to back up the file.
  - If it is lost, the server still holds every account's role. Members claim their
    accounts again with `link`, which proves ownership by signing in.
  - **Suspensions would be lost**, so the backup matters.
    - A suspended account is a `viewer`, like a lapsed one, and the server cannot
      tell them apart.
    - So after a loss, a suspended person who still qualifies could claim their
      account back to `operator` through `link`.
    - After restoring from an older copy or starting empty, an admin re-runs any
      suspension they still want.

## Failures and how they show

| failure | behaviour | where it shows |
|---|---|---|
| Not configured | the module runs idle, listing the missing settings | `/status`, `bot`, the log |
| Slack unreachable | reconnect with backoff up to 5 min; at most 500 queued messages | `/status`, the log |
| Resync, or the subscription ends | rebuild, then subscribe again | the log |
| The bot cannot sign in | account work stops | `bot`, every account command, the log |
| `SESSION_REQUIRED` or another problem code | the code and `trace_id` in the answer and the log | the answer, the log |
| denhac is down or its page changed | "cannot tell": signup and reset say "try later", and the sweep changes nothing | `bot`, the log |
| The sweep hits its limit | the rest wait for the next sweep | `bot`, the log |
| Camera 503 or timeout | the message without a picture | the log |

`GET /status` answers:
- the version and the SDK version
- Slack's state and the last event's time
- the queue depth
- whether account features are on, the last sign-in result, and the last sweep's
  counts
- open journal entries and missing settings

`bot` shows the same in Slack.

Logs go to stderr, which lands in the server's journal (`journalctl -u prusa-connect`),
in `slog` text with `component=`. Account actions are `component=audit`. Secrets print
as `[redacted]`, and a test checks this.

## Security

| threat | answer |
|---|---|
| A Slack token leaks | secret options, never logged, least scopes |
| Another workspace, or a guest | `team_id` checked on every payload; guests refused by default |
| A non-member reads printer status or stills | read commands answer members only, and post only in #bots |
| Someone claims membership they lack | denhac's software sets the fields, not the member. The bot reads them from Slack, builds the URL itself, and reads denhac's own page. One code belongs to one Slack user |
| A taken-over Slack account of a member | it can reset only its own member account, at most 3 times a day, and every reset is logged. Never an admin account |
| A broken denhac page or a Slack outage | "cannot tell" never turns anyone off. Turning off needs two failed checks 20 h apart, and at most 5 accounts per sweep |
| A fake admin | admin actions need a link made through a Connect sign-in, and a check with the server every time |
| Member codes and Slack identities seen by the wrong person | only `lookup` shows them, only to a confirmed admin, only ephemerally; every lookup is logged |
| Phishing a link code | a nonce binds it to one Slack user, both sides name whom it links, wrong tries are limited, and it expires in 10 min |
| The bot's password leaks | it is full control of the server: a dedicated account, a secret, loopback or TLS only, rotated in the web app |
| A password is seen | shown once, in a modal, to its owner. Never in history or the logs |
| The `cameras` or `verify` settings point somewhere bad | cameras are checked when the connection is made. denhac addresses are built from a parsed code, with no redirects off-site |

## Maintainability

- **Each outside system is one package.** `docs/compat.md` lists what each relies on:
  - the server's wire methods, events, routes, fields and problem codes
  - Slack's methods, scopes and payloads
  - the Pi camera API's routes and codes
  - denhac's two profile fields and the page markup

  It is updated in the same commit as the code.
- **Upgrading the server** (`docs/upgrading.md`):
  1. Read the release's `[sdk]` and auth changelog lines.
  2. Bump the SDK `require` and `require.core`, then run `make test`.
  3. Run `make compat-live SERVER=…`, which checks the live `/openapi.json` and event
     vocabulary against `compat.md`. `testdata/server/<version>/` holds the offline
     copy.
  4. Release, update from the Extensions page, and run the smoke checklist.
- **When denhac.org changes:** the bot says "cannot tell". Save the new page into
  `testdata/denhac/`, fix the parser and release. Only `internal/verify` changes.
- **Dependencies:** the SDK, `slack-go/slack`, `golang.org/x/net` and
  `golang.org/x/sync`. Versions are exact, `govulncheck` runs in CI, and each
  dependency has its reason written in the README.
- **Conventions:**
  - Errors are turned into domain errors at the adapter.
  - `internal/domain` does no I/O.
  - There is no package-level state.
- **Reference:** upstream's `extensions/simple-queue`, for its cursor handling, its
  `require` list and its `internal/hostfake` test host.

## Testing

| level | what | how |
|---|---|---|
| domain | the command table, policy, access rules, the notification reducer (delays, rebuild, natural keys), the account states (signup, crash recovery, suspension, lapsing, the 90 days, the sweep limits, changes made by hand) | table tests with a fake clock and fake ports |
| rendering | every message and modal | golden Block Kit JSON |
| `verify` | Slack fields by label, code parsing, page parsing, 404, cannot-tell | saved profiles and saved pages |
| `connect` | real envelopes, resync and rebuild, `access.check` | recorded envelopes over a fake host like upstream's `hostfake` |
| `accounts` | sign-in, the bearer session, renewal, each call, retrying a 5xx reset, refusing admin accounts, problem documents | `httptest` with recorded bodies, plus the OpenAPI check |
| `slack` | acknowledging within 3 s, reconnect, 429, modals, upload, team and guest checks | a fake WebSocket and Web API |
| `camera` | 200, 503, timeout, a public or Tailscale address, a redirect | `httptest` |
| boundaries | the package table | `internal/arch` import test |
| whole module | install, events, commands, the sweep | `make demo` (simulated printers), a test Slack app, a test denhac code. Checklist in `docs/testing.md` |

CI runs `go test -race`, a pinned `golangci-lint`, `govulncheck`, the arch test, and
release builds for `linux/arm64` and `linux/amd64`.

## Build, release and deploy

- **Toolchain:** Go 1.26.11, as the SDK declares: the official build from go.dev.
- `make release` writes `bin/release/<version>/` and a `.tar.gz` of it. The directory
  holds `module.yaml`, `openapi.json`, `slack-bot_linux_arm64`, `slack-bot_linux_amd64`
  (`CGO_ENABLED=0 go build -trimpath`) and `checksums.txt`.
- **Once, with a server restart at a quiet time:** add `core.modules_local` naming
  `/var/lib/prusa-connect/modules-local/slack-bot` to the server's config.
- **Every install and update after that, with no restart:**
  1. `deploy/deploy.sh push HOST` copies a release there over SSH. It uses the sister
     project's `SSHPASS` and `PI_SUDO_PASSWORD` variables, and a known-hosts file of
     its own.
  2. An admin clicks Install or Update on the Extensions page, approves the requests,
     and enters the settings.
  3. The server keeps the previous release and rolls back by itself if the new one is
     not ready within a minute.
- `deploy/deploy.sh status HOST` reads the module's journal lines. Downgrading from the
  Extensions page is tested in phase 0.

## Phases and milestones

The user commits at each milestone after reviewing it.

| phase | scope | done when (milestone) |
|---|---|---|
| — | this design | the user has reviewed `docs/design.md` |
| 0 | Done already: the Pi on beta.2, the `slackbot` account, the bearer-session check, and Go 1.26.11. Still to do: `core.modules_local` on the Pi (one restart), and the Slack app, which the user creates from its manifest. Then: Go module, skeleton, settings, idle-until-configured, Socket Mode, `help`, `bot`, `/status`, `make release`, `deploy.sh push` | the module is ready on the Pi and answers `bot`, and a server restart brings it back. Memory is measured. Checked: `access.check` on auth, the Slack facts, an Extensions-page downgrade |
| 1 | Status: the reducer, rebuild, every signal, threads, stills; `status`, `jobs`, `snap` | a real print posts its start and end with a still, and a server restart mid-print posts nothing twice |
| 2 | Accounts: `internal/verify` (denhac), `internal/accounts`, the writer and journal, `signup`, `password`, `whoami`, `link`, `suspend`/`unsuspend` | a real member signs up and resets, and a non-member is refused with the reason. A suspended member stays refused while denhac says ACTIVE. A crash in each operation recovers. No password in history or the logs |
| 3 | The sweep: lapsing with two checks, deleting after 90 days, the limits, drift, changes made by hand | a former member's account turns `viewer` after two sweeps and is deleted after `delete_after` (tested with a short value). A broken page turns off at most 5 accounts |
| Later | Per-printer access from further authorizations (`device:` grants; turning off also withdraws them). Print control for members. Approving waiting printers. A watcher outside the Pi | each is its own design note |

## Settled with the user (2026-11-25)

- **Turning off:** an admin's suspension and a lapsed membership both move the role to
  `viewer`, and nothing else. The person may still sign in as a viewer. A lapsed
  account is deleted after 90 days.
- **Licence:** MIT.
- **Repository:** `Denhac/prusa-connect-local-bot` on GitHub. Module path
  `github.com/denhac/prusa-connect-local-bot`.
- **Who reads status:** members only. #bots is members-only, and non-members have
  #public.
- **No audit channel.** Account actions go to the server's log.
- **Ownership:** the user owns the Slack app, and the repository is in the denhac
  GitHub organisation.
- **The Pi runs beta.2.**
- **The user commits.** Work stops at milestones for review.

## Licence

- This repository is **MIT**, and `module.yaml` says `license: MIT`. Prusa's own
  template for third-party modules (`prusa3d/prusa-connect-extension-example`) is MIT
  too, and the server treats the manifest's licence as free text.
- **Dependencies and their licences:**
  - `slack-go/slack`: BSD-2-Clause
  - `golang.org/x/net` and `golang.org/x/sync`: BSD-3-Clause
  - All are compatible with MIT.
- **The Prusa Connect SDK** has no licence of its own. It is under the repository's
  OCL v1.1 + Micro v1 + NoSaaS v1 + SWAtt v1. The source here only imports it, but
  release binaries contain it. So the README and `bot` say "built on Prusa Connect by
  Prusa Research", and the README names the SDK's licence. That follows the SWAtt
  add-on's attribution rule at no cost. denhac is a non-profit and offers no hosted
  service, so the Micro and NoSaaS add-ons do not bear on it.
- This is a practical reading, not legal advice.

## Upstream asks (issues for prusa3d/prusa-connect)

1. A wire capability for account administration, approved by the operator like
   `list_people`, so a module needs no password.
2. A suspend or disable call for accounts.
3. A camera read on the wire (`camera.snapshot`).
4. `egress` entries that can name a `list` or `object` option.
