.PHONY: build fmt test race vet check

build:
	mkdir -p bin
	go build -o bin/musical-packets ./cmd/musical-packets

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

check: test race vet

