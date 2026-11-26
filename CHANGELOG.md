# Changelog

Newest first. Each heading is a version in `module.yaml`. A change to what the
module ships raises that version, so the server's Extensions page offers it as an
update.

## 0.3.2 - unreleased

- Passwords the bot makes are 12 characters, down from 24.
- `snap` in a channel the bot has not been invited to says to invite it.

## 0.3.1 - unreleased

- `snap` logs why it could not get or post a picture, instead of only telling the
  person it failed.

## 0.3.0 - unreleased

- Accounts, phase 2 of `docs/design.md`. denhac members whose member page shows ACTIVE
  and the "3D Printer User" authorization:
  - `signup` makes an operator account, and shows the password once, in a window. A
    taken name offers the next free numbered name, or the person types their own. A
    changed denhac name offers to replace the account
  - `password` sets a new password on their own account, and turns a lapsed account
    back on
  - `whoami` says what the bot knows about them
  - `link` claims an account made before the bot, or recognises an admin
- Admins (linked to an account the server confirms is an admin) get `suspend`,
  `unsuspend` and `lookup`.
- Every account change is journaled and finished after a crash. The bot never creates,
  changes or deletes an account that holds admin, nor its own.
- The settings `public_url` and `verify`. The state file is schema 2; schema 1 is read
  and converted.
- The Slack app's command sends mentions escaped (`should_escape: true`).

## 0.2.1 - 2026-11-25 (on the test server only)

- Camera stills come from the server's own cameras, found by printer, signed in as the
  bot's account (`admin_password`). A Pi address in `cameras` still wins for that
  printer, and lets `snap` wait for a fresh picture.
- The settings `server_url`, `admin_username` and `admin_password`.

## 0.2.0 - 2026-11-25 (on the test server only)

- Status in the status channel (#bots), phase 1 of `docs/design.md`:
  - each print's start, with its ending threaded under it and a camera still
  - a printer waiting for a person, after 30 s, and when it is answered
  - a printer that drops off during a print, after 2 min, and when it is back
  - the bot's own start, after a gap or a new version
- After a restart, the bot reads the fleet again and posts only what it missed,
  never the same thing twice.
- `/prusaconnect status`, `jobs` and `snap`, in #bots.
- The settings `cameras` and `issue_reasons`.
- Log lines no longer carry a timestamp of their own: the server's log has one.

## 0.1.0 - 2026-11-25

- The module skeleton (phase 0 of `docs/design.md`).
  - It runs under the server, reads its settings, and idles until it has the Slack
    tokens.
  - It connects to Slack over Socket Mode.
  - It answers `/prusaconnect help` and `/prusaconnect bot` to anyone in the workspace
    except guests.
  - `GET /status` reports its health.
