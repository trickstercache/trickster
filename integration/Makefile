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
