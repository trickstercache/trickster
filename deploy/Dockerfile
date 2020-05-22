ARG IMAGE_ARCH=amd64

FROM golang:1.14.3 as builder
COPY . /go/src/github.com/Comcast/trickster
WORKDIR /go/src/github.com/Comcast/trickster

ARG GOARCH=amd64
RUN GOOS=linux GOARCH=${GOARCH} CGO_ENABLED=0 make build

FROM ${IMAGE_ARCH}/alpine:3.11.6
LABEL maintainer "The Trickster Authors <trickster-developers@googlegroups.com>"

COPY --from=builder /go/src/github.com/Comcast/trickster/OPATH/trickster /usr/local/bin/trickster
COPY cmd/trickster/conf/example.conf /etc/trickster/trickster.conf
RUN chown nobody /usr/local/bin/trickster
RUN chmod +x /usr/local/bin/trickster

RUN apk update && apk add ca-certificates && rm -rf /var/cache/apk/*

USER nobody
ENTRYPOINT ["trickster"]
