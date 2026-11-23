SHELL := /bin/bash
VERSION := $(shell cut -d' ' -f1 VERSION)
IMAGE := ghcr.io/orchard-suite/redgreen
# The Orrery checkout the expression check runs against.
ORRERY ?= ../orrery
BRANCH ?=

.PHONY: docker check land

# The image, for linux/amd64.
docker:
	docker build --platform linux/amd64 -t $(IMAGE):$(VERSION) .

# Everything that must pass before a commit lands: formatting, vet, the
# tests, and every calculation checked in Orrery's own expression language,
# against the Orrery checkout in ORRERY. It runs here before a commit lands,
# and in CI on every pull request and release.
check:
	@test -z "$$(gofmt -l cmd internal scripts)" || { gofmt -l cmd internal scripts; echo "gofmt: the files above need formatting" >&2; exit 1; }
	go vet ./...
	go test ./...
	go run ./scripts/blueprints > blueprints.check.json
	node scripts/check-expressions.mts blueprints.check.json $(ORRERY)

# Run the check, then land the commits on main.
land: check
	release-damnit land $(BRANCH)
