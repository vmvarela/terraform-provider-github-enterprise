.PHONY: build test integration check fmt docs package-check

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
