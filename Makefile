.PHONY: test test-short test-integration test-e2e vet build install test-install

test:
	go test ./... -race

test-short:
	go test ./... -short -race

test-integration:
	go test -tags=integration ./tests/integration/... -v -count=1

test-e2e:
	go test -tags=integration -run 'TestE2E|TestErgo' ./tests/integration -v -count=1

test-install:
	sh tests/install/install_test.sh

vet:
	go vet ./...

build:
	go build -o /tmp/jkp ./cmd/jkp

install:
	mkdir -p "$(HOME)/.local/bin" "$(HOME)/.config/opencode/command" "$(HOME)/.config/opencode/skills/jkp" "$(HOME)/.claude/commands" "$(HOME)/.pi/agent/extensions"
	go build -o "$(HOME)/.local/bin/jkp" ./cmd/jkp
	chmod 0755 "$(HOME)/.local/bin/jkp"
	cp integrations/opencode/jkp.md "$(HOME)/.config/opencode/command/jkp.md"
	cp integrations/opencode/jkp-skill/SKILL.md "$(HOME)/.config/opencode/skills/jkp/SKILL.md"
	cp integrations/claude/jkp.md "$(HOME)/.claude/commands/jkp.md"
	cp extensions/pi-jkp/index.ts "$(HOME)/.pi/agent/extensions/jkp.ts"
