GO             ?= go

# Start/Stop the docker-compose environment for integration testing
developer-start:
	@$(MAKE) -C .. developer-start

developer-stop:
	@$(MAKE) -C .. developer-stop

developer-delete:
	@$(MAKE) -C .. developer-delete

# integration-start uses the integration compose profile, which adds the
# integration-only containers (coredns) to every TSDB backend
integration-start:
	@$(MAKE) -C .. integration-start

integration-stop:
	@$(MAKE) -C .. integration-stop

# quic-go warns loudly when the OS UDP receive buffer cannot be raised, which
# is the norm in CI containers and unactionable there
export QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING = 1

# the -race run takes several minutes and grows with the suite, so go test's
# 10m default would end it mid-run on a slow runner
TEST_TIMEOUT ?= 30m

test:
	$(GO) test -timeout $(TEST_TIMEOUT) -v --failfast
	@../hack/filter-coverprofile.sh .integration.coverprofile

.PHONY: test-no-failfast
test-no-failfast:
	@status=0; \
	$(GO) test -timeout $(TEST_TIMEOUT) -v || status=1; \
	../hack/filter-coverprofile.sh .integration.coverprofile || status=1; \
	exit $$status

data-race-test:
	$(GO) test -timeout $(TEST_TIMEOUT) -v --failfast --race

.PHONY: data-race-test-no-failfast
data-race-test-no-failfast:
	$(GO) test -timeout $(TEST_TIMEOUT) -v --race

cover:
	$(GO) test -timeout $(TEST_TIMEOUT) -v --failfast -coverpkg=../... -coverprofile=.integration.coverprofile
	@../hack/filter-coverprofile.sh .integration.coverprofile
	$(GO) tool cover -func=.integration.coverprofile | tail -1

.PHONY: cover-race
cover-race:
	$(GO) test -timeout $(TEST_TIMEOUT) -v --failfast -race -covermode=atomic -coverpkg=../... -coverprofile=.integration.coverprofile
	@../hack/filter-coverprofile.sh .integration.coverprofile
	$(GO) tool cover -func=.integration.coverprofile | tail -1

cover-html: cover
	$(GO) tool cover -html=.integration.coverprofile
