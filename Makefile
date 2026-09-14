# Stays on Make, no value in moving to Just.
#
# A public repo also pays twice: contributors need a new tool, and CI,
# which pins every action to a SHA, gains unpinned supply-chain surface.

all: lint test build

BINDIR   := bin
COVERDIR := coverage
GO       := go
PACKAGES := ./...

# If _RAW_GOBIN is empty, take the first element in _RAW_GOPATH
# Replace Windows/UNIX directory separators to create a "make" list.
# Then take the first word and append /bin
_RAW_GOBIN  := $(shell $(GO) env GOBIN)
_RAW_GOPATH := $(shell $(GO) env GOPATH)
GOBIN := $(if $(_RAW_GOBIN),$(_RAW_GOBIN),$(firstword $(subst :, ,$(subst ;, ,$(_RAW_GOPATH))))/bin)

.PHONY: all build clean cover install lint modernize test

lint: modernize
	$(GO) tool staticcheck -checks=all $(PACKAGES)

# go fix exits non-zero when fixes are pending, and prints them as a patch,
# so a failure here already says what to apply.
modernize:
	$(GO) fix -diff $(PACKAGES)

cover:
	mkdir -p $(COVERDIR)
	$(GO) test -v -race -coverprofile=$(COVERDIR)/cover.out -covermode=atomic $(PACKAGES)
	$(GO) tool cover -html=$(COVERDIR)/cover.out

test:
	$(GO) test -race $(PACKAGES)

# go build discards its result when handed more than one package,
# so $(PACKAGES) would check that the command builds without ever producing it.
build:
	$(GO) build -o $(BINDIR)/ .

install: test build
	$(GO) install .

clean:
	@echo GOBIN: $(GOBIN)
	rm -fr $(COVERDIR) $(BINDIR)/drupal_warmup $(GOBIN)/drupal_warmup
