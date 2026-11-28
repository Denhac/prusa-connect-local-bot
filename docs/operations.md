# Running the Slack bot

For the admins who run denhac's printer server. Building and installing a release is
in the [README](../README.md#deploying), the settings are in its
[settings table](../README.md#settings), and the Slack app is in
[`slack-app.md`](slack-app.md).

## Is it working?

- **In Slack:** `/prusaconnect bot` shows the version, the Slack connection, the
  status messages, whether account features are on and why not, the last membership
  sweep, and any settings still missing.
- **On the server:** the module's page under **Settings, Extensions** shows its state.
  `GET /api/v1/modules/com.github.aux.denhac.prusa-connect-local-bot/status` answers
  the same health as `bot`, as JSON. It needs a sign-in that may view the module.
- **The log:** the module writes to the server's journal.

  ```shell
  journalctl -u prusa-connect --since today | grep slack-bot
  deploy/deploy.sh status rpi@<server>                                # from a checkout
  ```

  No line ever holds a token or a password. **On the Pi, the journal is kept in memory
  only** (Raspberry Pi OS sets journald's `Storage=volatile`). That spares the SD card,
  and every line is gone after a reboot. So the account lines (`component=audit`) are
  also kept on disk; see [the audit log](#the-audit-log).

## What the bot changes, and what it never touches

- It creates, resets, turns off and deletes the accounts of members it looks after:
  those made with `signup` or claimed with `link`.
- It makes an admin only with `/prusaconnect admin`, and takes admin only from accounts
  it made admin.
- It never changes an admin account made by hand. It never changes its own account
  (`slackbot`), and never sets an admin's password.
- Every change is an audit line naming the account, and the admin when an admin asked.
  There is no audit channel in Slack.

## The bot's files

The bot keeps two files in its data directory. On a standard install that is:

```
/var/lib/prusa-connect/config/config.d/module-data/slack-bot/
```

The module logs the path when it starts ("the host granted a data directory").

### The state file, `state.json`

It holds everything the bot knows about members:
- each member code, its Slack user and its account
- the suspensions
- the admin links
- the sweep's progress
- the threads of the status messages

It is written whole, through a temporary file and a rename, so a plain copy is always
a consistent one. **Suspensions exist only in this file.** A suspended account is a
viewer, the same as a lapsed one, and the server cannot tell the two apart.

### The audit log, `audit.log`

One line for each account change, admin action, lookup and sweep pass, the same
lines the journal has as `component=audit`. Unlike the journal, it survives a reboot.

```shell
sudo tail -n 50 /var/lib/prusa-connect/config/config.d/module-data/slack-bot/audit.log
```

At 1 MB it moves to `audit.log.1`, and the one before that is deleted, so the two
together hold a long history in at most 2 MB.

### What they write to the SD card

The SD card wears with every write, so the bot writes little:
- **Status:** a post, a thread or an open issue is written at once, since a lost
  one could repeat a post after a restart. The heartbeat and the time of the last
  event are written at most every 10 minutes, and when the module stops. That is
  about 150 small writes a day, instead of one a minute plus one for every printer
  event.
- **Accounts:** written when an account changes, a few times a day.
- **The audit log:** appended a few lines a day.
- **A save that would write what the file already holds** writes nothing.

After a crash or a power cut, the start message says the bot "stopped some time
after" its last heartbeat, which can be up to 10 minutes early.

The server writes more than the bot, mostly its access-control database
(`authz.db-wal`). That is upstream, outside this module. Keep the journal volatile:
`Storage=persistent` would put every log line of every service on the card.

## Backups

**The server makes its own backups.** Each one is a consistent snapshot of:
- its configuration, with every module's settings **in plain text**: the Slack
  tokens and the `slackbot` password among them (the server keeps them so in
  `config.d/resources.json`)
- its databases, the local accounts and their password hashes among them
- every module's data directory, this bot's `state.json` and `audit.log` included

The server keeps the last 5, on the Pi, in
`/var/lib/prusa-connect/config/config.d/upgrade/backups/`. Each is about 20 MB,
mostly module releases, so make them by hand rather than on a schedule.

| to | do |
|---|---|
| Make one on the Pi now, from Slack | `/prusaconnect backup` (admins). `backup list` shows the ones kept |
| Make one and copy it off the Pi | `deploy/deploy.sh backup rpi@<server> ~/backups`, from a checkout with SSH to the Pi |
| Make one from the web app | Settings, Server, **Updates** |

A backup on the Pi protects against a bad update or a mistake, but not against a dead
SD card. **Copy one off the Pi**, after any suspension, and before any server update.
`deploy.sh backup` saves `prusa-connect-<backup id>.tar.gz`, readable only by you.
**It is as sensitive as the credentials it holds:** whoever reads it can post as the bot
in Slack and sign in to the printer server as an admin. Keep it only where admins alone
can read it. The secrets cannot be cut out of a copy: the server's restore checks every
file against the backup's checksums. If a copy is ever exposed, rotate the Slack tokens
and the `slackbot` password ([Changing credentials](#changing-credentials)).

### Restore the whole server

This puts back every account, setting and module's data as they were at the backup.
It follows the server's own steps (`prusa-connect backup restore`), which have not
been tried on this server yet.

1. Copy the archive to the Pi, and unpack it as the server's user into the backups
   directory:

   ```shell
   sudo -u prusa-connect tar -xzf prusa-connect-<id>.tar.gz \
     -C /var/lib/prusa-connect/config/config.d/upgrade/backups/
   ```

2. Wait until nothing is printing, then stop the server: `sudo systemctl stop prusa-connect`.
3. Restore it:

   ```shell
   sudo -u prusa-connect sh -c 'cd /var/lib/prusa-connect &&
     bin/prusa-connect backup restore <id> -config /etc/prusa-connect/config.yaml'
   ```

4. Start the server: `sudo systemctl start prusa-connect`.

### Restore only the bot's records

1. Take `files/module-data/slack-bot/state.json` out of the archive.
2. Wait until nothing is printing, then stop the server. The running module would
   overwrite the file on its next change.
3. Copy it over `state.json` in the data directory. Keep its owner (`prusa-connect`)
   and its mode (600).
4. Start the server.

### After a restore

Anything done since the backup is missing from the bot's records. Look it up in
`audit.log`, and redo it:
- **Suspensions made since:** run `suspend` again for anyone who should still be
  suspended.
- **Signups since** (only the bot's records restored): those accounts are now
  drift. The members claim them with `/prusaconnect link`.
- **Admins made with `admin` since:** they show as admins made by hand, which the bot
  no longer manages. Take admin away in the web app, or leave it.

### If the bot's records are lost with no backup

The module starts with an empty file. The server still has every account and role.
- Members claim their accounts again with `/prusaconnect link`, which proves who they
  are by signing in.
- Every suspension is gone. A suspended person who still qualifies could claim their
  account and become an operator again. Re-run each suspension you still want, from
  `audit.log` if it survived, before telling members to link.
- Admin links are gone too. Each admin runs `/prusaconnect link` once.

## Everyday admin tasks

You need your admin account linked first: run `/prusaconnect link` once, and open the
address it sends you while signed in as your admin account. `/prusaconnect help` shows
the admin commands only once you are linked.

| task | command |
|---|---|
| Back up the server and the bot's records | `backup`; see [Backups](#backups) |
| Find out whose account something is | `lookup @someone`, `lookup <username>` or `lookup <member code>` |
| Turn someone off at once, for a safety issue | `suspend @someone <reason>`. They stay a viewer while denhac still says ACTIVE, until you `unsuspend` them |
| See who is suspended | `suspend` alone |
| Make a member an admin, and take it back | `admin @someone`, `unadmin @someone` |

**A typed `@mention` reaches the bot as plain text** unless the slash command has
"Escape channels, users, and links" switched on (`should_escape` in the app manifest).
Until then, name people by username or member code.

**A member says their code "belongs to another Slack user".** Member codes are unique,
so this is a fault in denhac's member data or a Slack profile. Fix it there. The bot
refuses the signup and never moves the account.

**Someone deleted or changed a member's account in the web app.** The next full sweep
takes it in:
- **Deleted:** the bot forgets the record.
- **Made admin:** the bot stops managing the account.
- **Set to viewer:** the bot keeps it as a suspension "by an admin on the server".
- **Set back to operator while suspended or lapsed:** the bot sets it to viewer again.

## The membership sweep

Once a day by default, the bot reads every member's denhac page.
- **Turning off:** a member who fails two checks at least 20 hours apart becomes a
  viewer.
- **Deleting:** after 90 days off, and no check that qualifies in that time, the
  account is deleted. Its suspension, if any, is kept.
- **Logging:** it also logs accounts it does not manage, and grants written by hand
  on single printers. It changes neither.

**The safety limits:**
- At most `sweep_max_changes` (default 5) accounts are turned off in any 24 hours,
  and at most as many deleted.
- A full sweep of 4 or more members in which nobody qualifies counts against nobody.
  That looks like a changed page or a renamed authorization, and the log says so.
- When denhac cannot be reached, or its page cannot be read, nothing counts against
  anyone.

**Settings to steer it:**

| to | set |
|---|---|
| Watch a first run without changing anything | `sweep_max_changes` to `0`. `bot` and the log show what would have been held |
| Pause it | `sweep_interval` to `0` |
| Check monthly instead | `sweep_interval` to `720h`. A member with a failed check is still checked again 20 hours later |
| Never delete accounts | `delete_after` to `0` |

Changing a setting restarts the module. That causes no extra sweep: the time of the
last full sweep is kept in the state file.

## Changing credentials

- **The bot's server password** (`slackbot`):
  1. As another admin, reset `slackbot`'s password in the web app.
  2. Paste the new one into `admin_password` on the module's settings page.
  3. Account features and camera stills come back once it signs in again.

  On the test server the password is also kept in
  `/etc/prusa-connect-local-bot/admin_password` (root only). Update that file too.
- **The Slack tokens:** see [`slack-app.md`](slack-app.md). Paste new ones into
  `slack_bot_token` and `slack_app_token`. A stored secret reads back as `xxxxx`, and
  sending `xxxxx` back keeps the stored value.
- **Never paste a token or password into Slack or a chat.** If one leaks, rotate it.

## Updating and going back

- **To update:** `deploy/deploy.sh push`, then **Update** on the Extensions page, as in
  the README. If the new version does not start within a minute, the server goes back
  by itself.
- **To go back by hand:** push the older release from a checkout,
  `deploy/deploy.sh push rpi@<server> bin/release/<version>`, then choose it on the
  Extensions page. On the server, `deploy.sh` also keeps the release it replaced, as
  `/var/lib/prusa-connect/modules-local/slack-bot.previous`. The testing checklist
  includes a downgrade; run it before relying on one.
- **The state file has been schema 2 since 0.3.0.** Going back to a release before
  0.3.0 is not supported.

## When something is wrong

| you see | meaning | do |
|---|---|---|
| `bot`: Slack "token refused" | a Slack token was revoked or pasted wrong | new tokens from the Slack app, then the settings page |
| Status messages fail with `missing_scope` (log) | the app lacks a scope, or its approval is pending | approve or reinstall the app with the scopes in `slack/app-manifest.yaml` |
| "The bot is not in this channel" / `not_in_channel` | the bot user was never invited | `/invite @printer-bot` in #bots |
| "Account features are off: admin_password is not set" | no password for `slackbot` | set `admin_password` |
| Account commands say the bot cannot reach the server, and the log shows 401 or `LOGIN_LIMITED` | `slackbot`'s password changed, or too many sign-ins | set the current password. A login limit clears by itself |
| "Linking is off: the module's public_url is not set" | `link` needs the address people open the server at | set `public_url` |
| `signup` says the bot "cannot check your membership" | denhac.org or Slack did not answer, or the member page's layout changed | try later. If it lasts, see whether the page changed (`internal/verify`) |
| `bot`: the sweep says "no member checked qualifies…" | the breaker held a full sweep | look at a member page by hand. If the authorization was renamed, set `verify.authorization` |
| `snap`: "no camera set up" | no camera on the server for that printer, and no `cameras` override | add the camera on the server |
