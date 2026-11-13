/**
 * jkp pi extension — connects this pi session to a jkp LAN room.
 *
 * Usage inside pi:
 *   /jkp jkp1.<token>   Join a room using an invite token from the host
 *   /jkp say <message>  Send a message to the host or last speaker
 *   /jkp say --to <nick> <message>  Target one guest from the host
 *   /jkp reset          Clear local room state
 *   /jkp help           Show available commands
 *   /jkp status         Show current connection status
 *   /jkp leave          Disconnect
 *
 * The extension spawns `jkp join` as a detached process that silently mints a
 * unique guest identity and bridges this pi session to the host over TLS.
 */

type ExtensionAPI = {
  on(event: string, handler: (...args: unknown[]) => unknown): unknown;
  registerCommand(
    name: string,
    definition: {
      description?: string;
      handler: (args: unknown, ctx: ExtensionCommandContext) => unknown;
    }
  ): unknown;
};

type ExtensionCommandContext = {
  cwd: string;
  waitForIdle(): Promise<void>;
  sessionManager: { getSessionId(): string };
  ui: {
    notify(message: string, level?: "warning" | "info" | "error"): void;
    setEditorText(text: string): void;
  };
};

import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, watch } from "node:fs";
import path from "node:path";

const EXT = "jkp-pi";
let agentProc: ChildProcess | null = null;
let connectedPeer: string | null = null;
let logWatcher: ReturnType<typeof watch> | null = null;

export default function jkpExtension(pi: ExtensionAPI) {
  pi.registerCommand("jkp", {
    description: "Host/join a jkp LAN room, message the peer, or manage the room",
    handler: (args: unknown, ctx: ExtensionCommandContext) => {
      const input = String(args ?? "").trim();
      const [sub, ...rest] = input.split(/\s+/);
      const target = rest.join(" ");

      switch (sub) {
        case "status":
          return doStatus(ctx);
        case "leave":
          return doLeave(ctx);
        case "say":
          if (!target) {
            return doCli(ctx, ["say"]);
          }
          if (target.startsWith("--to ")) {
            const fields = target.slice("--to ".length).trim().split(/\s+/);
            const nick = fields.shift();
            if (fields[0] === "--") fields.shift();
            const message = fields.join(" ");
            if (!nick || !message) {
              ctx.ui.notify("Usage: /jkp say --to <nick> <message>", "info");
              return;
            }
            return doCli(ctx, ["say", "--to", nick, "--", message]);
          }
          return doCli(ctx, ["say", "--", target]);
        case "reset":
          if (doCli(ctx, ["reset"])) {
            stopAgent();
            ctx.ui.notify("[jkp] local room state cleared; Ergo left running", "info");
          }
          return;
        case "help":
          return doCli(ctx, ["help"]);
        default:
          // If the first arg looks like an invite token, treat it as join.
          if (sub && sub.startsWith("jkp1.")) {
            return doJoin(ctx, input);
          }
          if (!sub) {
            ctx.ui.notify("Paste the invite: /jkp jkp1....", "info");
          } else {
            ctx.ui.notify(
              "Usage: /jkp jkp1.<token> | /jkp say <message> | /jkp reset | /jkp help | /jkp status | /jkp leave",
              "info"
            );
          }
          return;
      }
    },
  });

  pi.on("session_start", () => {
    // nothing to do on start; join is explicit
  });

  pi.on("shutdown", () => {
    stopAgent();
  });
}

function doJoin(ctx: ExtensionCommandContext, token: string) {
  if (agentProc) {
    ctx.ui.notify("Already connected. Use /jkp leave first.", "warning");
    return;
  }

  const sessionId = ctx.sessionManager.getSessionId();
  let jkpBin = findJKPBinary();
  if (!jkpBin) {
    // Try to install the binary via the installer script.
    ctx.ui.notify("[jkp] installing jkp binary...", "info");
    try {
      const { spawnSync } = require("node:child_process");
      const r = spawnSync("sh", ["-c", 'curl -fsSL "${JKP_RELEASE_BASE:-https://github.com/joeyism/jkp/releases/download/v0.1.0}/install.sh" | sh'], {
        stdio: "inherit",
        env: process.env,
      });
      if (r.status !== 0) {
        ctx.ui.notify("jkp install failed. See output above.", "error");
        return;
      }
    } catch {
      ctx.ui.notify("jkp is not installed. Run the jkp install script.", "error");
      return;
    }
    // Re-check after install.
    jkpBin = findJKPBinary();
    if (!jkpBin) {
      ctx.ui.notify("jkp install ran but jkp is still not found. Add ~/.local/bin to PATH.", "error");
      return;
    }
  }

  const args = ["join", "--invite", token, "--harness", "pi", "--pi-session", sessionId];

  agentProc = spawn(jkpBin, args, {
    stdio: ["ignore", "pipe", "pipe"],
    detached: true,
    env: process.env,
  });

  agentProc.stdout?.on("data", (d: Buffer) => {
    for (const line of d.toString().split(/\r?\n/).map((part) => part.trim()).filter(Boolean)) {
      ctx.ui.notify(`[jkp] ${line}`, "info");
      if (line.includes("jkp: joined")) {
        ctx.ui.notify("[jkp] Join is silent. To message the host: /jkp say <message>", "info");
      }
      if (line.startsWith("jkp: log at ")) startLogTail(ctx, line.slice("jkp: log at ".length));
    }
  });
  agentProc.stderr?.on("data", (d: Buffer) => {
    for (const line of d.toString().split(/\r?\n/).map((part) => part.trim()).filter(Boolean)) {
      ctx.ui.notify(`[jkp] ${line}`, "warning");
    }
  });
  agentProc.on("exit", (code) => {
    ctx.ui.notify(`[jkp] agent exited (code ${code})`, "info");
    agentProc = null;
    connectedPeer = null;
    if (logWatcher) {
      logWatcher.close();
      logWatcher = null;
    }
  });

  ctx.ui.notify("[jkp] joining room...", "info");
}

function doCli(ctx: ExtensionCommandContext, args: string[]): boolean {
  const jkpBin = findJKPBinary();
  if (!jkpBin) {
    ctx.ui.notify("jkp is not installed. Run the jkp install script.", "error");
    return false;
  }
  try {
    const { spawnSync } = require("node:child_process");
    const result = spawnSync(jkpBin, args, { encoding: "utf8", env: process.env });
    const stdout = String(result.stdout ?? "").trim();
    const stderr = String(result.stderr ?? "").trim();
    if (stdout) ctx.ui.notify(stdout, result.status === 0 ? "info" : "error");
    if (stderr) ctx.ui.notify(stderr, "error");
    return result.status === 0;
  } catch (err) {
    ctx.ui.notify(`[jkp] command failed: ${String(err)}`, "error");
    return false;
  }
}

function startLogTail(ctx: ExtensionCommandContext, logPath: string) {
  if (!existsSync(logPath)) return;

  let lastSize = 0;
  try {
    const { statSync, readFileSync } = require("node:fs");
    const fi = statSync(logPath);
    lastSize = fi.size;
    logWatcher = watch(logPath, { persistent: false }, () => {
      try {
        const { statSync, readFileSync } = require("node:fs");
        const fi2 = statSync(logPath);
        if (fi2.size > lastSize) {
          const buf = readFileSync(logPath);
          const newContent = buf.slice(lastSize).toString();
          lastSize = fi2.size;
          for (const line of newContent.split("\n")) {
            const trimmed = line.trim();
            if (!trimmed) continue;
            if (
              trimmed.includes("jkp: <-") ||
              trimmed.includes("jkp: ->") ||
              trimmed.includes("jkp: cannot reach") ||
              trimmed.includes("jkp: joined") ||
              trimmed.includes("jkp: connected")
            ) {
              ctx.ui.notify(`[jkp] ${trimmed}`, "info");
            }
          }
        }
      } catch {}
    });
  } catch {}
}

function doStatus(ctx: ExtensionCommandContext) {
  if (agentProc) {
    ctx.ui.notify(
      `[jkp] connected (peer: ${connectedPeer ?? "?"}), pid ${agentProc.pid}`,
      "info"
    );
  } else {
    ctx.ui.notify("[jkp] not connected", "info");
  }
}

function doLeave(ctx: ExtensionCommandContext) {
  stopAgent();
  ctx.ui.notify("[jkp] disconnected", "info");
}

function stopAgent() {
  if (agentProc) {
    agentProc.kill("SIGTERM");
    agentProc = null;
    connectedPeer = null;
  }
  if (logWatcher) {
    logWatcher.close();
    logWatcher = null;
  }
}

function findJKPBinary(): string | null {
  const candidates = [
    process.env.JKP_BIN,
    path.join(process.env.HOME ?? "", ".local/bin/jkp"),
    "/usr/local/bin/jkp",
    "jkp",
  ].filter(Boolean) as string[];

  for (const c of candidates) {
    if (c === "jkp") return c;
    if (existsSync(c)) return c;
  }
  return "jkp";
}
