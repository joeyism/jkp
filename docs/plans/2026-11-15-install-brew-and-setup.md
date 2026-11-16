# Version-free brew + curl install Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Users install jkp with a version-free `brew install` or `curl | sh` line; `jkp setup` writes agent hook files from bytes embedded in the binary.

**Architecture:** `internal/setup` embeds copies of the four hook files (`//go:embed` cannot leave its package). `jkp setup` writes them under `$HOME`. `jkp host` / `jkp join` call setup and warn on failure. `install.sh` downloads `latest` binary, then runs `jkp setup`. Homebrew formula in `joeyism/homebrew-tap` installs only the binary.

**Tech Stack:** Go 1.26, `embed.FS`, POSIX `install.sh`, Homebrew formula Ruby, GitHub Actions release.yml

**Design:** `docs/plans/2026-11-15-install-brew-and-setup-design.md`

---

### Task 1: `internal/setup` writes embedded hooks

**Files:**
- Create: `internal/setup/hooks/opencode.md` (copy of `integrations/opencode/jkp.md`)
- Create: `internal/setup/hooks/opencode-skill.md` (copy of `integrations/opencode/jkp-skill/SKILL.md`)
- Create: `internal/setup/hooks/claude.md` (copy of `integrations/claude/jkp.md`)
- Create: `internal/setup/hooks/pi.ts` (copy of `extensions/pi-jkp/index.ts`)
- Create: `internal/setup/setup.go`
- Create: `internal/setup/setup_test.go`

**Step 1: Copy hook sources into the setup package**

```bash
mkdir -p internal/setup/hooks
cp integrations/opencode/jkp.md internal/setup/hooks/opencode.md
cp integrations/opencode/jkp-skill/SKILL.md internal/setup/hooks/opencode-skill.md
cp integrations/claude/jkp.md internal/setup/hooks/claude.md
cp extensions/pi-jkp/index.ts internal/setup/hooks/pi.ts
```

Go cannot `//go:embed` files outside the package. The test in step 2 locks these copies to the canonical files so they cannot drift.

**Step 2: Write the failing tests**

```go
package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallWritesAllHooks(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := Install(home); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(home, ".config/opencode/command/jkp.md"),
		filepath.Join(home, ".config/opencode/skills/jkp/SKILL.md"),
		filepath.Join(home, ".claude/commands/jkp.md"),
		filepath.Join(home, ".pi/agent/extensions/jkp.ts"),
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if len(b) == 0 {
			t.Fatalf("empty %s", p)
		}
	}
}

func TestInstallOverwrites(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	p := filepath.Join(home, ".config/opencode/command/jkp.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(home); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "stale" {
		t.Fatal("setup must overwrite")
	}
}

func TestInstallFailsWhenParentIsFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".config"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(home); err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddedHooksMatchSource(t *testing.T) {
	pairs := [][2]string{
		{"hooks/opencode.md", "../../integrations/opencode/jkp.md"},
		{"hooks/opencode-skill.md", "../../integrations/opencode/jkp-skill/SKILL.md"},
		{"hooks/claude.md", "../../integrations/claude/jkp.md"},
		{"hooks/pi.ts", "../../extensions/pi-jkp/index.ts"},
	}
	for _, p := range pairs {
		want, err := os.ReadFile(p[1])
		if err != nil {
			t.Fatal(err)
		}
		got, err := fs.ReadFile(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s drifted from %s", p[0], p[1])
		}
	}
}
```

`fs` is the `embed.FS` in `setup.go`. Tests will not compile until that exists — that is the fail.

**Step 3: Run tests to verify they fail**

Run: `go test ./internal/setup/ -count=1`

Expected: FAIL, package or `Install` undefined.

**Step 4: Minimal implementation**

```go
package setup

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed hooks/opencode.md hooks/opencode-skill.md hooks/claude.md hooks/pi.ts
var fs embed.FS

var files = []struct {
	src, dst string
}{
	{"hooks/opencode.md", ".config/opencode/command/jkp.md"},
	{"hooks/opencode-skill.md", ".config/opencode/skills/jkp/SKILL.md"},
	{"hooks/claude.md", ".claude/commands/jkp.md"},
	{"hooks/pi.ts", ".pi/agent/extensions/jkp.ts"},
}

// Install writes agent hook files under home, overwriting any existing files.
func Install(home string) error {
	for _, f := range files {
		body, err := fs.ReadFile(f.src)
		if err != nil {
			return fmt.Errorf("embed %s: %w", f.src, err)
		}
		dst := filepath.Join(home, filepath.FromSlash(f.dst))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}
```

**Step 5: Run tests to verify they pass**

Run: `go test ./internal/setup/ -count=1`

Expected: PASS

**Step 6: Commit**

```bash
git add internal/setup
git commit -m "Add jkp setup embed that writes agent hook files."
```

---

### Task 2: `jkp setup` CLI command

**Files:**
- Modify: `internal/cli/cli.go` (`CLI` struct, `Run`, `help`, new `setup` method)
- Modify: `internal/cli/cli_test.go` (`newCLI`, help test, new setup tests)

**Step 1: Write the failing tests**

In `newCLI`, set a temp home so later host/join tests cannot write to the real `$HOME`:

```go
func newCLI(t *testing.T) (*cli.CLI, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	svc := core.NewService(discardSender{}, syncClock{})
	var out, errOut bytes.Buffer
	c := cli.New(svc, &out, &errOut)
	home := t.TempDir()
	c.HomeDir = func() (string, error) { return home, nil }
	t.Setenv("HOME", home)
	return c, &out, &errOut
}
```

Add:

```go
func TestSetupWritesHooks(t *testing.T) {
	c, out, _ := newCLI(t)
	if err := c.Run([]string{"setup"}); err != nil {
		t.Fatal(err)
	}
	home, err := c.HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".config/opencode/command/jkp.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "jkp host") {
		t.Fatalf("OpenCode command missing jkp host: %s", b)
	}
	if !strings.Contains(out.String(), p) {
		t.Fatalf("setup should print the path, got %q", out.String())
	}
}

func TestSetupUnknownCommandGone(t *testing.T) {
	c, _, _ := newCLI(t)
	if err := c.Run([]string{"setup"}); err != nil {
		t.Fatal(err)
	}
}

func TestHelpMentionsSetup(t *testing.T) {
	c, out, _ := newCLI(t)
	if err := c.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "jkp setup") {
		t.Fatalf("help missing jkp setup: %q", out.String())
	}
}
```

`TestSetupUnknownCommandGone` is redundant with `TestSetupWritesHooks` — skip it. Keep `TestSetupWritesHooks` and `TestHelpMentionsSetup`.

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -count=1 -run 'TestSetupWritesHooks|TestHelpMentionsSetup'`

Expected: FAIL (`HomeDir` undefined and/or unknown command `setup`)

**Step 3: Implement**

On `CLI`:

```go
HomeDir func() (string, error)
```

In `New`:

```go
return &CLI{svc: svc, out: out, errOut: errOut, Env: os.Getenv, RoomDir: room.Dir, HomeDir: os.UserHomeDir}
```

In `Run` switch:

```go
case "setup":
	err = c.setup()
```

```go
func (c *CLI) setup() error {
	home, err := c.HomeDir()
	if err != nil {
		return err
	}
	if err := setup.Install(home); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "jkp: wrote agent hooks under", home)
	fmt.Fprintln(c.out, "  ~/.config/opencode/command/jkp.md")
	fmt.Fprintln(c.out, "  ~/.config/opencode/skills/jkp/SKILL.md")
	fmt.Fprintln(c.out, "  ~/.claude/commands/jkp.md")
	fmt.Fprintln(c.out, "  ~/.pi/agent/extensions/jkp.ts")
	return nil
}
```

Import `github.com/joey/jkp/internal/setup`.

Help line after version:

```
  jkp setup                              write OpenCode/Claude/pi slash commands
```

Print the real paths (joined with home) if you want the test to see the OpenCode path — then `TestSetupWritesHooks` checking `out` for `p` is correct. Prefer printing absolute paths:

```go
fmt.Fprintln(c.out, filepath.Join(home, ".config/opencode/command/jkp.md"))
```

**Step 4: Run tests**

Run: `go test ./internal/cli/ -count=1`

Expected: PASS (including existing help tests)

**Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "Add jkp setup command."
```

---

### Task 3: `host` and `join` call setup, warn on failure

**Files:**
- Modify: `internal/cli/cli.go` (`host`, `join`)
- Modify: `internal/cli/cli_test.go`

**Step 1: Write the failing tests**

```go
func TestJoinWarnsWhenSetupFails(t *testing.T) {
	c, _, errOut := newCLI(t)
	c.HomeDir = func() (string, error) { return "", fmt.Errorf("no home") }
	err := c.Run([]string{"join", "--invite", "jkp1.invalid", "--harness", "pi", "--pi-session", "s"})
	if err == nil {
		t.Fatal("join should still fail on invalid invite")
	}
	if !strings.Contains(errOut.String(), "setup") {
		t.Fatalf("join should warn about setup, got %q", errOut.String())
	}
}

func TestHostWritesHooksBeforeOtherWork(t *testing.T) {
	c, _, _ := newCLI(t)
	_ = c.Run([]string{"host", "--room-dir", t.TempDir(), "--lan-ip", "127.0.0.1", "--oc-session", "missing"})
	home, _ := c.HomeDir()
	p := filepath.Join(home, ".config/opencode/command/jkp.md")
	if _, err := os.ReadFile(p); err != nil {
		t.Fatalf("host should write hooks even if it fails later: %v", err)
	}
}
```

Need `fmt` import in the test file if not present.

`TestHostWritesHooksBeforeOtherWork` is the important one: host may fail (no Docker / no OpenCode) but hooks must exist.

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -count=1 -run 'TestJoinWarnsWhenSetupFails|TestHostWritesHooksBeforeOtherWork'`

Expected: FAIL (no setup warning / no hook file)

**Step 3: Implement**

```go
func (c *CLI) warnSetup() {
	home, err := c.HomeDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "jkp: setup skipped: %v\n", err)
		return
	}
	if err := setup.Install(home); err != nil {
		fmt.Fprintf(c.errOut, "jkp: setup: %v\n", err)
	}
}
```

First line of `host` after flag parse, and first line of `join` after flag parse (before invite required checks is OK — even usage errors can write hooks; cheaper to call after parse succeeds). Call `c.warnSetup()` immediately after `fs.Parse` succeeds in both.

**Step 4: Run tests**

Run: `go test ./internal/cli/ -count=1`

Expected: PASS

**Step 5: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "Write agent hooks at the start of jkp host and join."
```

---

### Task 4: `install.sh` uses latest + `jkp setup`

**Files:**
- Modify: `scripts/install.sh`
- Modify: `tests/install/install_test.sh`
- Modify: `extensions/pi-jkp/index.ts` (line 124)
- Then copy `extensions/pi-jkp/index.ts` → `internal/setup/hooks/pi.ts` so Task 1 drift test still passes.

**Step 1: Update the install test first (TDD for the shell)**

Change the asset check at the top to only require `install.sh` and `SHA256SUMS` in `release.yml`.

Replace the stub binary with one that implements `setup`:

```sh
cat > "$RELEASE/jkp-linux-amd64" << 'STUB'
#!/bin/sh
if [ "$1" = "version" ]; then echo ci-test; exit 0; fi
if [ "$1" = "setup" ]; then
  mkdir -p "$HOME/.config/opencode/command" \
           "$HOME/.config/opencode/skills/jkp" \
           "$HOME/.claude/commands" \
           "$HOME/.pi/agent/extensions"
  printf '%s\n' 'jkp host' > "$HOME/.config/opencode/command/jkp.md"
  printf '%s\n' 'jkp host' > "$HOME/.config/opencode/skills/jkp/SKILL.md"
  printf '%s\n' 'jkp host' > "$HOME/.claude/commands/jkp.md"
  printf '%s\n' 'export default function jkp() {}' > "$HOME/.pi/agent/extensions/jkp.ts"
  exit 0
fi
echo "stub jkp $*"
exit 0
STUB
```

Remove the block that copies `opencode-jkp.md` / skill / claude / pi into `$RELEASE` (lines 92–123). Keep `cp scripts/install.sh` if present.

Case 1 assertions (hook files exist, contain `jkp host`, no `go build`) stay.

**Step 2: Run install test to verify it fails**

Run: `sh tests/install/install_test.sh`

Expected: FAIL — stub `setup` is never invoked, hook files missing (current `install.sh` still curls sidecars that are no longer staged).

**Step 3: Change `install.sh`**

Header comment: default is GitHub `latest/download`, then `jkp setup`.

```sh
BASE="${JKP_RELEASE_BASE:-https://github.com/joeyism/jkp/releases/latest/download}"
```

Delete the sidecar download loop (lines 85–104). After installing the binary:

```sh
echo "jkp: installed $BIN_DIR/jkp"
if ! HOME="$HOME" "$BIN_DIR/jkp" setup; then
  echo "jkp: warning: jkp setup failed" >&2
fi
```

Keep PATH check after that. Exit 2 still means installed-but-not-on-PATH.

**Step 4: Point pi at latest**

In `extensions/pi-jkp/index.ts` line 124, replace the pinned URL with:

```
curl -fsSL "${JKP_RELEASE_BASE:-https://github.com/joeyism/jkp/releases/latest/download}/install.sh" | sh
```

Copy the edited file to `internal/setup/hooks/pi.ts`.

**Step 5: Re-run tests**

Run:

```
sh tests/install/install_test.sh
go test ./internal/setup/ ./internal/cli/ -count=1
```

Expected: PASS

**Step 6: Commit**

```bash
git add scripts/install.sh tests/install/install_test.sh extensions/pi-jkp/index.ts internal/setup/hooks/pi.ts
git commit -m "Install latest binary then run jkp setup."
```

---

### Task 5: `make install` and release workflow

**Files:**
- Modify: `Makefile`
- Modify: `.github/workflows/release.yml`

**Step 1: Makefile**

Replace the four `cp` lines with setup:

```make
install:
	mkdir -p "$(HOME)/.local/bin"
	go build -o "$(HOME)/.local/bin/jkp" ./cmd/jkp
	chmod 0755 "$(HOME)/.local/bin/jkp"
	"$(HOME)/.local/bin/jkp" setup
```

**Step 2: release.yml**

In `build all platforms`, keep binaries, `cp scripts/install.sh dist/install.sh`, and `sha256sum jkp-*`. Delete:

```
cp integrations/opencode/jkp.md dist/opencode-jkp.md
cp integrations/opencode/jkp-skill/SKILL.md dist/opencode-jkp-skill.md
cp integrations/claude/jkp.md dist/claude-jkp.md
cp extensions/pi-jkp/index.ts dist/pi-jkp.ts
```

**Step 3: Confirm install test still likes release.yml**

Run: `sh tests/install/install_test.sh`

Expected: PASS (the workflow-asset grep now only wants `install.sh` and `SHA256SUMS`)

**Step 4: Commit**

```bash
git add Makefile .github/workflows/release.yml
git commit -m "Drop sidecar hook assets from make install and releases."
```

---

### Task 6: README — brew + curl latest, no versions

**Files:**
- Modify: `README.md`

**Step 1: Replace the Install section** (from `## Install` through the PATH note / `jkp version`, drop the v0.1.0 paragraph)

```markdown
## Install

### Homebrew

```bash
brew tap joeyism/tap
brew install jkp
jkp setup
```

`jkp setup` writes the OpenCode, Claude Code, and pi slash commands. `jkp host` and `jkp join` also run it.

### curl

No Go toolchain. One command:

```bash
curl -fsSL https://github.com/joeyism/jkp/releases/latest/download/install.sh | sh
```

That downloads a prebuilt `jkp` binary, checks its SHA-256, and runs `jkp setup`, which installs:

| What | Where |
|---|---|
| Binary | `~/.local/bin/jkp` (Homebrew: prefix `bin`) |
| pi slash command | `~/.pi/agent/extensions/jkp.ts` |
| OpenCode slash command | `~/.config/opencode/command/jkp.md` |
| OpenCode skill | `~/.config/opencode/skills/jkp/SKILL.md` |
| Claude Code slash command | `~/.claude/commands/jkp.md` |

If `~/.local/bin` is not on your `PATH`, the curl script still installs and exits 2. Add this to your shell profile and open a new terminal:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Check it:

```bash
jkp version
```
```

**Step 2: "What people do"** — both host and guest blocks become brew **or** curl-latest (same two snippets). Then `/jkp` as today.

**Step 3: Fail table**

- Delete the `v0.1.0` release row.
- Add: `` `/jkp` missing in OpenCode `` → `jkp setup`, then start a new OpenCode session.
- Keep PATH exit 2, checksum, 6697, OpenCode server, leftover room, pi model, `jkp` not found.

**Step 4: Commit**

```bash
git add README.md
git commit -m "Document brew and version-free curl install."
```

---

### Task 7: Homebrew formula on `joeyism/homebrew-tap`

**Files:**
- Create: `Formula/jkp.rb` in https://github.com/joeyism/homebrew-tap (separate repo)

**Step 1: Clone tap if needed**

```bash
gh repo clone joeyism/homebrew-tap /tmp/homebrew-tap
```

**Step 2: Add `Formula/jkp.rb`**

Use v0.1.1 shas from the public release. Homebrew `url` on a raw binary needs `bin.install` of the downloaded filename.

```ruby
# typed: false
# frozen_string_literal: true

class Jkp < Formula
  desc "LAN coding-agent rooms over IRC"
  homepage "https://github.com/joeyism/jkp"
  version "0.1.1"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/joeyism/jkp/releases/download/v0.1.1/jkp-darwin-amd64"
      sha256 "2913bd33b8a03cf65181faa58f2262f38c4c12def190927744c859c7c02ed1d4"

      def install
        bin.install "jkp-darwin-amd64" => "jkp"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/joeyism/jkp/releases/download/v0.1.1/jkp-darwin-arm64"
      sha256 "5f21015d31caf4304d2c10121ccb816905e8d5f1cbb42d6f2788bc24c85f8ae0"

      def install
        bin.install "jkp-darwin-arm64" => "jkp"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/joeyism/jkp/releases/download/v0.1.1/jkp-linux-amd64"
      sha256 "6f62891a25da383118d74b106a9d2c6d34924111e73307cde90201d53d1ad024"

      def install
        bin.install "jkp-linux-amd64" => "jkp"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/joeyism/jkp/releases/download/v0.1.1/jkp-linux-arm64"
      sha256 "c1783773ebefdcef317f0a5cf31b4eb23a87a53e41287ad8f830f833daf5f122"

      def install
        bin.install "jkp-linux-arm64" => "jkp"
      end
    end
  end

  def caveats
    <<~EOS
      Agent slash commands are not installed by Homebrew.
      Run:
        jkp setup
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/jkp version")
  end
end
```

**Note:** v0.1.1 binaries were built **before** `jkp setup` exists. The formula is still valid for the binary; `jkp setup` will only work after the next jkp release that contains Task 1–3. Do not ship README brew instructions as the primary path until that release, or accept that brew users on v0.1.1 have no `setup` yet.

If v0.1.1 `jkp version` does not print `0.1.1`, drop the `test do` block or match whatever it prints (`dev` if ldflags were wrong). Check with `gh release download` if unsure. Prefer matching a non-empty version line rather than a strict string if needed:

```ruby
test do
  assert_match(/./, shell_output("#{bin}/jkp version"))
end
```

**Step 3: Commit and push the tap**

```bash
cd /tmp/homebrew-tap
git checkout -b jkp-formula
git add Formula/jkp.rb
git commit -m "Add jkp formula."
git push -u origin jkp-formula
```

Open a PR or push to `master` if that is how this tap is maintained (current default branch is `master`, 5 commits, one formula). Pushing to `master` is fine for a personal tap.

**Step 4: Optional local audit**

```bash
brew tap joeyism/tap
brew audit --strict joeyism/tap/jkp
```

Skip if `brew` is not on this Linux machine.

---

### Task 8: Full verification

**Step 1:**

```
go test ./... -race
sh tests/install/install_test.sh
```

Expected: PASS

**Step 2:** Confirm README has no `v0.1.0` and has both brew and `latest/download`.

```
grep -n 'v0.1.0' README.md scripts/install.sh extensions/pi-jkp/index.ts
```

Expected: no matches (except maybe historical comments — there should be none).

**Step 3:** Commit any leftover fixes. Do not tag a release in this plan; tagging is how `jkp setup` actually reaches brew users.
