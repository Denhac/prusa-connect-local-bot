# Changelog

Newest first. Each heading is a version in `module.yaml`. A change to what the
module ships raises that version, so the server's Extensions page offers it as an
update.

## 0.1.0 - unreleased

- The module skeleton (phase 0 of `docs/design.md`).
  - It runs under the server, reads its settings, and idles until it has the Slack
    tokens.
  - It connects to Slack over Socket Mode.
  - It answers `/prusaconnect help` and `/prusaconnect bot` to anyone in the workspace
    except guests.
  - `GET /status` reports its health.
