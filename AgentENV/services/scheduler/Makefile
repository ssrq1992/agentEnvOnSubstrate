ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)
GO   ?= go

.PHONY: build test fmt fmt-check vet

build:
	cd $(ROOT) && $(GO) build -o bin/scheduler ./scheduler/cmd

test:
	cd $(ROOT) && $(GO) test ./scheduler/...

fmt:
	cd $(ROOT) && gofmt -w $$(find scheduler shared api -name '*.go' -type f)

fmt-check:
	@out=$$(cd $(ROOT) && gofmt -l $$(find scheduler shared api -name '*.go' -type f)); \
	 test -z "$$out" || (printf 'Files need formatting:\n%s\n' "$$out"; exit 1)

vet:
	cd $(ROOT) && $(GO) vet ./scheduler/... ./shared/... ./api/...
