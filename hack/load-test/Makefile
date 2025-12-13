
TRICKSTER_BASE_ENDPOINT ?= http://localhost:8480/prom1

VIRTUAL_USERS ?= 50
BATCH_SIZE ?= 100
ITERATIONS ?= 5000

run:
	for file in $$(find . -name '*.js'); do \
	  echo "Running load test: $$file"; \
	  k6 run \
	    --vus $(VIRTUAL_USERS) \
		--batch $(BATCH_SIZE) \
		--iterations $(ITERATIONS) \
	    --env TRICKSTER_BASE_ENDPOINT="$(TRICKSTER_BASE_ENDPOINT)" $$file; \
	done


