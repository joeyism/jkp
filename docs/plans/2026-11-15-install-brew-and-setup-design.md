# Install: brew, latest curl, and `jkp setup`

Validated 2026-11-15. Goal: version-free install instructions, without teaching two different products.

## User-facing install

Two on-ramps, same result. Neither mentions `v0.1.x`.

```bash
brew tap joeyism/tap
brew install jkp
jkp setup
```

```bash
curl -fsSL https://github.com/joeyism/jkp/releases/latest/download/install.sh | sh
```

README Install and "What people do" use these blocks. Drop the "you need a published v0.1.0" note. Fail table: remove "v0.1.0 is not published"; add "`/jkp` not found in OpenCode → run `jkp setup`". Keep the curl PATH exit-2 row. Brew users get PATH from Homebrew.

`jkp setup` is required in the brew instructions because `/jkp` in OpenCode cannot exist until the command file is on disk.

## `jkp setup`

Single writer of agent hooks. Always overwrite, no backup.

| Source in the repo | Destination |
|---|---|
| `integrations/opencode/jkp.md` | `~/.config/opencode/command/jkp.md` |
| `integrations/opencode/jkp-skill/SKILL.md` | `~/.config/opencode/skills/jkp/SKILL.md` |
| `integrations/claude/jkp.md` | `~/.claude/commands/jkp.md` |
| `extensions/pi-jkp/index.ts` | `~/.pi/agent/extensions/jkp.ts` |

New package `internal/setup` `go:embed`s those four files. The brew binary never fetches GitHub assets to install hooks. Embedded bytes always match that binary's version.

`jkp setup` is a CLI subcommand: mkdir, write, print each path. Home is `os.UserHomeDir`, overridable in tests (same pattern as `Env` / `RoomDir`). No flags for v1. Setup does not install the binary.

`jkp host` and `jkp join` call setup first. Explicit `jkp setup` **fails** on write error. host/join **warn on stderr and continue** so a terminal `jkp host` still works if `~/.config` is unwritable. All four files every time.

## `install.sh` and make

Default `JKP_RELEASE_BASE` becomes `https://github.com/joeyism/jkp/releases/latest/download`. After the binary is in `$PREFIX/bin/jkp`, run `jkp setup`. Stop downloading the four sidecar markdown/ts files. Binary checksum stays. PATH check stays (exit 2).

The pi extension's "jkp missing" curl uses that same latest `install.sh`.

`make install`: build into `~/.local/bin/jkp`, then `jkp setup`. Drop the four `cp`s.

## Homebrew formula

`Formula/jkp.rb` in `joeyism/homebrew-tap`, same shape as `technews-tui.rb`: `on_macos` / `on_linux`, intel/arm, `url` + `sha256` per arch. Assets are raw binaries (`jkp-darwin-arm64`), not tarballs.

```ruby
bin.install "jkp-darwin-arm64" => "jkp"
```

No hook writes. `caveats` says run `jkp setup`. Homepage `https://github.com/joeyism/jkp`. No license field (repo has no LICENSE). First formula pins v0.1.1. Tap bumps stay manual; GoReleaser auto-update is out of scope.

## Release workflow

Stop copying the four sidecar files into `dist/`. Keep `install.sh`, `jkp-*`, `SHA256SUMS`. People always curl `latest/download/install.sh`, whose default base is `latest/download`.

## Testing

Go tests for `internal/setup` under a fake home: four paths written, bytes equal embed, second run overwrites, parents created, write error returns error. host/join tests assert setup is invoked.

`tests/install/install_test.sh`: fake release dir only serves `jkp-*`, `SHA256SUMS`, `install.sh`. Stub binary implements `setup` (writes four files containing `jkp host`, not `go build`) so case 1 still passes with no Go on PATH. Real-binary half proves embed content. Drop the assertion that `release.yml` uploads sidecar assets. Checksum-mismatch and PATH-exit-2 cases unchanged.

No brew CI in this repo. `brew audit` in the tap is enough for v1.

## Out of scope

- GoReleaser pushing formula updates to the tap
- Homebrew writing dotfiles
- Windows
- `jkp setup` installing or upgrading the binary
