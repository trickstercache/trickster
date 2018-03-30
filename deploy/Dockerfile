FROM golang:1.9.3 as builder

COPY . /go/src/github.com/comcast/trickster
WORKDIR /go/src/github.com/comcast/trickster

RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh
RUN dep ensure 
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -a -installsuffix cgo -o trickster -v github.com/comcast/trickster


FROM alpine:3.7
LABEL maintainer "The Trickster Authors <trickster-developers@googlegroups.com>"

RUN addgroup trickgrp && adduser -D -g '' trickusr && adduser trickusr trickgrp

COPY --from=builder /go/src/github.com/comcast/trickster/trickster /usr/local/bin/trickster
COPY conf/example.conf /etc/trickster/trickster.conf
RUN chown trickusr /usr/local/bin/trickster
RUN chmod +x /usr/local/bin/trickster

RUN apk update && apk add ca-certificates && rm -rf /var/cache/apk/*

EXPOSE 9090 8082
USER trickusr
ENTRYPOINT ["trickster"]
