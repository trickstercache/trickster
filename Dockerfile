# BIN_SOURCE selects where the binary comes from: 'builder' compiles it here,
# 'prebuilt' copies release artifacts unpacked at ./dist/linux-<arch>
ARG BIN_SOURCE=builder

# certificates are architecture-independent, so this never needs emulation
FROM --platform=${BUILDPLATFORM} alpine:latest AS certs
RUN apk update && apk add ca-certificates

FROM --platform=${BUILDPLATFORM} golang:1.27 AS builder
ARG GIT_LATEST_COMMIT_ID
ARG TAGVER

COPY . /go/src/github.com/trickstercache/trickster
WORKDIR /go/src/github.com/trickstercache/trickster

ARG TARGETARCH
RUN GOOS=linux GOARCH=${TARGETARCH} CGO_ENABLED=0 BUILD_FLAGS=-v make build third-party-licenses \
    && mkdir /out && mv bin/trickster bin/third-party-licenses /out/

FROM scratch AS prebuilt
ARG TARGETARCH
COPY dist/linux-${TARGETARCH}/bin/trickster /out/trickster
COPY dist/linux-${TARGETARCH}/third-party-licenses /out/third-party-licenses

FROM ${BIN_SOURCE} AS bin

FROM gcr.io/distroless/static-debian12 AS final
LABEL maintainer="The Trickster Authors <trickster-developers@googlegroups.com>"

COPY --from=certs /etc/ssl /etc/ssl
COPY --from=bin /out/trickster /trickster
COPY --from=bin /out/third-party-licenses /licenses/third-party-licenses
COPY LICENSE NOTICE /licenses/
COPY examples/conf/example.full.yaml /etc/trickster/trickster.yaml
USER nobody
ENTRYPOINT ["/trickster"]
