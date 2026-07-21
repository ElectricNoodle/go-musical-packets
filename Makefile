.PHONY: build fmt test go-test web-deps web-build web-test web-typecheck race vet check

web/node_modules/.package-lock.json: web/package-lock.json
	npm --prefix web ci

web-deps: web/node_modules/.package-lock.json

web-build: web-deps
	npm --prefix web run build

build: web-build
	mkdir -p bin
	go build -o bin/musical-packets ./cmd/musical-packets

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

go-test:
	go test ./...

web-test: web-deps
	npm --prefix web test

web-typecheck: web-deps
	npm --prefix web run typecheck

test: go-test web-test

race:
	go test -race ./...

vet:
	go vet ./...

check: test web-typecheck race vet
