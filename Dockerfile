FROM alpine:latest as certs
RUN apk update && apk add ca-certificates

ARG BUILDPLATFORM=linux/amd64
FROM --platform=${BUILDPLATFORM} golang:1.26 as builder
ARG GIT_LATEST_COMMIT_ID
ARG TAGVER

COPY . /go/src/github.com/trickstercache/trickster
WORKDIR /go/src/github.com/trickstercache/trickster

ARG TARGETARCH
RUN GOOS=linux GOARCH=${TARGETARCH} CGO_ENABLED=0 BUILD_FLAGS=-v make build

FROM gcr.io/distroless/static-debian12 as final
LABEL maintainer "The Trickster Authors <trickster-developers@googlegroups.com>"

COPY --from=certs /etc/ssl /etc/ssl
COPY --from=builder /go/src/github.com/trickstercache/trickster/bin/trickster /trickster
COPY examples/conf/example.full.yaml /etc/trickster/trickster.yaml
USER nobody
ENTRYPOINT ["/trickster"]
