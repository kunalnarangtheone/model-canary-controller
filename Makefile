BUF := buf

# Colors
BOLD  := \033[1m
RESET := \033[0m
GREEN := \033[32m
CYAN  := \033[36m
YELLOW := \033[33m
RED   := \033[31m

.PHONY: proto proto-lint generate-rpcs clean test lint build check

proto: proto-lint generate-rpcs

proto-lint:
	@printf "$(BOLD)$(CYAN)» Linting proto files...$(RESET)\n"
	@$(BUF) lint && printf "$(GREEN)✔ Proto lint passed$(RESET)\n"

generate-rpcs:
	@printf "$(BOLD)$(CYAN)» Generating RPCs from proto files...$(RESET)\n"
	@find . -name 'buf.gen.yaml' -not -path './.venv/*' | while read tmpl; do \
		printf "  $(YELLOW)→ Generating from $$tmpl$(RESET)\n"; \
		$(BUF) generate --template "$$tmpl" "$$(dirname $$tmpl)/proto"; \
	done
	@printf "$(GREEN)✔ RPC generation done$(RESET)\n"

lint:
	@printf "$(BOLD)$(CYAN)» Running linter (golangci-lint)...$(RESET)\n"
	@golangci-lint run ./...
	@printf "$(BOLD)$(CYAN)» Running nil-safety analysis (nilaway)...$(RESET)\n"
	@go tool nilaway ./... && printf "$(GREEN)✔ Lint passed$(RESET)\n"

build:
	@printf "$(BOLD)$(CYAN)» Building Go binaries...$(RESET)\n"
	@find . -name 'main.go' -not -path './.venv/*' | while read f; do \
		dir=$$(dirname $$f); \
		name=$$(basename $$dir); \
		printf "  $(YELLOW)→ Building $$name$(RESET)\n"; \
		go build -o "$$dir/bin/$$name" "$$dir"; \
	done
	@printf "$(GREEN)✔ Build succeeded$(RESET)\n"

test:
	@printf "$(BOLD)$(CYAN)» Running tests...$(RESET)\n"
	@go test -count=1 ./... && printf "$(GREEN)✔ All tests passed$(RESET)\n"

check: build test lint

clean:
	@printf "$(BOLD)$(RED)» Cleaning generated proto files...$(RESET)\n"
	@find . -name 'buf.gen.yaml' -not -path './.venv/*' | while read tmpl; do \
		printf "  $(YELLOW)→ Removing $$(dirname $$tmpl)/proto/generated$(RESET)\n"; \
		rm -rf "$$(dirname $$tmpl)/proto/generated"; \
	done
	@printf "$(GREEN)✔ Clean done$(RESET)\n"
