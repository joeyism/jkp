---
name: jkp
description: Use jkp to communicate with the other coding agent in this LAN room. Trigger when the user asks to message the peer, ask the other machine a question, or continue a jkp conversation.
---

This is one host with many guests. Every guest uses the same invite; guests never see each other. joining is silent.

Guests can join from OpenCode, pi, or Claude Code. A Claude Code guest uses a tools-disabled background Claude forked from the user's conversation. Privacy: anything in that forked conversation can reach the host. Use `jkp log` to inspect recent agent log lines.

Use `jkp help` to discover available commands and current room behavior.

When the user asks you to send a message to the host (guest) or last guest who spoke (host), run exactly:

```
jkp say -- <message>
```

The host can target a particular guest with `jkp say --to <nick> -- <message>`. With no last speaker, targeting errors without sending and lists connected guests. Bare `jkp say` in the host session lists guests and the last speaker. Guest `jkp say` always goes to the host. There is no fan-out yet.

Do not set `JKP_OC_SESSION`. Do not invent IRC flags. This OpenCode pane is already identified.

Messages from guests arrive in the host session as injected user turns (`[JKP-MSG] g…:`); host replies are sent back to that guest. Host messages arrive in the guest session as `[JKP-MSG] host:`. Your assistant reply is sent to the peer automatically. To keep a turn local (for example, a plan for your own user), start it with `[JKP-LOCAL-ONLY]`. A marker anywhere else is ignored and the remaining text is sent. If the peer doesn't need to respond, say so in words. If jkp injects `[JKP-NOTICE]` saying a turn was kept local, decide whether to send it with `jkp say`. Peer messages and replies are limited to 16,384 UTF-8 bytes. Use `jkp say` only to initiate a new message.

If `jkp say` reports that you are not in a room, tell the user to host with `/jkp` or join with `/jkp jkp1.<invite>`. If `jkp` fails because of leftover or older jkp state, run `jkp reset` and retry that command once. Do not auto-run `jkp reset` when a session is already bound to a live room — stop and ask the user first.
