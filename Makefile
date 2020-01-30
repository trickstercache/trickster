# Copyright 2018 Comcast Cable Communications Management, LLC
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
# http://www.apache.org/licenses/LICENSE-2.0
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

DEFAULT: build

GO             ?= go
GOFMT          ?= $(GO)fmt
FIRST_GOPATH   := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))
TRICKSTER_MAIN := cmd/trickster
TRICKSTER      := $(FIRST_GOPATH)/bin/trickster
PROGVER        := $(shell grep 'applicationVersion = ' $(TRICKSTER_MAIN)/main.go | awk '{print $$3}' | sed -e 's/\"//g')
BUILD_TIME     := $(shell date -u +%FT%T%z)
GIT_LATEST_COMMIT_ID     := $(shell git rev-parse HEAD)
GO_VER         := $(shell go version | awk '{print $$3}')
LDFLAGS=-ldflags "-s -X main.applicationBuildTime=$(BUILD_TIME) -X main.applicationGitCommitID=$(GIT_LATEST_COMMIT_ID) -X main.applicationGoVersion=$(GO_VER) -X main.applicationGoArch=$(GOARCH)"
GO111MODULE    ?= on
export GO111MODULE

.PHONY: go-mod-vendor
go-mod-vendor:
	$(GO) mod vendor

.PHONY: go-mod-tidy
go-mod-tidy:
	$(GO) mod tidy

.PHONY: test-go-mod
test-go-mod:
	@git diff --quiet --exit-code go.mod go.sum || echo "There are changes to go.mod and go.sum which needs to be committed"

.PHONY: build
build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) -o ./OPATH/trickster -a -v $(TRICKSTER_MAIN)/main.go 

rpm: build
	mkdir -p ./OPATH/SOURCES
	cp -p ./OPATH/trickster ./OPATH/SOURCES/
	cp $(TRICKSTER_MAIN)/conf/trickster.service ./OPATH/SOURCES/
	sed -e 's%^# log_file =.*$$%log_file = "/var/log/trickster/trickster.log"%' \
		-e 's%prometheus:9090%localhost:9090%' \
		< $(TRICKSTER_MAIN)/conf/example.conf > ./OPATH/SOURCES/trickster.conf
	rpmbuild --define "_topdir $(CURDIR)/OPATH" \
		--define "_version $(PROGVER)" \
		--define "_release 1" \
		-ba deploy/packaging/trickster.spec

.PHONY: install
install:
	$(GO) install -o $(TRICKSTER) $(PROGVER)

.PHONY: release
release: build release-artifacts docker docker-release

.PHONY: release-artifacts
release-artifacts:
	GOOS=darwin GOARCH=amd64 $(GO) build -o ./OPATH/trickster-$(PROGVER).darwin-amd64 -a -v $(TRICKSTER_MAIN)/main.go && tar cvfz ./OPATH/trickster-$(PROGVER).darwin-amd64.tar.gz ./OPATH/trickster-$(PROGVER).darwin-amd64
	GOOS=linux  GOARCH=amd64 $(GO) build -o ./OPATH/trickster-$(PROGVER).linux-amd64  -a -v $(TRICKSTER_MAIN)/main.go && tar cvfz ./OPATH/trickster-$(PROGVER).linux-amd64.tar.gz ./OPATH/trickster-$(PROGVER).linux-amd64
	GOOS=linux  GOARCH=arm64 $(GO) build -o ./OPATH/trickster-$(PROGVER).linux-arm64  -a -v $(TRICKSTER_MAIN)/main.go && tar cvfz ./OPATH/trickster-$(PROGVER).linux-arm64.tar.gz ./OPATH/trickster-$(PROGVER).linux-arm64

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

.PHONY: docker
docker:
	docker build -f ./deploy/Dockerfile -t trickster:$(PROGVER) .

.PHONY: docker-release
docker-release:
	docker tag trickster:$(PROGVER) tricksterio/trickster:$(PROGVER)
	docker tag tricksterio/trickster:$(PROGVER) tricksterio/trickster:latest

.PHONY: style
style:
	! gofmt -d $$(find . -path ./vendor -prune -o -name '*.go' -print) | grep '^'

.PHONY: test
test:
	@go test -v -coverprofile=.coverprofile ./... 

.PHONY: bench
bench:
	$(GO) test -v -coverprofile=.coverprofile ./... -run=nonthingplease -bench=. | grep -v ' app=trickster '

.PHONY: test-cover
test-cover: test
	$(GO) tool cover -html=.coverprofile

.PHONY: clean
clean:
	rm -rf ./trickster ./OPATH ./vendor
