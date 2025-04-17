
# NOTE: This Makefile is only necessary if you
# plan on developing the msgp tool and library.
# Installation can still be performed with a
# normal `go install`.

# generated integration test files
GGEN = ./_generated/generated.go ./_generated/generated_test.go
# generated unit test files
MGEN = ./msgp/defgen_test.go

SHELL := /bin/bash

BIN = $(GOBIN)/msgp

.PHONY: clean wipe install get-deps bench all ci prepare

$(BIN): */*.go
	@go install ./...

install: $(BIN)

$(GGEN): ./_generated/def.go
	go generate ./_generated

$(MGEN): ./msgp/defs_test.go
	go generate ./msgp

test: all
	go test ./... ./_generated

bench: all
	go test -bench ./...

clean:
	$(RM) $(GGEN) $(MGEN)

wipe: clean
	$(RM) $(BIN)

get-deps:
	go get -d -t ./...

all: install $(GGEN) $(MGEN)

# Prepare generated code to be used for linting and testing in CI
prepare:
	go install .
	go generate ./msgp
	go generate ./_generated

# CI enters here
ci: prepare
	arch
	if [ `arch` == 'x86_64' ]; then \
		sudo apt-get -y -q update; \
		sudo apt-get -y -q install build-essential; \
		wget -q https://github.com/tinygo-org/tinygo/releases/download/v0.33.0/tinygo_0.33.0_amd64.deb; \
		sudo dpkg -i tinygo_0.33.0_amd64.deb; \
		export PATH=$$PATH:/usr/local/tinygo/bin; \
	fi
	go test -v ./... ./_generated
