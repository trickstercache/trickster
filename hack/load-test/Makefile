TRICKSTER_BASE_ENDPOINT ?= http://localhost:8480/prom1
PROMQL_QUERY ?= rate(process_cpu_seconds_total[5m])

SIZE ?= M
include sizes.env

VIRTUAL_USERS = $(VIRTUAL_USERS_$(SIZE))
BATCH_SIZE = $(BATCH_SIZE_$(SIZE))
ITERATIONS = $(ITERATIONS_$(SIZE))

# dashboard.js settings
RANGE_SECONDS ?= 21600 # 6 hours
STEP_SECONDS ?= 15
REFRESH_INTERVAL ?= 5
PANEL_QUERIES ?=

# Rate mode: set RATE to target iterations/sec (e.g., make query-range RATE=100)
RATE ?=
DURATION ?= 2m
MAX_VUS ?= 500

K6_ENV = \
	--env TRICKSTER_BASE_ENDPOINT="$(TRICKSTER_BASE_ENDPOINT)" \
	--env PROMQL_QUERY="$(PROMQL_QUERY)" \
	--env RANGE_SECONDS="$(RANGE_SECONDS)" \
	--env STEP_SECONDS="$(STEP_SECONDS)" \
	--env REFRESH_INTERVAL="$(REFRESH_INTERVAL)"

ifneq ($(PANEL_QUERIES),)
K6_ENV += --env PANEL_QUERIES='$(PANEL_QUERIES)'
endif

# In rate mode, the script defines a constant-arrival-rate scenario;
# --vus/--iterations are omitted so they don't override it.
ifneq ($(RATE),)
K6_ENV += --env K6_RATE="$(RATE)" --env K6_DURATION="$(DURATION)" --env K6_MAX_VUS="$(MAX_VUS)"
K6_EXEC_FLAGS = --batch $(BATCH_SIZE)
else
K6_EXEC_FLAGS = --vus $(VIRTUAL_USERS) --batch $(BATCH_SIZE) --iterations $(ITERATIONS)
endif

run:
	@for file in $$(find . -name '*.js'); do \
	  $(MAKE) $$(basename $$file .js); \
	done

# examples: `make query-range`, `make query-range RATE=100`
%:
	@echo "Running load test: $@.js"
	k6 run $(K6_EXEC_FLAGS) $(K6_ENV) $@.js
