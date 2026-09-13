#!/bin/sh
# Renders deploy/kube/configmap.yaml from examples/conf/example.full.yaml so
# the exhaustive Kubernetes ConfigMap never drifts from the example config.
# Usage: hack/gen-kube-configmap.sh [example.full.yaml] [configmap.yaml]
set -eu

src="${1:-examples/conf/example.full.yaml}"
dst="${2:-deploy/kube/configmap.yaml}"

{
	cat <<'HEADER'
# Generated from examples/conf/example.full.yaml by `make kube-configmap`;
# edit the example and regenerate rather than editing this file.
apiVersion: v1
kind: ConfigMap
metadata:
  name: trickster-conf
  labels:
    name: trickster-conf

data:
  trickster-conf: |-
HEADER
	# indent every non-empty line by four spaces; blank lines stay empty
	sed -e 's/^\(.\)/    \1/' "$src"
} > "$dst"
