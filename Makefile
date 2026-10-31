.PHONY: build test integration check fmt docs package-check upstream-check upstream-prs-check upstream-prs-baseline go-github-check

build:
	go build -trimpath -o bin/terraform-provider-github-enterprise .

test:
	go test ./...

integration:
	test -n "$$TF_ACC_TERRAFORM_PATH" || command -v terraform >/dev/null
	TF_REQUIRE_CLI=1 go test ./internal/provider -run '^TestTerraform' -count=1 -v

docs:
	go run ./internal/cmd/docs

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test -race ./...

fmt:
	gofmt -w .

package-check:
	python3 -m unittest discover -s scripts -p 'test_*.py'
	bash scripts/package-check.sh

# Fails when a type this provider declares is also declared by the official
# github provider, which would make the two unusable side by side.
upstream-check:
	./scripts/upstream-check.sh $(UPSTREAM_VERSION)

# Fails when the open pull requests of the official provider propose a type we
# declare, or when they changed since the recorded baseline.
upstream-prs-check:
	./scripts/upstream-prs-check.sh

# Accepts what the official pull requests propose right now as the new baseline.
upstream-prs-baseline:
	./scripts/upstream-prs-check.sh --update

# Reports a newer go-github, whether the provider still builds against it, and
# which enterprise API methods appeared since the pinned version.
go-github-check:
	./scripts/go-github-check.sh
