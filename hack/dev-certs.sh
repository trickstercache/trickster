#!/usr/bin/env bash
# Generates a self-signed certificate for local HTTP/3 development. HTTP/3 is
# TLS-only, so the developer environment needs a certificate before the QUIC
# listener will start.
#
# Usage: hack/dev-certs.sh [output-dir]   (default: docs/developer/environment/certs)
set -euo pipefail

OUT_DIR="${1:-docs/developer/environment/certs}"
mkdir -p "$OUT_DIR"
CERT="$OUT_DIR/trickster-dev.crt"
KEY="$OUT_DIR/trickster-dev.key"

if [[ -f "$CERT" && -f "$KEY" ]]; then
  echo "certificate already present at $CERT (delete it to regenerate)"
  exit 0
fi

openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
  -keyout "$KEY" -out "$CERT" \
  -subj "/O=Trickster Development/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1"

chmod 600 "$KEY"
echo "wrote $CERT and $KEY"
echo
echo "Point a backend's tls block at these paths, then exercise HTTP/3 with:"
echo "  go run ./hack/h3-client -url https://127.0.0.1:8443/ -ca $CERT"
