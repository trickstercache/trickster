
bootstrap-trickster-dev:
	kubectl config use-context minikube
	kubectl create -f configmap.yaml
	kubectl create -f deployment.yaml
	kubectl create -f service.yaml

bootstrap-graphite-demo:
	kubectl config use-context minikube
	kubectl create -f graphite-configmap.yaml
	kubectl patch deployment trickster --type strategic --patch-file graphite-deployment-patch.yaml

start-minikube:
	minikube start
	
delete:
	kubectl config use-context minikube
	kubectl delete deployment 

.PHONY: bootstrap-trickster-dev bootstrap-graphite-demo start-minikube delete