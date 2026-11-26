# Setting up the Slack app

The module talks to Slack through one Slack app that the workspace owns. It uses Socket
Mode: the module opens a WebSocket to Slack, so Slack never has to reach the server,
and the server needs no public address.

## Create it

1. Open <https://api.slack.com/apps>, choose **Create New App**, then **From a manifest**.
2. Pick the workspace, and paste [`slack/app-manifest.yaml`](../slack/app-manifest.yaml).
   Review it and create the app.
3. **App-level token.** Under **Basic Information**, go to **App-Level Tokens** and choose
   **Generate Token and Scopes**.
   - Give it a name such as `socket`, and the one scope `connections:write`.
   - The token starts with `xapp-`. This is the module's `slack_app_token`.
4. **Install.** Under **Install App**, install it to the workspace. A workspace that
   asks admins to approve apps waits for that approval here.
   - The **Bot User OAuth Token** starts with `xoxb-`. This is the module's
     `slack_bot_token`.
5. **Invite the bot to #bots**: in #bots, type `/invite @` and pick the app's bot user.
6. **The channel ID of #bots.** Open the channel's details. The ID is at the bottom
   and starts with `C`. This is the module's `status_channel`.

The tokens go into the module's settings on the server's Extensions page, which keeps
them as secrets. Do not paste them into chat, issues or commits.

## What the app may do, and why

| scope | used for |
|---|---|
| `commands` | the `/prusaconnect` command |
| `chat:write` | status messages in #bots, and messages in a DM the bot opened |
| `files:write` | camera stills, uploaded through `files.getUploadURLExternal` and `files.completeUploadExternal` |
| `users:read` | `users.info`: refusing guests and deactivated users, and display names |
| `users.profile:read` | `users.profile.get` and `team.profile.get`: the profile fields **Member** and **Member Code URL** |
| `im:write` | `conversations.open`: the DM that carries the address for `link` |

Socket Mode needs the app-level token's `connections:write`. Pop-up windows (modals)
and command answers through `response_url` need no scope of their own.

The manifest also keeps `incoming-webhook`. The module does not use it; it is there
for the webhook integration this Slack app already served.

The app reads no channel history. It gets no events except the command and button
clicks, and it cannot post into a channel it has not been invited to.

## One app, one running module

Slack shares a Socket Mode app's traffic among all its open connections. If a
development copy of the module and the one on the server run on the same app at the
same time, each gets only some of the commands.

For development, make a second app from the same manifest, for example "Printer Bot
(dev)", in a test workspace or with the bot kept out of #bots.
