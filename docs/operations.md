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
  journalctl -u prusa-connect --since today | grep component=audit   # account changes only
  deploy/deploy.sh status rpi@<server>                                # from a checkout
  ```

  No line ever holds a token or a password.

## What the bot changes, and what it never touches

- It creates, resets, turns off and deletes the accounts of members it looks after:
  those made with `signup` or claimed with `link`.
- It makes an admin only with `/prusaconnect admin`, and takes admin only from accounts
  it made admin.
- It never changes an admin account made by hand. It never changes its own account
  (`slackbot`), and never sets an admin's password.
- Every change is an audit line naming the account, and the admin when an admin asked.
  There is no audit channel in Slack.

## The state file

Everything the bot knows about members lives in one file:

```
/var/lib/prusa-connect/config/config.d/module-data/slack-bot/state.json
```

That is the path on a standard install. The module logs its data directory when it
starts ("the host granted a data directory"). The file holds:
- each member code, its Slack user and its account
- the suspensions
- the admin links
- the sweep's progress
- the threads of the status messages

It is written whole on every change, through a temporary file and a rename. So a plain
copy is always a consistent one.

### Back it up

**Suspensions exist only in this file.** A suspended account is a viewer, the same as
a lapsed one, and the server cannot tell the two apart. Lose the file and the
suspensions go with it.

Copy it somewhere off the Pi, daily or after any suspension:

```shell
sudo cp /var/lib/prusa-connect/config/config.d/module-data/slack-bot/state.json \
  /root/slack-bot-state-$(date +%F).json
```

It holds member codes, Slack user IDs and usernames. Keep the copies where only admins
can read them.

### Restore it

1. Wait until nothing is printing, then stop the server: `sudo systemctl stop prusa-connect`.
2. Copy the backup over `state.json`. Keep its owner (`prusa-connect`) and its mode.
3. Start the server: `sudo systemctl start prusa-connect`.

Stop the server first: the running module would overwrite the file on its next change.

After a restore, anything done since the backup is missing from the file. Look up and
redo it:
- **Suspensions made since:** run `suspend` again for anyone who should still be
  suspended.
- **Signups since:** those members' accounts are now drift. They claim them with
  `/prusaconnect link`.
- **Admins made with `admin` since:** these show as admins made by hand, which the bot
  no longer manages. Take admin away in the web app, or leave it.

### If it is lost with no backup

The module starts with an empty file. The server still has every account and role.
- Members claim their accounts again with `/prusaconnect link`, which proves who they
  are by signing in.
- Every suspension is gone. A suspended person who still qualifies could claim their
  account and become an operator again. Re-run each suspension you still want, before
  telling members to link.
- Admin links are gone too. Each admin runs `/prusaconnect link` once.

## Everyday admin tasks

You need your admin account linked first: run `/prusaconnect link` once, and open the
address it sends you while signed in as your admin account. `/prusaconnect help` shows
the admin commands only once you are linked.

| task | command |
|---|---|
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
