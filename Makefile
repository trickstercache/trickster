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
THIRD_PARTY_LICENSES_DIR  := $(BUILD_SUBDIR)/third-party-licenses
GOLANG_CI_LINT_VERSION ?= v2.13.1

.PHONY: go-mod-vendor
go-mod-vendor:
	$(GO) mod vendor

.PHONY: go-mod-tidy
go-mod-tidy:
	$(GO) mod tidy

.PHONY: test-go-mod
test-go-mod:
	@git diff --quiet --exit-code go.mod go.sum || echo "There are changes to go.mod and go.sum which needs to be committed"

# go-jmespath ships an abbreviated Apache-2.0 license notice that go-licenses
# cannot classify. The target excludes it from automatic classification only,
# then copies its original license notice into the generated distribution.
.PHONY: third-party-licenses
third-party-licenses:
	$(GO) tool go-licenses save ./cmd/trickster \
		--force \
		--save_path=$(THIRD_PARTY_LICENSES_DIR) \
		--ignore=github.com/trickstercache/trickster/v2 \
		--ignore=github.com/jmespath/go-jmespath
	@jmespath_dir="$$($(GO) list -mod=mod -m -f '{{.Dir}}' github.com/jmespath/go-jmespath)"; \
		mkdir -p "$(THIRD_PARTY_LICENSES_DIR)/github.com/jmespath/go-jmespath"; \
		cp "$$jmespath_dir/LICENSE" \
			"$(THIRD_PARTY_LICENSES_DIR)/github.com/jmespath/go-jmespath/LICENSE"
	@test -s "$(THIRD_PARTY_LICENSES_DIR)/vitess.io/vitess/go/LICENSE"

.PHONY: check-third-party-licenses
check-third-party-licenses: third-party-licenses
	@echo "verified release notices for Vitess and the complete linked dependency graph"

BUILD_FLAGS ?= -a -v
.PHONY: build
build: go-mod-tidy go-mod-vendor
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) $(BUILD_FLAGS) -o ./$(BUILD_SUBDIR)/trickster  $(TRICKSTER_MAIN)/*.go

rpm: build third-party-licenses
	mkdir -p ./$(BUILD_SUBDIR)/SOURCES
	cp -p ./$(BUILD_SUBDIR)/trickster ./$(BUILD_SUBDIR)/SOURCES/
	cp deploy/systemd/trickster.service ./$(BUILD_SUBDIR)/SOURCES/
	cp -p LICENSE NOTICE ./$(BUILD_SUBDIR)/SOURCES/
	cp -R ./$(THIRD_PARTY_LICENSES_DIR) ./$(BUILD_SUBDIR)/SOURCES/
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

.PHONY: check-imports
check-imports:
	@go run hack/check-imports/main.go

.PHONY: gofix-apply
gofix-apply:
	@go fix ./...

.PHONY: gofix-diff
gofix-diff:
	@go fix -diff ./...

LINT_FLAGS ?= 
.PHONY: golangci-lint
golangci-lint:
	@go tool golangci-lint run $(LINT_FLAGS) -c .golangci.yml

.PHONY: lint
lint: check-imports spelling vulncheck gofix-diff golangci-lint

.PHONY: vulncheck
vulncheck:
	@go tool govulncheck ./...

.PHONY: benchmark-mysql-smoke
benchmark-mysql-smoke:
	@go test ./pkg/backends/mysql -run '^$$' -bench '^BenchmarkMySQLSmoke$$' -benchtime=1x -benchmem

.PHONY: benchmark-mysql
benchmark-mysql:
	@go test ./pkg/backends/mysql -run '^$$' -bench '^BenchmarkMySQL' -benchmem -count=5
	@go test ./pkg/backends/alb/mech/ur -run '^$$' -bench '^BenchmarkResolveRouteProtocolNeutral$$' -benchmem -count=5

.PHONY: benchmark-graphite
benchmark-graphite:
	@go test ./pkg/backends/graphite/resolution -run '^$$' -bench '^BenchmarkResolver' \
		-benchmem -count=5

.PHONY: benchmark-mysql-acceptance
benchmark-mysql-acceptance:
	@go test ./pkg/backends/mysql -run '^$$' -bench '^BenchmarkMySQLCompatibilityCorpus$$' \
		-benchmem -benchtime=200ms | tee /tmp/trickster-mysql-benchmarks.txt
	@awk -f hack/check-mysql-benchmarks.awk /tmp/trickster-mysql-benchmarks.txt

.PHONY: lint-fix
lint-fix:
	@go fix ./...
	@LINT_FLAGS="--fix" $(MAKE) lint
	@go tool golangci-lint fmt -c .golangci.yml

GO_TEST_FLAGS ?= -coverprofile=.coverprofile
.PHONY: test
test: check-license-headers check-codegen gotest check-fmtprints check-todos

GO_TEST_PATH ?= $(shell $(GO) list ./... | grep -v v2/integration)
.PHONY: gotest
gotest:
	$(GO) test -timeout=5m -v ${GO_TEST_FLAGS} $(GO_TEST_PATH)
	@./hack/filter-coverprofile.sh .coverprofile
	@echo
	@./hack/coverprofile-summary.sh

.PHONY: data-race-test
data-race-test:
	GO_TEST_FLAGS="-race" $(MAKE) test | tee race-output.log

.PHONY: data-race-test-inspect
data-race-test-inspect:
	./hack/inspect-race-output.sh race-output.log

.PHONY: integration-test
integration-test:
	$(MAKE) -C integration test
	$(MAKE) -C integration data-race-test

.PHONY: integration-cover
integration-cover:
	$(MAKE) -C integration cover

FUZZ_TIME ?= 30s

.PHONY: fuzz
fuzz:
	@for pkg in $$($(GO) list ./... | grep -v /vendor/); do \
		fuzz_funcs=$$($(GO) test -list 'Fuzz.*' $$pkg 2>/dev/null | grep '^Fuzz'); \
		for fn in $$fuzz_funcs; do \
			echo "fuzzing $$fn in $$pkg ($(FUZZ_TIME))"; \
			$(GO) test -fuzz=$$fn -fuzztime=$(FUZZ_TIME) $$pkg || exit 1; \
		done; \
	done

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
	cd integration && $(GO) generate ./...

.PHONY: insert-license-headers
insert-license-headers:
	@for file in $$(find ./pkg ./cmd ./integration -name '*.go') ; \
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

.PHONY: install-codespell
install-codespell:
	# if brew is available, use it to install codespell
	@which codespell ; \
	if [ "$$?" != "0" ]; then \
		if which brew ; then \
			brew install codespell ; \
		else \
			echo "codespell is not installed and brew is not available to install it" ; \
		fi ; \
	else \
		echo "codespell is already installed" ; \
	fi

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
	go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANG_CI_LINT_VERSION)

.PHONY: get-msgpack
get-msgpack:
	$(GO) get -tool github.com/tinylib/msgp@$(shell go list -m github.com/tinylib/msgp | cut -d' ' -f2)

.PHONY: developer-start
developer-start:
	@cd docs/developer/environment && docker compose up -d
	@echo "Waiting for Redis to be ready..."
	@cd docs/developer/environment && if ! timeout 30 sh -c \
		'until response=$$(docker compose exec -T redis redis-cli ping 2>&1); do \
			echo "PING -> $${response:-no response}"; \
			sleep 1; \
		done; \
		echo "PING -> $$response"'; then \
		echo "WARNING: timed out waiting for Redis readiness; continuing anyway"; \
	fi
	@echo "Waiting for Prometheus to be ready..."
	@timeout 120 sh -c 'until curl -sf http://127.0.0.1:9090/-/ready >/dev/null 2>&1; do sleep 2; done'
	@echo "Waiting for Graphite to be ready..."
	@timeout 120 sh -c 'until curl -sf "http://127.0.0.1:8081/metrics/find?query=carbon" >/dev/null 2>&1; do sleep 2; done'
	
.PHONY: developer-stop
developer-stop:
	@cd docs/developer/environment && docker compose stop


# --- Integration-only container toggling -------------------------------------
# The compose file carries integration-only services (e.g. coredns for the
# autodiscovery DNS tests) commented out between the
# "-- INTEGRATION CONTAINERS BELOW --" / "-- ABOVE --" markers, so developer
# workstations never run them. integration-start uncomments that section,
# seeds the mutable CoreDNS zone directory, and brings the environment up;
# integration-stop stops the environment and comments the section back out.

COMPOSE_ENV_DIR := docs/developer/environment
COMPOSE_YML     := $(COMPOSE_ENV_DIR)/docker-compose.yml
COREDNS_ZONES   := $(COMPOSE_ENV_DIR)/docker-compose-data/coredns-zones
# services defined only in the integration section of the compose file
INTEGRATION_SERVICES := coredns

.PHONY: integration-env-enable
integration-env-enable:
	@awk 'BEGIN{p=0} \
		/-- INTEGRATION CONTAINERS BELOW --/{p=1;print;next} \
		/-- INTEGRATION CONTAINERS ABOVE --/{p=0;print;next} \
		p==1 && /^#/{sub(/^#/,"");print;next} \
		{print}' $(COMPOSE_YML) > $(COMPOSE_YML).tmp \
		&& mv $(COMPOSE_YML).tmp $(COMPOSE_YML)
	@mkdir -p $(COREDNS_ZONES)
	@cp -f $(COMPOSE_ENV_DIR)/docker-compose-data/coredns/trickster.test.db.seed \
		$(COREDNS_ZONES)/trickster.test.db
	@echo "integration containers enabled in $(COMPOSE_YML)"

.PHONY: integration-env-disable
integration-env-disable:
	@awk 'BEGIN{p=0} \
		/-- INTEGRATION CONTAINERS BELOW --/{p=1;print;next} \
		/-- INTEGRATION CONTAINERS ABOVE --/{p=0;print;next} \
		p==1 && !/^[[:space:]]*#/ && !/^[[:space:]]*$$/{print "#" $$0;next} \
		{print}' $(COMPOSE_YML) > $(COMPOSE_YML).tmp \
		&& mv $(COMPOSE_YML).tmp $(COMPOSE_YML)
	@echo "integration containers disabled in $(COMPOSE_YML)"

.PHONY: integration-start
integration-start: integration-env-enable developer-start

.PHONY: integration-stop
integration-stop:
	@$(MAKE) integration-env-enable >/dev/null
	@# stop-and-remove the integration-only containers so a restart policy
	@# cannot resurrect them on developer machines
	@cd $(COMPOSE_ENV_DIR) && docker compose rm -sf $(INTEGRATION_SERVICES)
	@$(MAKE) developer-stop
	@$(MAKE) integration-env-disable

.PHONY: integration-delete
integration-delete:
	@$(MAKE) integration-env-enable >/dev/null
	@$(MAKE) developer-delete
	@$(MAKE) integration-env-disable

# --- End Integration-only container toggling ---------------------------------


.PHONY: developer-delete
developer-delete:
	@cd docs/developer/environment && docker compose down -v --remove-orphans

.PHONY: developer-recreate
developer-recreate: developer-delete
	@cd docs/developer/environment && docker compose up -d

.PHONY: developer-seed-data
developer-seed-data:
	@cd docs/developer/environment && docker compose up -d --wait clickhouse mysql
	@cd docs/developer/environment && docker compose run --rm seed_data_fetch
	@cd docs/developer/environment && \
	docker compose run --rm --no-deps clickhouse_seed & pid1=$$!; \
	( cd docs/developer/environment && docker compose run --rm --no-deps mysql_seed ) & pid2=$$!; \
	rc=0; wait $$pid1 || rc=1; wait $$pid2 || rc=1; exit $$rc
	@cd docs/developer/environment && docker compose stop graphite_generator && \
		docker compose run --rm -e GRAPHITE_SEED_FORCE=1 graphite_seed && \
		docker compose up -d graphite_generator

RUN_FLAGS ?=
.PHONY: serve-dev
serve-dev:
	@go run $(RUN_FLAGS) cmd/trickster/main.go -config $(if $(TRK_CONFIG),$(TRK_CONFIG),docs/developer/environment/trickster-config/trickster.yaml)

serve-dev-data-race:
	RUN_FLAGS=-race $(MAKE) serve-dev 2>&1 | tee race-output.log

# --- Kubernetes autodiscovery integration scenario ---------------------------
# Creates the kind cluster for integration/kind (TestALBDiscoveryKind),
# builds the trickster image, loads it, and deploys the manifests.
KIND_CLUSTER := trickster-it

.PHONY: kind-integration-start
kind-integration-start:
	kind create cluster --config integration/kind/kind-config.yaml
	docker build -t trickster:integration .
	kind load docker-image trickster:integration --name $(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) apply -f integration/kind/manifests.yaml
	kubectl --context kind-$(KIND_CLUSTER) -n trickster-it rollout status \
		deployment/webecho deployment/trickster --timeout=180s

.PHONY: kind-integration-stop
kind-integration-stop:
	kind delete cluster --name $(KIND_CLUSTER)
