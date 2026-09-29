GO             ?= go

# Start/Stop the docker-compose environment for integration testing
developer-start:
	@$(MAKE) -C .. developer-start

developer-stop:
	@$(MAKE) -C .. developer-stop

developer-delete:
	@$(MAKE) -C .. developer-delete

# integration-start/stop also toggle the integration-only containers
# (coredns) that stay commented out of the compose file for developers
integration-start:
	@$(MAKE) -C .. integration-start

integration-stop:
	@$(MAKE) -C .. integration-stop

# quic-go warns loudly when the OS UDP receive buffer cannot be raised, which
# is the norm in CI containers and unactionable there
export QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING = 1

test:
	$(GO) test -v --failfast
	$(GO) test -v ./greptimedb
	@../hack/filter-coverprofile.sh .integration.coverprofile

.PHONY: test-no-failfast
test-no-failfast:
	@status=0; \
	$(GO) test -v || status=1; \
	$(GO) test -v ./greptimedb || status=1; \
	../hack/filter-coverprofile.sh .integration.coverprofile || status=1; \
	exit $$status

data-race-test:
	$(GO) test -v --failfast --race
	$(GO) test -v --race ./greptimedb

.PHONY: data-race-test-no-failfast
data-race-test-no-failfast:
	@status=0; \
	$(GO) test -v --race || status=1; \
	$(GO) test -v --race ./greptimedb || status=1; \
	exit $$status

cover:
	$(GO) test -v --failfast -coverpkg=../... -coverprofile=.integration.coverprofile
	$(GO) test -v ./greptimedb
	@../hack/filter-coverprofile.sh .integration.coverprofile
	$(GO) tool cover -func=.integration.coverprofile | tail -1

cover-html: cover
	$(GO) tool cover -html=.integration.coverprofile
