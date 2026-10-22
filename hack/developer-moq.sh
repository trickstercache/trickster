#!/usr/bin/env bash
# Runs the MoQ harness: moq-dev's relay as the origin, diagstreamer publishing into it, and Trickster in the
# foreground with docs/developer/environment/trickster-config/moq.yaml. Run from the root of the repo.
#
# Usage: hack/developer-moq.sh up        start the relay and the publisher, then run Trickster
#        hack/developer-moq.sh browser   open the player in a Chrome profile that trusts the dev certificate
set -euo pipefail

ENV_DIR=docs/developer/environment
CONFIG=$ENV_DIR/trickster-config/moq.yaml
CERT=$ENV_DIR/certs/trickster-dev.crt
PLAYER_ORIGIN=localhost:8583
PLAYER_URL=https://$PLAYER_ORIGIN/player/
RELAY_PORT=10443
CHROME_PROFILE="${TMPDIR:-/tmp}/trickster-moq-chrome"

up() {
  # the integration stack runs its own moq-relay on the same host port; the harness's own relay may already be up
  local own holder
  own=$(cd "$ENV_DIR" && COMPOSE_PROFILES=moq docker compose ps -a --format '{{.Name}}' moq_relay 2>/dev/null || true)
  holder=$(docker ps --filter "publish=$RELAY_PORT/udp" --format '{{.Names}}' | grep -vxF "${own:-}" || true)
  if [[ -n "$holder" ]]; then
    echo "UDP $RELAY_PORT is published by $holder; stop it first, for example:" >&2
    echo "  docker stop $holder" >&2
    exit 1
  fi
  (cd "$ENV_DIR" && COMPOSE_PROFILES=moq docker compose up -d --build moq_relay diagstreamer_moq)
  echo
  echo "diagstreamer.hang is published into moq-relay on UDP $RELAY_PORT; its first start compiles, so allow a minute."
  echo "Player: $PLAYER_URL   (make developer-moq-browser opens it)"
  echo "Stop Trickster with Ctrl-C, and the containers with make developer-stop."
  echo
  exec go run ./cmd/trickster -config "$CONFIG" "$@"
}

chrome() {
  local c
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    "/Applications/Chromium.app/Contents/MacOS/Chromium" google-chrome google-chrome-stable chromium chromium-browser; do
    if [[ -x "$c" ]] || command -v "$c" >/dev/null 2>&1; then
      echo "$c"
      return
    fi
  done
  echo "Chrome or Chromium is not installed" >&2
  exit 1
}

browser() {
  # WebTransport refuses an untrusted certificate outright, so a throwaway profile trusts the dev certificate's key;
  # Chrome applies that to QUIC only for an origin it is told to reach over QUIC
  local spki bin
  spki=$(openssl x509 -in "$CERT" -pubkey -noout | openssl pkey -pubin -outform der |
    openssl dgst -sha256 -binary | base64)
  bin=$(chrome)
  "$bin" --user-data-dir="$CHROME_PROFILE" --ignore-certificate-errors-spki-list="$spki" \
    --origin-to-force-quic-on="$PLAYER_ORIGIN" --no-first-run --no-default-browser-check "$PLAYER_URL" >/dev/null 2>&1 &
  echo "opened $PLAYER_URL in a Chrome profile at $CHROME_PROFILE"
}

cmd="${1:-}"
shift || true
case "$cmd" in
up) up "$@" ;;
browser) browser ;;
*)
  echo "usage: $0 up|browser" >&2
  exit 2
  ;;
esac
