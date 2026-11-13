---
description: Host or join a jkp LAN room
---
Use `$ARGUMENTS` exactly as provided. Do not rewrite an invite or add flags.
Do not set JKP_OC_SESSION or other environment variables. This pane is already identified.

- If `$ARGUMENTS` is empty, exactly `host`, or starts with `host `, run `jkp host` plus any flags after `host`.
- If `$ARGUMENTS` starts with `jkp1.`, run `jkp join --invite "$ARGUMENTS" --harness opencode`.
- If `$ARGUMENTS` starts with `say --to `, parse the nick and message, then run `jkp say --to <nick> -- <message>`.
- If `$ARGUMENTS` starts with `say `, run `jkp say --` followed by the exact text after `say `.
- If `$ARGUMENTS` is exactly `say`, run `jkp say` to show the host guest roster (guests may see a usage error).
- If `$ARGUMENTS` is exactly `log` or starts with `log `, run `jkp log` plus any flags after `log`.
- If `$ARGUMENTS` is exactly `reset`, run `jkp reset`.
- If `$ARGUMENTS` is exactly `help`, run `jkp help`.
- Otherwise, run `jkp help`.

Run only the one matching command, then show the full output. Exception: if jkp host or jkp join fails because of leftover or older jkp state (for example "leftover from an older jkp" or "older ... control file"), run jkp reset and retry that command once, then show the output. Do not auto-run jkp reset if a session is already bound to a live room — stop and ask the user first. If jkp is not found, stop and tell the user to run the jkp install script. Do not compile anything.
