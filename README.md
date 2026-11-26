# prusa-connect-local-bot

A Slack bot for denhac's self-hosted [prusa-connect](https://github.com/prusa3d/prusa-connect)
server. It runs **inside** the server as a service module: the server starts it,
restarts it when it fails, and keeps its settings and secrets.

What it does, when finished:
- **Status in #bots:** prints that start and end (with a camera still), printers
  waiting for a person, and printers that drop off mid-print.
- **Accounts:** a denhac member with the "3D Printer User" authorization creates
  their account on the printer server, and resets its password, with `/prusaconnect`.
  Former members are turned to viewer, then removed after 90 days. Admins can suspend
  an account.

[`docs/design.md`](docs/design.md) is the design. [`docs/compat.md`](docs/compat.md)
lists what the module relies on from the server, Slack and the Pi cameras.

**Status: phase 1.** The module posts print starts and ends (with a camera still),
printers waiting for a person, and printers that drop off mid-print to #bots. It answers
`/prusaconnect help`, `bot`, and, in #bots, `status`, `jobs` and `snap`.

## Building

You need Go 1.26.11, the version `go.mod` and the Prusa Connect SDK name.

```shell
make check     # gofmt, go vet and every test
make race      # the tests under the race detector (needs a C compiler)
make release   # check, then bin/release/<version>/ and bin/release/slack-bot-<version>.tar.gz
```

If that Go is not first on your PATH, add `GO=/path/to/go` to each command.

A release is what the server installs: `module.yaml` at its root, one binary per
platform (`slack-bot_linux_arm64`, `slack-bot_linux_amd64`), `openapi.json`, the
licence, and `checksums.txt`.

## Deploying

**Once per server**, so that releases can be installed from its Extensions page:
1. Add this to the server's config, with the module path as the key:

   ```yaml
   core:
     modules_local:
       github.com/denhac/prusa-connect-local-bot: /var/lib/prusa-connect/modules-local/slack-bot
   ```

2. Restart the server when nothing is printing.

**Each release:**

```shell
make release
deploy/deploy.sh push rpi@<server>     # copies the newest release into that directory
```

Then, on the server's **Extensions** page, choose **Install** (the first time) or
**Update**, and approve what the module asks for. The server keeps the previous
release, and takes an update back by itself if the new one does not start within a
minute. `deploy/deploy.sh status rpi@<server>` shows the module's recent log lines.

`deploy.sh` takes the same `SSHPASS` and `PI_SUDO_PASSWORD` variables as the sister
project's scripts, and checks host keys strictly (`SSH_KNOWN_HOSTS` names a
known_hosts file of your own). Run `deploy/deploy.sh` alone for the details.

## Settings

Set them on the module's page under Settings, Extensions. Every one is optional:
the module runs idle and lists what is missing, in `/prusaconnect bot` and at `GET
/status`.

| key | what |
|---|---|
| `slack_bot_token` | the Slack app's bot token, `xoxb-…` (secret) |
| `slack_app_token` | the Slack app's app-level token for Socket Mode, `xapp-…` (secret) |
| `status_channel` | the channel ID of #bots, such as `C0123ABCD` |
| `allow_guests` | whether Slack guest accounts may use the command (default `false`) |
| `server_url` | where the bot signs in to the server's API (default `http://127.0.0.1:8080`). Plain http only to loopback; never a Tailscale address |
| `admin_username` | the bot's own account on the server (default `slackbot`) |
| `admin_password` | that account's password (secret). Camera stills from the server need it |
| `cameras` | optional: a printer's own Pi camera still address, by printer id, used before the server's camera and letting `snap` wait for a fresh picture: `{"<printer id>": "http://<Pi LAN address>:8555/v1/cameras/cam0/still"}`. LAN addresses only |
| `issue_reasons` | the server's issue reasons that mean a printer needs a person (default `["awaiting_operator"]`) |

[`docs/slack-app.md`](docs/slack-app.md) says how to set up the Slack app and where
each token comes from.

## Layout

| path | what |
|---|---|
| `main.go` | starts `internal/app`; embeds `openapi.json` |
| `internal/app` | builds the parts and runs them under the server's wire |
| `internal/domain` | what the bot does: commands, messages, health. Standard library only |
| `internal/slack` | Socket Mode, the Web API and Block Kit. The only package using slack-go |
| `internal/connect` | the server, through its SDK: the fleet, jobs and the event stream |
| `internal/camera` | stills from the Pi camera API (the optional override) |
| `internal/serverhttp` | the server's HTTP API, signed in as the bot's account: camera stills now, accounts in phase 2 |
| `internal/store` | the state file in the module's data directory |
| `internal/routes` | the module's HTTP routes on the server (`GET /status`) |
| `internal/settings` | the settings and their checks |
| `internal/secret` | a credential type that never prints its value |
| `internal/arch` | a test holding the packages to the boundaries in the design |
| `module.yaml` | the manifest the server reads. `manifest_test.go` holds it to the code |
| `slack/app-manifest.yaml` | the Slack app |
| `deploy/deploy.sh` | copies a release to the server's machine |

## Dependencies

| module | why |
|---|---|
| `github.com/prusa3d/prusa-connect/sdk` | the server's extension SDK: the module wire and its clients |
| `github.com/slack-go/slack` | the Slack Web API, Socket Mode and Block Kit types |
| `gopkg.in/yaml.v3` | tests only: parses `module.yaml` with the same parser the server uses |

Versions are exact in `go.mod`, and a new dependency gets a line here.

## Licence

MIT, see [LICENSE](LICENSE).

Built on Prusa Connect by Prusa Research. The Prusa Connect SDK, which release
binaries contain, is licensed under OCL v1.1 + Micro v1 + NoSaaS v1 + SWAtt v1; see
the [prusa-connect repository](https://github.com/prusa3d/prusa-connect).
