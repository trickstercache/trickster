DEFAULT: build

GO           ?= go
GOFMT        ?= $(GO)fmt
FIRST_GOPATH := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))
DEP          := $(FIRST_GOPATH)/bin/dep
TRICKSTER    := $(FIRST_GOPATH)/bin/trickster

PROGVER = $(shell grep 'applicationVersion = ' main.go | awk '{print $$3}' | sed -e 's/\"//g')

.PHONY: $(DEP)
$(DEP):
	GOOS= GOARCH= $(GO) get -u github.com/golang/dep/cmd/dep

.PHONY: deps
deps: $(DEP)
	$(DEP) ensure

.PHONY: build
build: deps
	go build

rpm: build
	mkdir -p ./OPATH/SOURCES
	cp -p trickster ./OPATH/SOURCES/
	cp conf/trickster.service ./OPATH/SOURCES/
	sed -e 's%^# log_file =.*$$%log_file = "/var/log/trickster/trickster.log"%' \
		-e 's%prometheus:9090%localhost:9090%' \
		< conf/example.conf > ./OPATH/SOURCES/trickster.conf
	rpmbuild --define "_topdir $(CURDIR)/OPATH" \
		--define "_version $(PROGVER)" \
		--define "_release 1" \
		-ba trickster.spec

.PHONY: install
install: deps
	echo go build -o $(TRICKSTER) $(PROGVER)

.PHONY: release
release: build release-artifacts docker docker-release

.PHONY: release-artifacts
release-artifacts:
	GOOS=darwin GOARCH=amd64 go build -o ./OPATH/trickster-$(PROGVER).darwin-amd64 && gzip -f ./OPATH/trickster-$(PROGVER).darwin-amd64
	GOOS=linux  GOARCH=amd64 go build -o ./OPATH/trickster-$(PROGVER).linux-amd64  && gzip -f ./OPATH/trickster-$(PROGVER).linux-amd64

.PHONY: helm-local
helm-local:
	kubectl config use-context minikube --namespace=trickster
	kubectl scale --replicas=0 deployment/dev-trickster -n trickster
	eval $$(minikube docker-env) \
		&& docker build -f deploy/Dockerfile -t trickster:dev .
	kubectl set image deployment/dev-trickster trickster=trickster:dev -n trickster
	kubectl scale --replicas=1 deployment/dev-trickster -n trickster

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
test: deps
	go test -o $(TRICKSTER) -v ./...

.PHONY: test-cover
test-cover: deps
	go test -o $(TRICKSTER) -coverprofile=cover.out ./...
	go tool cover -html=cover.out

.PHONY: clean
clean:
	rm -rf ./trickster ./OPATH ./vendor
