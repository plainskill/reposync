GO ?= go
BIN ?= reposync

.PHONY: build test tidy

build:
	CGO_ENABLED=0 $(GO) build -o $(BIN) ./cmd/reposync

test:
	CGO_ENABLED=0 $(GO) test ./...

tidy:
	$(GO) mod tidy
