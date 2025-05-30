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

.PHONY: release
release: clean go-mod-tidy go-mod-vendor release-artifacts release-sha256

# generate sha256sum for all release artifacts
RELEASE_CHECKSUM_FILE=$(BUILD_SUBDIR)/sha256sum.txt
.PHONY: release-sha256
release-sha256:
	./hack/release-sha256.sh $(RELEASE_CHECKSUM_FILE) $(BUILD_SUBDIR) $(TAGVER) $(BIN_DIR)

.PHONY: release-artifacts
release-artifacts: clean

	mkdir -p $(PACKAGE_DIR)
	mkdir -p $(BIN_DIR)
	mkdir -p $(CONF_DIR)

	cp -r ./docs $(PACKAGE_DIR)
	cp -r ./deploy $(PACKAGE_DIR)
	cp ./README.md $(PACKAGE_DIR)
	cp ./CONTRIBUTING.md $(PACKAGE_DIR)
	cp ./LICENSE $(PACKAGE_DIR)
	cp ./examples/conf/*.yaml $(CONF_DIR)

	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) -o $(BIN_DIR)/trickster-$(TAGVER).darwin-amd64  -v $(TRICKSTER_MAIN)/*.go
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) -o $(BIN_DIR)/trickster-$(TAGVER).darwin-arm64  -v $(TRICKSTER_MAIN)/*.go
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) -o $(BIN_DIR)/trickster-$(TAGVER).linux-amd64   -v $(TRICKSTER_MAIN)/*.go
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) -o $(BIN_DIR)/trickster-$(TAGVER).linux-arm64   -v $(TRICKSTER_MAIN)/*.go
	GOOS=windows GOARCH=amd64 CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(LDFLAGS) -o $(BIN_DIR)/trickster-$(TAGVER).windows-amd64 -v $(TRICKSTER_MAIN)/*.go

	cd ./$(BUILD_SUBDIR) && tar cvfz ./trickster-$(TAGVER).tar.gz ./trickster-$(TAGVER)/*
