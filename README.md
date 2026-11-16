# jkp

One host and many guests on the same LAN, each in the coding agent they already use. The host types `/jkp` once and shares the same invite with every guest. Their agents talk over IRC. Nobody compiles anything or types an IRC password.

The host machine runs a small IRC server (Ergo, in Docker). Each person's agent stays on their own machine. Only encrypted IRC crosses the network.

Supported today: macOS and Linux, on `arm64` or `amd64`. Windows is not supported.

<video src="https://github.com/user-attachments/assets/805a3f34-d0c7-4f1e-90b0-6b9b1a8c6bf5" width="100%" controls autoplay muted loop></video>

## Install

No Go toolchain. One command:

```bash
curl -fsSL https://github.com/joeyism/jkp/releases/download/v0.1.0/install.sh | sh
```

That downloads a prebuilt `jkp` binary, checks its SHA-256, and installs:

| What | Where |
|---|---|
| Binary | `~/.local/bin/jkp` |
| pi slash command | `~/.pi/agent/extensions/jkp.ts` |
| OpenCode slash command | `~/.config/opencode/command/jkp.md` |
| OpenCode skill | `~/.config/opencode/skills/jkp/SKILL.md` |
| Claude Code slash command | `~/.claude/commands/jkp.md` |

If `~/.local/bin` is not on your `PATH`, the script still installs and exits 2. Add this to your shell profile and open a new terminal:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Check it:

```bash
jkp version
```

You need a published `v0.1.0` release for that URL to work. Until that tag exists, the curl command will fail. Building from this repo is a developer path, not the install path.

### pi package

If you already have pi and would rather install the extension from git:

```bash
pi install git:github.com/joeyism/jkp
```

The first `/jkp` still downloads the binary if it is missing. `pi install` does not compile Go.

## How a room works

One person hosts. Any number of guests can join with the same invite. This is hub-and-spoke: every guest talks only to the host; guests never see each other's messages.

1. The host types `/jkp` in OpenCode (or runs `jkp host`).
2. jkp starts Ergo if it is not already running, registers the host and shared join accounts, binds the current OpenCode session, and prints one `jkp1.` token.
3. The host sends that same token to each guest.
4. Each guest pastes `/jkp jkp1....` in OpenCode, pi, or Claude Code. jkp securely mints a unique IRC identity for each join.
5. Joining is silent: the host pane is not interrupted. A guest starts the conversation with `/jkp say <message>`; the host replies in its OpenCode session and the message goes back only to that guest.

The only thing the host shares is the invite. It carries the host address, a shared bootstrap account, and the server certificate fingerprint. Anyone with it can join, so do not forward it off the LAN.

The host needs Docker. The guest does not. Both need to be on the same network. If the guest cannot connect, the host allows incoming TCP `6697`. Do not switch to plaintext port `6667`. That port is loopback-only.

Model logins stay yours. jkp does not create API keys. pi uses whatever provider that session already uses. If that login is dead, pi connects and then fails to answer.

## OpenCode

OpenCode can host or join. To host, in the session you want to share:

```text
/jkp
```

That runs `jkp host` and starts Ergo using Docker. Show the full output. The line that matters starts with `INVITE jkp1.`. Send the invite to the other machine.

`jkp host` is safe to run again. It reprints the invite and does not start a second agent.

What you need on this machine:

- `jkp` on `PATH`
- Docker, so Ergo can start
- OpenCode running with a local server, on the session you want to share. If you start OpenCode directly (not through ocmux), launch it with `opencode --port 4096`.

If discovery picks the wrong session, set these and run `/jkp` again:

```bash
export JKP_OC_URL=http://127.0.0.1:4200
export JKP_OC_SESSION=ses_...
```

You watch the conversation in that OpenCode session. Remote messages are injected there, and the model's reply is sent back over IRC.

To join a room from another OpenCode session, paste the invite:

```text
/jkp jkp1.<token>
```

OpenCode joins the invite using the local OpenCode session. If discovery picks the wrong session, set `JKP_OC_URL` and `JKP_OC_SESSION` before joining.

Two or more OpenCode windows on one Mac can join the same room too. Each join gets a separate nick and control file. `/jkp say` targets the current window. On the host, `/jkp say <message>` replies to the last guest who spoke; `/jkp say --to <nick> <message>` selects a guest directly. If nobody has spoken yet, bare host `/jkp say` lists connected guests and `/jkp say <message>` refuses to send. Guests always message the host. Fan-out is not supported yet.

The installed OpenCode jkp skill also tells the model how to send messages. `/jkp help` describes routing and lists commands. `/jkp reset` clears local room state without stopping Ergo.

## pi

pi is guest-only for now. After install, start pi normally. The extension is already in `~/.pi/agent/extensions`, so you do not pass `--extension`.

When you have the invite:

```text
/jkp jkp1.<token>
```

Do not add flags. Do not split the token.

You should see:

- `[jkp] joined as g…, talking to host` (each join gets a unique nick)
- `[jkp] Join is silent; use jkp say to talk to the host`
- `[jkp] jkp: <- host: ...` when the host replies

Other commands:

```text
/jkp say <message>
/jkp say --to <nick> <message>  # host only; pi is guest-only
/jkp status
/jkp leave
/jkp reset
/jkp help
```

`/jkp` with no token only tells you to paste one.

pi's open transcript may not show every turn. The `[jkp]` notices are the watch surface. The host's OpenCode session has the full exchange.

If `/jkp` says jkp is not installed, the extension tries the same install script. If that fails, run the curl command yourself and make sure `~/.local/bin` is on `PATH` in the shell that launched pi.

## Claude Code

The installer writes `~/.claude/commands/jkp.md`, so `/jkp` exists in Claude Code too.

Claude Code is guest-only; OpenCode hosts the room. `/jkp jkp1.<token>` silently mints a guest nick and starts a tools-disabled **background Claude** forked from the Claude Code session you joined from. It knows what you were working on without writing to your live window's transcript.

Incoming host messages do not appear in your Claude Code transcript. Run `/jkp log` to inspect recent `<-` / `->` traffic, and `/jkp say <message>` to send a message to the host. `/jkp reset` and `/jkp help` also work; bare `/jkp` shows help.

**Privacy:** Because the background Claude forks your Claude Code conversation, anything in that conversation can reach the host when it answers questions. Don't join from a window holding sensitive context you wouldn't share with the host.

## What people do

**Host (OpenCode), once:**

```bash
curl -fsSL https://github.com/joeyism/jkp/releases/download/v0.1.0/install.sh | sh
```

Then, in the OpenCode session:

```text
/jkp
```

**Each guest (OpenCode, pi, or Claude Code), once:**

```bash
curl -fsSL https://github.com/joeyism/jkp/releases/download/v0.1.0/install.sh | sh
```

Then:

```text
/jkp jkp1.<the same token from the host>
```

## If it fails

| What you see | What to do |
|---|---|
| `curl` cannot download `install.sh` | The `v0.1.0` release is not published yet. |
| `jkp: installed, but ... is not on PATH` and exit 2 | Add `~/.local/bin` to `PATH` and open a new shell. |
| `checksum mismatch` | Do not bypass it. Download again. |
| `cannot reach <ip>:6697` | On the host Mac, allow incoming TCP `6697`, then paste the same invite. |
| `no OpenCode server is running for <cwd>` | Restart OpenCode with `opencode --port 4096`, or use ocmux, then retry `/jkp`. |
| `room is leftover from an older jkp` | `jkp host` clears older room files automatically, or run `/jkp reset` then `/jkp` if an old invite was used. |
| pi connects, then a model error | pi's own provider login failed. Fix that in pi. jkp cannot do it. |
| `/jkp` in OpenCode says `jkp` is not found | The install did not finish, or this terminal cannot see `~/.local/bin`. |

## Develop

The install path above is what users run. This section is for working on jkp itself.

To build and install the current checkout locally, including its OpenCode command:

```bash
make install
```

This updates `~/.local/bin/jkp`, the installed OpenCode skill and slash command, and the Claude and pi command files. Start a new OpenCode session to reload `/jkp` and the skill.

```bash
go test ./... -race
make test-e2e
sh tests/install/install_test.sh
```

The install test builds a binary, serves it over HTTP, and runs `scripts/install.sh` with `go` removed from `PATH`. CI runs the same script.

`make test-e2e` requires the Ergo test server running at `JKP_ERGO_ADDR` (default `127.0.0.1:6667`). It launches two isolated jkp agents and fake OpenCode servers; it does not touch the local room or call a live model.

Architecture and the capability matrix live in `docs/architecture/`.
