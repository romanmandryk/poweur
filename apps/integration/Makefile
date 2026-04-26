# Coverage for the API and CLI as exercised by integration tests.
#
# Integration tests import github.com/eurything/api and github.com/eurything/cli
# and run the relay in-process (httptest); the default -cover only counts the
# integration package itself. -coverpkg instruments those dependencies so you
# see real end-to-end coverage.
#
# Usage:
#   make cover        — writes coverage/cover.out, prints merged % for api+cli
#   make cover-html   — + coverage/index.html
#   make cover-func   — per-function list (very long)

COV_DIR := coverage
PROF    := $(COV_DIR)/cover.out
HTML    := $(COV_DIR)/index.html

# Packages to instrument (must be dependencies of this module’s tests).
COVERPKGS := github.com/eurything/api/...,github.com/eurything/cli/...

.PHONY: test cover cover-html cover-func clean-coverage
test:
	go test -count=1 ./...

cover:
	@mkdir -p $(COV_DIR)
	go test -count=1 -covermode=atomic \
		-coverpkg=$(COVERPKGS) \
		-coverprofile=$(PROF) \
		./...
	@echo ""
	@echo "Merged statement coverage (api + cli only, via integration tests):"
	@go tool cover -func=$(PROF) | tail -1
	@echo "Profile: $(PROF)"

cover-func: cover
	go tool cover -func=$(PROF)

cover-html: cover
	go tool cover -html=$(PROF) -o $(HTML)
	@echo "Wrote $(HTML) — source for both api and cli packages in one report."

clean-coverage:
	rm -rf $(COV_DIR)
