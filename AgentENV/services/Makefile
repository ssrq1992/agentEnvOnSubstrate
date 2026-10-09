PROTOC    ?= protoc
GO        ?= go
GOBIN_DIR := $(shell $(GO) env GOPATH)/bin
PROTO_SRC := api/proto/scheduler.proto

.PHONY: services proto tidy build test fmt fmt-check vet run-scheduler run-gateway

proto:
	PATH="$(GOBIN_DIR):$$PATH" $(PROTOC) \
		--go_out=paths=source_relative:. \
		--go-grpc_out=paths=source_relative:. \
		$(PROTO_SRC)

tidy:
	$(GO) mod tidy

build:
	$(MAKE) -C gateway build
	$(MAKE) -C scheduler build

test:
	$(MAKE) -C gateway test
	$(MAKE) -C scheduler test

fmt:
	$(MAKE) -C gateway fmt
	$(MAKE) -C scheduler fmt

fmt-check:
	$(MAKE) -C gateway fmt-check
	$(MAKE) -C scheduler fmt-check

vet:
	$(MAKE) -C gateway vet
	$(MAKE) -C scheduler vet

run-scheduler:
	$(GO) run ./scheduler/cmd -config ./config/local.json

run-gateway:
	$(GO) run ./gateway/cmd -config ./config/local.json
