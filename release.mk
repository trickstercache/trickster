# Copyright 2018 The Trickster Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# -----------------------------------------------------------------------------
# Targets for building and releasing Trickster to be triggered by Trickster maintainers.
#
# Create a new tag and push it to the remote repository
#
# Note(s): 
# The CI/CD workflow is responsible for creating a release for the tag.
# Recommend to run from the master branch of the upstream trickster repository.

TAG_VERSION ?= 
.PHONY: create-tag
create-tag:
	@if [ -z "$(TAG_VERSION)" ]; then echo "TAG_VERSION is not set"; exit 1; fi
	@echo "FYI: the last proper tag was: $(shell $(MAKE) last-proper)"
	@echo "FYI: the last beta tag was: $(shell $(MAKE) last-beta)"
	@if ! echo $(TAG_VERSION) | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+(-beta[0-9]+)?$$'; then \
		echo "TAG_VERSION must be a semver (e.g. v1.2.3 or v1.2.3-beta1)"; \
		exit 1; \
	fi
	@if $(MAKE) get-tags | grep -qE "^$(TAG_VERSION)$$"; then \
		echo "Tag $(TAG_VERSION) already exists"; \
		exit 1; \
	fi
	@read -p "Create tag $(TAG_VERSION)? [y/N] " yn; \
	if [ "$$yn" != "y" ]; then \
		echo "Aborting"; \
		exit 1; \
	fi
	git tag $(TAG_VERSION)
	git push origin $(TAG_VERSION)

# List out all tags in the repository
.PHONY: get-tags
get-tags:
	@git tag -l | sort

# List out all tags in the repository matching the given pattern
# Example: make get-beta
.PHONY: get-%
get-%:
# special case: return all tags
	@if [[ "$*" == "all" ]]; then $(MAKE) get-tags; exit 0;	fi
# special case: return all "proper" (non-beta/rc) tags
	@if [[ "$*" == "proper" ]]; then $(MAKE) get-tags | grep -vE "beta|rc" || true; exit 0; fi
# else, beta, rc, etc.
	@$(MAKE) get-tags | grep "$*" || true

# Get the last tag matching the given pattern (e.g. last beta)
last-%:
	@export LAST="$(shell $(MAKE) get-$* | tail -n 1)"; \
		if [ -z "$$LAST" ]; then \
			echo "No tags found for $*"; \
			exit 1; \
		fi; \
		echo $$LAST
