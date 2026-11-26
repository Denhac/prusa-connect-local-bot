# Testing

For whoever changes the module. The automated tests run on any machine with Go. The
checklist at the end needs the test server, the Slack app, and a member who may sign
up.

## The automated tests

```shell
make check   # gofmt, go vet and every test
make race    # the same tests under the race detector (needs a C compiler)
```

`make release` runs `make check` first. If Go 1.26.11 is not first on `PATH`, add
`GO=$HOME/sdk/go1.26.11/bin/go`.

| where | what it covers |
|---|---|
| `internal/domain` | the commands, the help text, the notification reducer, signup, password, link, suspend, admin, the journal and recovery, and the sweep. Fakes for every port, and a clock the test moves |
| `internal/slack` | Block Kit rendering against golden files, the runner, the poster, profile reads |
| `internal/serverhttp` | the sign-in and the account, camera and grant routes, against a fake server in the shape of 0.1.0-beta.2. It checks that no admin account and not the bot's own is changed |
| `internal/verify` | the denhac page parser and the profile reading, against made-up pages in `testdata/denhac/` |
| `internal/connect`, `internal/app` | the wire calls, and a whole module lifecycle over a pipe |
| `internal/settings`, `manifest_test.go` | each setting, and that `module.yaml`, `settings.Sent` and `openapi.json` agree |
| `internal/arch` | the import rules: only `internal/slack` imports slack-go, only `internal/app` and `internal/connect` import the SDK, and the domain imports only the standard library |

### Golden files

`internal/slack/testdata/golden/` holds the Block Kit a message renders to. After a
deliberate change to a message, regenerate them, then read the diff before
committing:

```shell
go test ./internal/slack/ -update
git diff internal/slack/testdata/
```

### Rules for test data

- **Never real member data.** No real names, member codes, Slack IDs, account IDs or
  addresses. Use made-up ones such as `ABCD`, `Example User` and `printserver.example`.
- The fixtures in `internal/verify/testdata/denhac/` are made up in the shape of the
  real page. When denhac changes the page, write a new made-up fixture from the new
  shape. Never save a real member's page.
- A test that fails must fail with what it got and what it wanted. Before trusting a
  new test that passed the first time, break the code it covers once and watch it
  fail.

## On the test server

The test server is a Raspberry Pi running prusa-connect 0.1.0-beta.2, with the module
installed from `core.modules_local`.
- Deploy as the README says.
- Read the log with `deploy/deploy.sh status rpi@<server>`, or the `journalctl` lines
  in [`operations.md`](operations.md).
- Scripts that run on the Pi must not print a token, a password, or a raw HTTP body.
- The sweep can be watched without waiting a day: its first look comes 2 minutes
  after the module starts. Every settings change restarts the module.

`make demo`, with simulated printers, is in the design but not built. The checklist
uses the test server's real printers.

## The checklist

Run it before calling a phase done, and after a change to the area it covers. Each
line says what to do, then what must happen.

### Install and health

- [ ] Push a release and choose **Update** on the Extensions page. It becomes `ready`
      without asking again for what it already has, and the settings are kept.
- [ ] `/prusaconnect bot`. The version is the new one; Slack is connected; status
      messages and accounts are on; the sweep line is there.
- [ ] Restart the server. The module comes back by itself, and `bot` answers.
- [ ] Downgrade to the release before, then update again. Both start, and the state
      file survives.

### Status messages (phase 1)

- [ ] Start a real print. #bots gets the start with a camera still, and its end with a
      still in the same thread.
- [ ] Restart the server mid-print. Nothing is posted twice, and the end still lands in
      the thread.
- [ ] A printer that waits for a person (`awaiting_operator`) posts once. A printer
      that drops off mid-print posts once.
- [ ] `status` in #bots opens a window listing every printer with its state. Ticking
      two and pressing Show puts their status in the window. Ticking "Post the answer
      in #bots" too posts it in #bots instead.
- [ ] `snap` in #bots opens a window of the printers with a camera. Ticking two and
      pressing Post posts a picture of each in #bots, and the window says how each went.
      `snapshot`, `cam` and `camera` do the same, and `help` lists them only on snap's
      line.
- [ ] `jobs` in #bots opens a window of every printer. Ticking some and pressing Show
      lists their last prints in the window. Outside #bots, `jobs`, `snap` and `status` say to use
      #bots.
- [ ] `snap` in a channel the bot is not in says to invite it.

### Accounts (phase 2)

- [ ] `help` as a member lists no admin commands. As a linked admin, it lists them
      under "Admins".
- [ ] `signup` as a member who qualifies. A window shows a username and a three-word
      password once, asks them to change it, and links to the account page. Signing in
      works.
- [ ] `signup` as someone whose Slack profile does not say Member, or whose page lacks
      the authorization. It is refused, with the reason in words.
- [ ] `signup` when the obvious username is taken. It offers the next free numbered
      name, or lets them type one.
- [ ] `signup` again. It says they already have the account.
- [ ] `password`. A new password is shown once, and the old one stops working.
- [ ] `whoami`. It shows the membership, the account and its state.
- [ ] `link`, opening the address signed in as an admin, then typing the code and
      confirming. The person is linked as an admin.
- [ ] `lookup` by mention, username and member code. Each shows the same member, and
      each lookup is an audit line.
- [ ] `suspend <who> testing`, then confirm. The account signs in as a viewer only.
      `signup`, `password` and `link` are refused for them, even though denhac says
      ACTIVE.
- [ ] `unsuspend <who>`, then confirm. The account is an operator again.
- [ ] `admin <who>`, then confirm. The account is an admin; `lookup` shows who made it
      one; `password` refuses to set its password.
- [ ] `unadmin <who>`, then confirm. The account is an operator again. `unadmin` on an
      admin made by hand is refused.
- [ ] No password appears in the server's log or in any Slack message history; it
      appears only in the window.
- [ ] Crash recovery: restart the server just after confirming a suspend. After the
      start the account is a viewer, and the journal is empty.

### Files and backups

- [ ] After an account change, `audit.log` in the data directory has its line, and the
      line is still there after the Pi reboots.
- [ ] With nothing happening for 30 minutes, `state.json` is written at most 3 or 4
      times (its modification time).
- [ ] `/prusaconnect backup` as an admin answers "Backup made" with an id. `backup list`
      shows it. As a member, it is refused.
- [ ] `deploy/deploy.sh backup rpi@<server> <dir>` saves an archive readable only by you
      that holds `files/module-data/slack-bot/state.json` and `sqlite/host.db`.

### The sweep (phase 3)

- [ ] After a start, `bot` shows a first full pass within a few minutes. The log has a
      `sweep` line with `full=true` and the counts.
- [ ] Set `sweep_max_changes` to `0` with a member who no longer qualifies. Over two
      passes at least 20 h apart, the account stays an operator and the pass shows it
      held.
- [ ] Set it back to `5`. At the next recheck the account becomes a viewer, and `lookup`
      shows it lapsed and the date it will be deleted.
- [ ] With `delete_after` set to `1h` on a test account that has lapsed, the next pass
      after an hour deletes the account. A suspension record of that member stays.
- [ ] In the web app, set a member's account to viewer. At the next full pass it shows
      as suspended "by an admin on the server".
- [ ] In the web app, set a lapsed account back to operator. The next pass sets it to
      viewer again.
- [ ] Make an account by hand in the web app. The next full pass logs it as drift and
      leaves it alone.

**A real lapse takes at least 20 hours.** The time between the two failed checks is
fixed in the code (`recheckAfter`) and is not a setting. The unit tests in
`internal/domain/sweep_test.go` cover the timing. On the server, the lapse lines above
are the slow part of the checklist.
