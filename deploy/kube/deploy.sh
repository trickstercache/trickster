#!/bin/bash
# the plain caching proxy; `make bootstrap-trickster-gateway` deploys the
# Gateway API / Ingress controller

kubectl create -f configmap.yaml
kubectl create -f deployment.yaml
kubectl create -f service.yaml
