# matterbox — build + per-user install (no root required).
#
# Targets:
#   make            build ./matterbox
#   make install    build, copy to ~/.local/bin, install shell completion
#   make uninstall  remove the binary and completion files
#   make test/vet/fmt/clean/run  the usual dev helpers
#
# Override the install location with PREFIX, e.g.  make install PREFIX=~/apps

BINARY := matterbox
PKG    := .
GO     ?= go

# User-level install prefix. ~/.local/bin is already on this user's PATH.
PREFIX ?= $(HOME)/.local
BINDIR := $(PREFIX)/bin

# Per-user completion directories (XDG data/config — no root needed).
ZSH_COMPDIR  := $(HOME)/.local/share/zsh/site-functions
BASH_COMPDIR := $(HOME)/.local/share/bash-completion/completions
FISH_COMPDIR := $(HOME)/.config/fish/completions

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the matterbox binary into the repo root
	$(GO) build -o $(BINARY) $(PKG)

.PHONY: install
install: build install-completion ## Install binary + completion for the current user
	@install -d "$(BINDIR)"
	@install -m 0755 "$(BINARY)" "$(BINDIR)/$(BINARY)"
	@echo "installed $(BINDIR)/$(BINARY)"
	@case ":$$PATH:" in \
		*":$(BINDIR):"*) ;; \
		*) echo "note: $(BINDIR) is not on your PATH — add it to use '$(BINARY)' directly";; \
	esac

# Detect the user's shell and install the matching completion script. Falls
# back to zsh (this machine's login shell) when $SHELL is unset.
.PHONY: install-completion
install-completion: build ## Generate + install shell completion for the current shell
	@shell=$$(basename "$${SHELL:-/usr/bin/zsh}"); \
	case "$$shell" in \
	zsh) \
		install -d "$(ZSH_COMPDIR)"; \
		./$(BINARY) completion zsh > "$(ZSH_COMPDIR)/_$(BINARY)"; \
		echo "installed zsh completion -> $(ZSH_COMPDIR)/_$(BINARY)"; \
		if ! grep -qsF "$(ZSH_COMPDIR)" "$(HOME)/.zshrc"; then \
			printf '\n# matterbox completion (added by make install)\nfpath=(%s $$fpath)\nautoload -Uz compinit\ncompinit\n' "$(ZSH_COMPDIR)" >> "$(HOME)/.zshrc"; \
			echo "added fpath entry to ~/.zshrc — run 'source ~/.zshrc' or open a new shell"; \
		else \
			echo "~/.zshrc already references the completion dir — run 'compinit' or open a new shell"; \
		fi ;; \
	bash) \
		install -d "$(BASH_COMPDIR)"; \
		./$(BINARY) completion bash > "$(BASH_COMPDIR)/$(BINARY)"; \
		echo "installed bash completion -> $(BASH_COMPDIR)/$(BINARY)"; \
		echo "ensure bash-completion is loaded (most distros source $(BASH_COMPDIR) automatically)"; \
		;; \
	fish) \
		install -d "$(FISH_COMPDIR)"; \
		./$(BINARY) completion fish > "$(FISH_COMPDIR)/$(BINARY).fish"; \
		echo "installed fish completion -> $(FISH_COMPDIR)/$(BINARY).fish"; \
		;; \
	*) \
		echo "unknown shell '$$shell' — skipping completion (run './$(BINARY) completion --help')"; \
		;; \
	esac

.PHONY: uninstall
uninstall: ## Remove the installed binary and completion files
	@rm -f "$(BINDIR)/$(BINARY)" && echo "removed $(BINDIR)/$(BINARY)" || true
	@rm -f "$(ZSH_COMPDIR)/_$(BINARY)" "$(BASH_COMPDIR)/$(BINARY)" "$(FISH_COMPDIR)/$(BINARY).fish" 2>/dev/null || true
	@echo "removed completion scripts (the fpath line in ~/.zshrc, if added, is left for you to remove)"

.PHONY: test
test: ## Run the test suite
	$(GO) test ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go sources
	$(GO) fmt ./...

.PHONY: run
run: ## Build and launch the TUI
	$(GO) run $(PKG)

.PHONY: clean
clean: ## Remove the built binary
	@rm -f "$(BINARY)" && echo "removed ./$(BINARY)" || true

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
