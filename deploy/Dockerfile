ARG IMAGE_ARCH=amd64

FROM alpine:latest as builder
RUN apk add --no-cache bash gcc musl-dev openssl go make;
RUN export \
    GOROOT_BOOTSTRAP="$(go env GOROOT)" \
    GOOS="$(go env GOOS)" \
    GOARCH="$(go env GOARCH)" \
    GOHOSTOS="$(go env GOHOSTOS)" \
    GOHOSTARCH="$(go env GOHOSTARCH)";
RUN wget -O go.src.tar.gz https://go.dev/dl/go1.16.15.src.tar.gz; \
    echo '90a08c689279e35f3865ba510998c33a63255c36089b3ec206c912fc0568c3d3 *go.src.tar.gz' | sha256sum -c -; \
    tar -C /usr/local -xzf go.src.tar.gz; \
    rm go.src.tar.gz; \
    cd /usr/local/go/src; \
    ./make.bash; 
COPY . /go/src/github.com/trickstercache/trickster
WORKDIR /go/src/github.com/trickstercache/trickster

ARG GOARCH=amd64
RUN GOOS=linux GOARCH=${GOARCH} CGO_ENABLED=0 make build

FROM ${IMAGE_ARCH}/alpine:latest
LABEL maintainer "The Trickster Authors <trickster-developers@googlegroups.com>"

COPY --from=builder /go/src/github.com/trickstercache/trickster/OPATH/trickster /usr/local/bin/trickster
COPY cmd/trickster/conf/example.conf /etc/trickster/trickster.conf
RUN chown nobody /usr/local/bin/trickster
RUN chmod +x /usr/local/bin/trickster

RUN apk update && apk add ca-certificates && rm -rf /var/cache/apk/*

USER nobody
ENTRYPOINT ["trickster"]
