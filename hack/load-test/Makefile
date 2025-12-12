
TRICKSTER_BASE_ENDPOINT ?= http://localhost:8480/prom1

run:
	for file in $$(find . -name '*.js'); do \
	  echo "Running load test: $$file"; \
	  k6 run --env TRICKSTER_BASE_ENDPOINT="$(TRICKSTER_BASE_ENDPOINT)" $$file; \
	done


