
KUBE_DIR ?= kube-artifacts

install-tiller-dev: 
	kubectl config use-context minikube
	-kubectl create ns trickster
	kubectl config set-context minikube --namespace=trickster
	-kubectl --namespace trickster create -f tiller/templates/serviceaccount.yaml
	-kubectl --namespace trickster create -f tiller/templates/role.yaml
	-helm init --service-account tiller --tiller-image gcr.io/kubernetes-helm/tiller:v2.8.2 --tiller-namespace trickster
	
bootstrap-peripherals: install-tiller-dev
	kubectl config use-context minikube
	-kubectl create -f $(KUBE_DIR)/compute-quota.yaml -n trickster
	-kubectl create -f $(KUBE_DIR)/minikube.yaml

bootstrap-trickster-dev:
	helm upgrade dev trickster \
		--install \
		--namespace=trickster \
		--tiller-namespace=trickster
	
update-dev-chart: update-trickster
	kubectl config use-context minikube
	helm upgrade dev trickster --namespace=trickster --tiller-namespace=trickster --reuse-values

start-minikube:
	minikube start \
		--memory 2048 \
		--cpus 2 \
		--extra-config=apiserver.Authorization.Mode=RBAC \
		--kubernetes-version=v1.9.4
	-kubectl config set-context minikube --namespace=trickster
	
delete:
	kubectl config use-context minikube
	helm delete --purge dev --tiller-namespace=trickster

.PHONY: install-tiller-dev bootstrap-peripherals bootstrap-trickster-dev update-dev-chart start-minikube delete