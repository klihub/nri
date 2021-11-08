#   Copyright The containerd Authors.

#   Licensed under the Apache License, Version 2.0 (the "License");
#   you may not use this file except in compliance with the License.
#   You may obtain a copy of the License at

#       http://www.apache.org/licenses/LICENSE-2.0

#   Unless required by applicable law or agreed to in writing, software
#   distributed under the License is distributed on an "AS IS" BASIS,
#   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#   See the License for the specific language governing permissions and
#   limitations under the License.

PROTO_SOURCES = $(shell find . -name '*.proto' | grep -v /vendor/)
PROTO_GOFILES = $(patsubst %.proto,%.pb.go,$(PROTO_SOURCES))
PROTO_INCLUDE = $(HOME)/go/src $(shell go env GOPATH)/src
PROTO_MODULES = # gogoproto/gogo.proto=github.com/gogo/protobuf/gogoproto

TTRPC_INCLUDE = $(foreach dir,$(PROTO_INCLUDE),-I$(dir))
TTRPC_MODULES = $(foreach mod,$(PROTO_MODULES),--gogottrpc_opt=M$(mod))
TTRPC_OPTIONS = $(TTRPC_INCLUDE) $(TTRPC_MODULES) --gogottrpc_opt=paths=source_relative
TTRPC_COMPILE = protoc $(TTRPC_OPTIONS)

GO_CMD     := go
GO_BUILD   := $(GO_CMD) build
GO_INSTALL := $(GO_CMD) install
GO_FETCH   := GO111MODULE=off go get -u
GO_TEST    := $(GO_CMD) test
GO_MODULES := $(shell $(GO_CMD) list ./... | grep -v vendor/)

PLUGINS := bin/logger bin/hook-injector bin/cdi-resolver bin/device-injector

all: build

build: protos binaries
	go build -v $(shell go list ./...)

protos: $(PROTO_GOFILES)

binaries: $(PLUGINS)

%.pb.go: %.proto
	@echo "Generating $@..."; \
        PATH=$(PATH):$(shell go env GOPATH)/bin; \
	$(TTRPC_COMPILE) -I$(dir $<) --gogottrpc_out=plugins=ttrpc:$(dir $<) $<

bin/logger: $(wildcard v2alpha1/plugins/logger/*.go)
	@echo "Building $@..."; \
	$(GO_BUILD) -o $@ ./$(dir $<)

bin/hook-injector: $(wildcard v2alpha1/plugins/hook-injector/*.go)
	@echo "Building $@..."; \
	$(GO_BUILD) -o $@ ./$(dir $<)

bin/cdi-resolver: $(wildcard v2alpha1/plugins/cdi-resolver/*.go)
	@echo "Building $@..."; \
	$(GO_BUILD) -o $@ ./$(dir $<)

bin/device-injector: $(wildcard v2alpha1/plugins/device-injector/*.go)
	@echo "Building $@..."; \
	$(GO_BUILD) -o $@ ./$(dir $<)

install-ttrpc-plugin:
	$(GO_INSTALL) github.com/containerd/ttrpc/cmd/protoc-gen-gogottrpc

install-proto-dependencies:
	$(GO_FETCH) github.com/gogo/protobuf/gogoproto

test: unittest

unittest:
	@for m in $(GO_MODULES); do \
	    $(GO_TEST) -race $$m || exit $?; \
	done
