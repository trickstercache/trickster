TRICKSTER_BASE_ENDPOINT ?= http://localhost:8480/prom1
PROMQL_QUERY ?= rate(process_cpu_seconds_total[5m])

# standard test
VIRTUAL_USERS ?= 500
BATCH_SIZE ?= 500
ITERATIONS ?= 50000

# stress test
VIRTUAL_USERS_STRESS ?= 5000
BATCH_SIZE_STRESS ?= 2500
ITERATIONS_STRESS ?= 500000 # 500k iterations

stress-%:
	VIRTUAL_USERS=$(VIRTUAL_USERS_STRESS) \
	BATCH_SIZE=$(BATCH_SIZE_STRESS) \
	ITERATIONS=$(ITERATIONS_STRESS) \
	$(MAKE) $*

# dashboard.js settings
RANGE_SECONDS ?= 21600 # 6 hours
STEP_SECONDS ?= 15
REFRESH_INTERVAL ?= 5
PANEL_QUERIES ?=

K6_ENV = \
	--env TRICKSTER_BASE_ENDPOINT="$(TRICKSTER_BASE_ENDPOINT)" \
	--env PROMQL_QUERY="$(PROMQL_QUERY)" \
	--env RANGE_SECONDS="$(RANGE_SECONDS)" \
	--env STEP_SECONDS="$(STEP_SECONDS)" \
	--env REFRESH_INTERVAL="$(REFRESH_INTERVAL)"

ifneq ($(PANEL_QUERIES),)
K6_ENV += --env PANEL_QUERIES='$(PANEL_QUERIES)'
endif

run:
	for file in $$(find . -name '*.js'); do \
	  echo "Running load test: $$file"; \
	  k6 run \
	    --vus $(VIRTUAL_USERS) \
	    --batch $(BATCH_SIZE) \
	    --iterations $(ITERATIONS) \
	    $(K6_ENV) $$file; \
	done

query-range:
	k6 run \
	  --vus $(VIRTUAL_USERS) \
	  --batch $(BATCH_SIZE) \
	  --iterations $(ITERATIONS) \
	  $(K6_ENV) query-range.js

instant-query:
	k6 run \
	  --vus $(VIRTUAL_USERS) \
	  --batch $(BATCH_SIZE) \
	  --iterations $(ITERATIONS) \
	  $(K6_ENV) instant-query.js
