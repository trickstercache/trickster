GO             ?= go

# Start/Stop the docker-compose environment for integration testing
developer-start:
	@$(MAKE) -C .. developer-start

developer-stop:
	@$(MAKE) -C .. developer-stop

developer-delete:
	@$(MAKE) -C .. developer-delete

test:
	$(GO) test -v --failfast

data-race-test:
	$(GO) test -v --failfast --race

cover:
	$(GO) test -v --failfast -coverpkg=../... -coverprofile=.integration.coverprofile
	$(GO) tool cover -func=.integration.coverprofile | tail -1

cover-html: cover
	$(GO) tool cover -html=.integration.coverprofile