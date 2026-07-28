# Every quality gate facet has, defined once. CI calls these targets rather than
# repeating the commands, so a gate cannot exist in CI but not on your machine,
# and the two cannot drift.
#
# Run `make check` before pushing. That is the whole contract.

# Pinned, and run via `go run` rather than installed or declared as a `tool`
# directive. `go install` would put a binary in a shared ~/go/bin, so CI and a
# developer machine quietly disagree about what passing means; a tool directive
# would drag the whole dependency tree of each tool into this module's go.sum,
# which for a module with three direct dependencies is a poor trade.
GOLANGCI_VERSION := v2.12.2
GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

# Pinned for the same reason and deliberately not `@latest`, which is what this
# repository used to run: a scanner that changes underneath you turns a green
# history into an unreproducible one, and the version that ran is part of what
# "no known vulnerabilities" meant on the day it said so.
GOVULNCHECK_VERSION := v1.6.0
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: check build fmt fmt-fix vet lint test tidy vuln clean

## check: everything CI's first tier runs
check: fmt vet lint test tidy vuln

## build: compile the binary
build:
	go build ./...

## fmt: fail on unformatted code (gofmt -s is the standard, and it is free)
fmt:
	@out=$$(gofmt -s -l .); \
	if [ -n "$$out" ]; then \
		echo "not gofmt -s clean:"; echo "$$out" | sed 's/^/  /'; \
		echo "fix: make fmt-fix"; \
		exit 1; \
	fi
	@echo "fmt: ok"

## fmt-fix: rewrite files in place
fmt-fix:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	$(GOLANGCI) run ./...

## test: -race because the suite drives real git repositories and a real tmux
## server; -shuffle because tests that depend on each other's order are a lie.
##
## The privacy guard runs here rather than as its own gate, because it is a Go
## test. It needs a word list -- `.denylist` at the repository root, or the
## FACET_DENYLIST environment variable -- and in CI it FAILS rather than skips
## when neither is set, because a guard that passes having checked nothing is a
## hole rather than a pass. Locally it skips, which is why a green `make check`
## on your machine is not evidence that guard ran.
test:
	go test ./... -race -shuffle=on

## tidy: go.mod/go.sum must already match the imports -- checked without
## rewriting them, so CI reports the drift instead of silently fixing it.
##
## This gate is here because its absence was measured: yaml.v3 was a direct
## import recorded as `// indirect`, and nothing noticed.
tidy:
	go mod tidy -diff

## vuln: known vulnerabilities in what we actually call.
##
## Note what it does not do: govulncheck reports a vulnerability only when a
## reachable symbol is called, so it stays green on a vulnerable dependency you
## merely require. That is a reason the gate is worth having, not a reason to
## trust its silence.
vuln:
	$(GOVULNCHECK) ./...

clean:
	rm -f facet
