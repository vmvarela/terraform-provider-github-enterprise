.PHONY: build test integration check fmt docs

build:
	go build -trimpath -o bin/terraform-provider-github-enterprise .

test:
	go test ./...

integration:
	go test ./internal/provider -run '^TestTerraform' -count=1 -v

docs:
	go run ./internal/cmd/docs

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test -race ./...

fmt:
	gofmt -w .
