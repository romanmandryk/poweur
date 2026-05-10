# Test coverage (stdlib only; no extra installs).
# Usage:
#   make cover        — merged profile in coverage/cover.out; instruments all
#                       packages so tests in pkg/relay count toward internal/* too
#   make cover-html   — same + coverage/index.html (open in a browser)
#   make cover-func   — per-function list (longer than cover)
#   make test         — tests without a coverage profile

COV_DIR := coverage
PROF    := $(COV_DIR)/cover.out
HTML    := $(COV_DIR)/index.html
# Count every package in this module toward coverage (matches `go help test` -coverpkg).
COVERPKG := github.com/poweur/api/...

.PHONY: test cover cover-html cover-func clean-coverage
test:
	go test -count=1 ./...

cover:
	@mkdir -p $(COV_DIR)
	go test -count=1 -covermode=atomic -coverpkg=$(COVERPKG) -coverprofile=$(PROF) ./...
	@echo ""
	@echo "Merged total (all instrumented packages):"
	@go tool cover -func=$(PROF) | tail -1
	@echo "Profile: $(PROF) — e.g. go tool cover -html=$(PROF)"

cover-func: cover
	go tool cover -func=$(PROF)

cover-html: cover
	go tool cover -html=$(PROF) -o $(HTML)
	@echo "Wrote $(HTML) — open it to browse uncovered lines in context."

clean-coverage:
	rm -rf $(COV_DIR)
