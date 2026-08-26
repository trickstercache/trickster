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

test:
	$(GO) test -v --failfast
	@../hack/filter-coverprofile.sh .integration.coverprofile

data-race-test:
	$(GO) test -v --failfast --race

cover:
	$(GO) test -v --failfast -coverpkg=../... -coverprofile=.integration.coverprofile
	@../hack/filter-coverprofile.sh .integration.coverprofile
	$(GO) tool cover -func=.integration.coverprofile | tail -1

cover-html: cover
	$(GO) tool cover -html=.integration.coverprofile