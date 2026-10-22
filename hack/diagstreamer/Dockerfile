# Runs hack/diagstreamer from the repository mounted at /repo: the Go image plus ffmpeg, with nothing of the tool
# built into the image. From the repository root:
#
#   docker build -t trickster-diagstreamer hack/diagstreamer
#   docker run --rm -p 8496:8496 -v "$PWD":/repo:ro trickster-diagstreamer
#
# Then open http://localhost:8496/. DIAG_* environment variables (-e) or flags after the image name customize the
# stream; hack/diagstreamer/README.md lists them.
FROM golang:1.27-alpine
RUN apk add --no-cache ffmpeg
WORKDIR /repo/hack/diagstreamer
EXPOSE 8496
ENTRYPOINT ["go", "run", "./cmd/diagstreamer"]
