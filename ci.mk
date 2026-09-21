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
# Targets for building and releasing Trickster from a CI/CD pipeline
# Not meant for local usage except for testing

# one archive is published per platform, each holding a single binary
RELEASE_PLATFORMS ?= darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64
RELEASE_PLATFORM  ?= $(shell $(GO) env GOOS)-$(shell $(GO) env GOARCH)
RELEASE_OS         = $(word 1,$(subst -, ,$(RELEASE_PLATFORM)))
RELEASE_ARCH       = $(word 2,$(subst -, ,$(RELEASE_PLATFORM)))
RELEASE_EXT        = $(if $(filter windows,$(RELEASE_OS)),.exe,)
PACKAGE_NAME       = trickster-$(TAGVER).$(RELEASE_PLATFORM)
PACKAGE_DIR        = ./$(BUILD_SUBDIR)/$(PACKAGE_NAME)
BIN_DIR            = $(PACKAGE_DIR)/bin
CONF_DIR           = $(PACKAGE_DIR)/conf
HOST_GO_LICENSES   = $(CURDIR)/$(BUILD_SUBDIR)/.tools/go-licenses

.PHONY: release
release: clean go-mod-tidy release-artifacts release-sha256

# generate sha256sum for all release archives
RELEASE_CHECKSUM_FILE=$(BUILD_SUBDIR)/sha256sum.txt
.PHONY: release-sha256
release-sha256:
	./hack/release-sha256.sh $(RELEASE_CHECKSUM_FILE) $(BUILD_SUBDIR) $(TAGVER)

.PHONY: release-artifacts
release-artifacts: $(addprefix release-artifact-,$(RELEASE_PLATFORMS))

# e.g., make release-artifact-linux-arm64
release-artifact-%:
	$(MAKE) release-artifact RELEASE_PLATFORM=$*

# builds ./bin/trickster-<version>.<os>-<arch>.tar.gz for RELEASE_PLATFORM
.PHONY: release-artifact
release-artifact:
	@test -n "$(RELEASE_OS)" -a -n "$(RELEASE_ARCH)" || { echo "RELEASE_PLATFORM must be <os>-<arch>" >&2; exit 1; }
	rm -rf $(PACKAGE_DIR) $(PACKAGE_DIR).tar.gz
	mkdir -p $(BIN_DIR) $(CONF_DIR)

	GOOS= GOARCH= $(GO) build -o $(HOST_GO_LICENSES) github.com/google/go-licenses/v2
	GOOS=$(RELEASE_OS) GOARCH=$(RELEASE_ARCH) $(MAKE) third-party-licenses \
		GO_LICENSES=$(HOST_GO_LICENSES) THIRD_PARTY_LICENSES_DIR=$(PACKAGE_DIR)/third-party-licenses

	# tracked files only, so local developer-environment data never ships
	git archive HEAD docs deploy | tar -x -C $(PACKAGE_DIR)
	cp ./README.md ./CONTRIBUTING.md ./LICENSE ./NOTICE $(PACKAGE_DIR)
	cp ./examples/conf/*.yaml $(CONF_DIR)

	GOOS=$(RELEASE_OS) GOARCH=$(RELEASE_ARCH) CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) \
		-o $(BIN_DIR)/trickster$(RELEASE_EXT) -v $(TRICKSTER_MAIN)/*.go

	tar -C ./$(BUILD_SUBDIR) -czf $(PACKAGE_DIR).tar.gz $(PACKAGE_NAME)
