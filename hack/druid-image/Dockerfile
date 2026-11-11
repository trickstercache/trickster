# Copyright 2018 The Trickster Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# apache/druid is published for linux/amd64 only. This builds the same release
# tarball on a multi-arch Java 17 runtime, so arm64 hosts run Druid natively
# with the layout the official image uses: /opt/druid, owned by druid (1000).

FROM eclipse-temurin:17-jre-noble

ARG DRUID_VERSION=37.0.0
ARG DRUID_SHA512=ba2f23755676a057df93e6ceb5cc9d158d1b0c8f693a9b189d7048c57aea161773b039076bfeb5d2eb1bc1cd318d8f80a5030bcb6ab7deaf30ba8b019cfb57be

RUN set -eux; \
    userdel -r ubuntu 2>/dev/null || true; \
    groupadd -g 1000 druid; \
    useradd -u 1000 -g druid -d /opt/druid -s /bin/sh -M druid; \
    tarball="apache-druid-${DRUID_VERSION}-bin.tar.gz"; \
    wget -q -O "/tmp/${tarball}" \
      "https://archive.apache.org/dist/druid/${DRUID_VERSION}/${tarball}"; \
    echo "${DRUID_SHA512}  /tmp/${tarball}" | sha512sum -c -; \
    tar -xzf "/tmp/${tarball}" -C /opt; \
    rm "/tmp/${tarball}"; \
    mv "/opt/apache-druid-${DRUID_VERSION}" /opt/druid; \
    mkdir -p /opt/druid/var /opt/shared; \
    chown -R druid:druid /opt/druid /opt/shared; \
    chmod 775 /opt/druid/var /opt/shared

USER druid
WORKDIR /opt/druid
VOLUME ["/opt/druid/var"]
