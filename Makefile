GO ?= go

.PHONY: build vet test check help

build:
	$(GO) build ./...

vet:
	$(GO) vet ./...

# Every test boots real containers, so this needs a working Docker.
test:
	$(GO) test -count=1 ./...

check: vet test

help:
	@echo "build  - compile the package"
	@echo "vet    - run go vet"
	@echo "test   - run the tests, which need Docker"
	@echo "check  - vet, then test"
	@echo
	@echo "Releases are cut by the Release workflow, not from here."
