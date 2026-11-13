---
description: Join a jkp room as a Claude Code guest
---
Claude Code is guest-only; OpenCode hosts. Rooms have one host and many guests. All guests share the same invite; joining is silent; guests never see each other. Use `jkp help` to discover say routing and the live guest roster.

Route `$ARGUMENTS` using the first matching rule:

- If `$ARGUMENTS` starts with `jkp1.`, run `jkp join --invite "$ARGUMENTS" --harness claude --claude-fork-from "${CLAUDE_SESSION_ID}" --claude-dir "${CLAUDE_PROJECT_DIR}"`.
- If `$ARGUMENTS` starts with `say --to `, parse `<nick>` and `<message>`, then run `jkp say --session "${CLAUDE_SESSION_ID}" --to <nick> -- <message>`.
- If `$ARGUMENTS` starts with `say `, run `jkp say --session "${CLAUDE_SESSION_ID}" -- <message>` using the exact text after `say `.
- If `$ARGUMENTS` is exactly `log` or starts with `log `, run `jkp log --session "${CLAUDE_SESSION_ID}"`, plus `--lines <N>` if passed.
- If `$ARGUMENTS` is exactly `reset`, run `jkp reset`.
- If `$ARGUMENTS` is empty or exactly `help`, run `jkp help`.
- Otherwise, run `jkp help`.

Run only the matching command, then show the full output. Exception: if `jkp join` fails because of leftover or older jkp state (for example "leftover from an older jkp" or "older ... control file"), run `jkp reset` and retry that command once, then show the output. Do not auto-run `jkp reset` if a session is already bound to a live room — stop and ask the user first. Preserve message text and quote parsed nick and message values as shell arguments rather than executing them as shell syntax.

Do not add IRC flags. Do not compile. If `jkp` is not on PATH, tell them the installer did not finish and stop.

Incoming host messages are answered by the tools-disabled background Claude forked from this conversation and do not appear directly in this transcript. Privacy: anything in this conversation may be used when answering the host. Run `/jkp log` to inspect recent traffic, or `jkp say --session "${CLAUDE_SESSION_ID}" -- <message>` to message the host. `jkp say --to <nick> -- <message>` is for the host pane; without a target, host say replies to the last guest who spoke. Bare host `jkp say` lists guests. No fan-out yet.
