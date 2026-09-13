
# the plain caching proxy: the exhaustive example configuration as a ConfigMap
bootstrap-trickster-dev:
	kubectl config use-context minikube
	kubectl create -f configmap.yaml
	kubectl create -f deployment.yaml
	kubectl create -f service.yaml

bootstrap-graphite-demo:
	kubectl config use-context minikube
	kubectl create -f graphite-configmap.yaml
	kubectl patch deployment trickster --type strategic --patch-file graphite-deployment-patch.yaml

# the Gateway API / Ingress controller; GATEWAY_API_VERSION selects the
# upstream CRD release and GATEWAY_API_CHANNEL standard or experimental
GATEWAY_API_VERSION ?= v1.6.1
GATEWAY_API_CHANNEL ?= standard
GATEWAY_API_CRDS = https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/$(GATEWAY_API_CHANNEL)-install.yaml

install-gateway-crds:
	kubectl apply --server-side -f $(GATEWAY_API_CRDS)
	kubectl apply -f crds/trickstercachepolicies.yaml
	kubectl wait --for=condition=Established crd --all --timeout=60s

bootstrap-trickster-gateway:
	kubectl apply -f gateway-namespace.yaml
	kubectl apply -f gateway-rbac.yaml
	kubectl apply -f gateway-classes.yaml
	kubectl apply -f gateway-configmap.yaml
	kubectl apply -f gateway-deployment.yaml
	kubectl apply -f gateway-service.yaml
	kubectl apply -f gateway-pdb.yaml
	kubectl -n trickster rollout status deployment/trickster-gateway --timeout=180s

delete-trickster-gateway:
	kubectl delete -f gateway-pdb.yaml -f gateway-hpa.yaml --ignore-not-found
	kubectl delete -f gateway-service.yaml -f gateway-deployment.yaml -f gateway-configmap.yaml
	kubectl delete -f gateway-classes.yaml -f gateway-rbac.yaml --ignore-not-found

start-minikube:
	minikube start

delete:
	kubectl config use-context minikube
	kubectl delete deployment trickster

.PHONY: bootstrap-trickster-dev bootstrap-graphite-demo install-gateway-crds \
	bootstrap-trickster-gateway delete-trickster-gateway start-minikube delete
