# Copyright 2018 The Trickster Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

-include ci.mk release.mk

DEFAULT: build

PROJECT_DIR    := $(shell pwd)
GO             ?= go
GOFMT          ?= $(GO)fmt
FIRST_GOPATH   := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))
TRICKSTER_MAIN := cmd/trickster
TRICKSTER      := $(FIRST_GOPATH)/bin/trickster
BUILD_TIME     := $(shell date -u +%FT%T%z)
GIT_LATEST_COMMIT_ID     ?= $(shell git rev-parse HEAD)
IMAGE_TAG      ?= latest
IMAGE_ARCH     ?= $(shell $(GO) env GOARCH)
GOARCH         ?= $(shell $(GO) env GOARCH)
TAGVER         ?= $(shell git describe --tags --dirty --always)
LDFLAGS         =-ldflags "-extldflags '-static' -w -s -X main.applicationBuildTime=$(BUILD_TIME) -X main.applicationGitCommitID=$(GIT_LATEST_COMMIT_ID) -X main.applicationVersion=$(TAGVER)"
BUILD_SUBDIR   := bin
PACKAGE_DIR    := ./$(BUILD_SUBDIR)/trickster-$(TAGVER)
BIN_DIR        := $(PACKAGE_DIR)/bin
CONF_DIR       := $(PACKAGE_DIR)/conf
CGO_ENABLED    ?= 0
BUMPER_FILE    := ./testdata/license_header_template.txt

.PHONY: go-mod-vendor
go-mod-vendor:
	$(GO) mod vendor

.PHONY: go-mod-tidy
go-mod-tidy:
	$(GO) mod tidy

.PHONY: test-go-mod
test-go-mod:
	@git diff --quiet --exit-code go.mod go.sum || echo "There are changes to go.mod and go.sum which needs to be committed"

BUILD_FLAGS ?= -a -v
.PHONY: build
build: go-mod-tidy go-mod-vendor
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) $(BUILD_FLAGS) -o ./$(BUILD_SUBDIR)/trickster  $(TRICKSTER_MAIN)/*.go

rpm: build
	mkdir -p ./$(BUILD_SUBDIR)/SOURCES
	cp -p ./$(BUILD_SUBDIR)/trickster ./$(BUILD_SUBDIR)/SOURCES/
	cp deploy/systemd/trickster.service ./$(BUILD_SUBDIR)/SOURCES/
	sed -e 's%^# log_file:.*$$%log_file: /var/log/trickster/trickster.log%' \
		-e 's%prometheus:9090%localhost:9090%' \
		< examples/conf/example.full.yaml > ./$(BUILD_SUBDIR)/SOURCES/trickster.yaml
	rpmbuild --define "_topdir $(CURDIR)/$(BUILD_SUBDIR)" \
		--define "_version $(TAGVER)" \
		--define "_release 1" \
		-ba deploy/packaging/trickster.spec

.PHONY: install
install:
	$(GO) install -o $(TRICKSTER) $(TAGVER)

# Minikube and helm bootstrapping are done via deploy/helm/Makefile
.PHONY: helm-local
helm-local:
	kubectl config use-context minikube --namespace=trickster
	kubectl scale --replicas=0 deployment/dev-trickster -n trickster
	eval $$(minikube docker-env) \
		&& docker build -f deploy/Dockerfile -t trickster:dev .
	kubectl set image deployment/dev-trickster trickster=trickster:dev -n trickster
	kubectl scale --replicas=1 deployment/dev-trickster -n trickster

# Minikube and helm bootstrapping are done via deploy/kube/Makefile
.PHONY: kube-local
kube-local:
	kubectl config use-context minikube
	kubectl scale --replicas=0 deployment/trickster
	eval $$(minikube docker-env) \
		&& docker build -f deploy/Dockerfile -t trickster:dev .
	kubectl set image deployment/trickster trickster=trickster:dev
	kubectl scale --replicas=1 deployment/trickster

DOCKER_TARGET ?= final
.PHONY: docker
docker:
	docker buildx build \
		--progress=plain \
		--build-arg IMAGE_ARCH=$(IMAGE_ARCH) \
		--build-arg GIT_LATEST_COMMIT_ID=$(GIT_LATEST_COMMIT_ID) \
		--target $(DOCKER_TARGET) \
		--build-arg GOARCH=$(GOARCH) \
		--build-arg TAGVER=$(TAGVER) \
		-f ./Dockerfile \
		-t trickster:$(TAGVER) \
		--platform linux/$(IMAGE_ARCH) \
		.

.PHONY: docker-release
docker-release:
# linux x86 image
	docker build --build-arg IMAGE_ARCH=amd64 --build-arg GOARCH=amd64 -f ./deploy/Dockerfile -t trickstercache/trickster:$(IMAGE_TAG) .
# linux arm image
	docker build --build-arg IMAGE_ARCH=arm64v8 --build-arg GOARCH=arm64 -f ./deploy/Dockerfile -t trickstercache/trickster:arm64v8-$(IMAGE_TAG) .

.PHONY: style
style:
	! gofmt -d $$(find . -path ./vendor -prune -o -name '*.go' -print) | grep '^'

.PHONY: lint
lint:
	@golangci-lint run

GO_TEST_FLAGS ?= -coverprofile=.coverprofile
.PHONY: test
test: check-license-headers check-codegen gotest check-fmtprints check-todos

.PHONY: gotest
gotest:
	go test -timeout=5m -v ${GO_TEST_FLAGS} ./...

.PHONY: data-race-test
data-race-test:
	GO_TEST_FLAGS="-race" $(MAKE) test | tee race-output.log

.PHONY: data-race-test-inspect
data-race-test-inspect:
	./hack/inspect-race-output.sh race-output.log

.PHONY: bench
bench:
	bash -c "$(GO) test -v -coverprofile=.coverprofile ./... -run=nonthingplease -bench=. | grep -v ' app=trickster '; exit ${PIPESTATUS[0]}"

.PHONY: test-cover
test-cover: test
	$(GO) tool cover -html=.coverprofile

.PHONY: clean
clean:
	rm -rf ./trickster ./$(BUILD_SUBDIR)

.PHONY: generate
generate: perform-generate insert-license-headers

.PHONY: perform-generate
perform-generate:
	$(GO) generate ./pkg/... ./cmd/...

.PHONY: insert-license-headers
insert-license-headers:
	@for file in $$(find ./pkg ./cmd -name '*.go') ; \
	do \
		output=$$(grep 'Licensed under the Apache License' $$file) ; \
		if [ "$$?" != "0" ]; then \
			echo "adding License Header Block to $$file" ; \
			cat $(BUMPER_FILE) > /tmp/trktmp.go ; \
			cat $$file >> /tmp/trktmp.go ; \
			mv /tmp/trktmp.go $$file ; \
		fi ; \
	done

CODEGEN_PATHS ?= "'./pkg/**_gen.go'"
.PHONY: check-codegen
check-codegen:
	@$(MAKE) generate > /dev/null
	@git diff --name-only --exit-code ${CODEGEN_PATHS}

.PHONY: check-license-headers
check-license-headers: SHELL:=/bin/sh
check-license-headers:
	@for file in $$(find ./pkg ./cmd -name '*.go') ; \
	do \
		output=$$(grep 'Licensed under the Apache License' $$file) ; \
		if [ "$$?" != "0" ]; then \
			echo "" ; \
			echo "Some project code files do not have the Trickster / Apache 2.0 license header." ; \
			echo "Run 'make insert-license-headers' and commit the changes." ; \
			echo "" ; \
			exit 1 ; \
		fi ; \
	done ; \
	echo "" ; echo "\033[1;32m✓\033[0m All code files have the required license header." ; echo ""

.PHONY: check-fmtprints
check-fmtprints: SHELL:=/bin/sh
check-fmtprints: # fails if there are any fmt.Print* calls outside of the 3 approved files
	@cd pkg && \
	fmtprints=$$(git grep -n fmt.Print | grep -v 'appinfo/usage/usage.go' | grep -v '^daemon/'); \
	count=0; \
	if [ -n "$$fmtprints" ]; then \
		count="$$(echo "$$fmtprints" | wc -l | tr -d '[:space:]')" ; \
	fi; \
	if [ "$$count" -ne 0 ]; then \
		echo "" ; \
		echo "\033[1;31m⨉\033[0m ($$count) unexpected fmt.Print*(s) must be removed from the codebase:"; \
		echo "" ; \
		echo "$$fmtprints" ; \
		echo "" ; \
		echo "" ; \
		exit 1; \
	fi ; \
	echo "" ; echo "\033[1;32m✓\033[0m No unexpected fmt.Print* calls." ; echo ""

.PHONY: check-todos
check-todos: SHELL:=/bin/sh
check-todos: # there are 11 known "TODO"s in the codebase. This check fails if more are added.
	@cd pkg && \
	todos=$$(git grep -in todo | grep -v 'context\.TODO'); \
	count=0; \
	if [ -n "$$todos" ]; then \
		count="$$(echo "$$todos" | wc -l | tr -d '[:space:]')" ; \
	fi; \
	KNOWN_TODO_COUNT=7 ; \
	if [ "$$count" -gt $$KNOWN_TODO_COUNT ]; then \
		newtodos=$$(($$count - $$KNOWN_TODO_COUNT)) ; \
		echo "" ; \
		echo "\033[1;31m$$newtodos new TODOs found in the codebase.\033[0m Do not add any new TODOs to the codebase." ;\
		echo "" ; \
		echo "All TODOs:" ; \
		echo "" ; \
		echo "$$todos" | cut -b 1-100 ; \
		echo "" ; \
		echo "" ; \
		exit 1; \
	fi ; \
	echo "" ; echo "\033[1;32m✓\033[0m No new TODOs found." ; echo ""

.PHONY: spelling
spelling:
	@which mdspell ; \
	if [ "$$?" != "0" ]; then \
		echo "mdspell is not installed" ; \
	else \
		mdspell './README.md' './docs/**/*.md' ; \
	fi
	@which codespell ; \
	if [ "$$?" != "0" ]; then \
		echo "codespell is not installed" ; \
	else \
		codespell --skip='vendor,*.git,*.png,*.pdf,*.tiff,*.plist,*.pem,rangesim*.go,*.gz,go.sum,go.mod' --ignore-words='./testdata/ignore_words.txt' ; \
	fi

.PHONY: serve
serve:
	@cd cmd/trickster && go run . -config /etc/trickster/trickster.yaml

.PHONY: serve-debug
serve-debug:
	@cd cmd/trickster && go run . -config /etc/trickster/trickster.yaml --log-level debug

.PHONY: serve-info
serve-info:
	@cd cmd/trickster && go run . -config /etc/trickster/trickster.yaml --log-level info

.PHONY: serve-cli
serve-cli:
	@cd cmd/trickster && go run . -origin-url http://127.0.0.1:9090/ -provider prometheus

.PHONY: get-tools
get-tools: get-msgpack
	@echo "Installing tools..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.0.2
	go install honnef.co/go/tools/cmd/staticcheck@2025.1.1

.PHONY: get-msgpack
get-msgpack:
	$(GO) get -tool github.com/tinylib/msgp@v1.2.5

.PHONY: developer-start
developer-start:
	@cd docs/developer/environment && docker compose up -d
	
.PHONY: developer-stop
developer-stop:
	@cd docs/developer/environment && docker compose stop

.PHONY: developer-delete
developer-delete:
	@cd docs/developer/environment && docker compose down

.PHONY: developer-restart
developer-restart:
	@cd docs/developer/environment && docker compose down && docker compose up -d

.PHONY: developer-seed-data
developer-seed-data:
	@cd docs/developer/environment && docker compose run --rm clickhouse_seed

.PHONY: serve-dev
serve-dev:
	@go run cmd/trickster/main.go -config $(if $(TRK_CONFIG),$(TRK_CONFIG),docs/developer/environment/trickster-config/trickster.yaml)
