
bootstrap-trickster-dev:
	kubectl config use-context minikube
	kubectl create -f configmap.yaml
	kubectl create -f deployment.yaml
	kubectl create -f service.yaml

start-minikube:
	minikube start
	
delete:
	kubectl config use-context minikube
	kubectl delete deployment 

.PHONY: bootstrap-trickster-dev start-minikube delete