# Deployment

## Docker
---
```
$ docker run --name trickster -d [-v /path/to/trickster.yaml:/etc/trickster/trickster.yaml] -p 0.0.0.0:9090:9090 trickstercache/trickster:latest
```

## Kubernetes, Helm, RBAC
---
If you are wanting to use Helm and kubernetes rbac use the following install steps in the `deploy/helm` directory.

#### Bootstrap Local Kubernetes-Helm Dev

- Install [Helm](helm.sh) **Client Version 2.9.1**
    ```
    brew install kubernetes-helm
    ```

- Install [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/) **client server 1.13.4, client version 1.13.4**
    ```
    curl -LO https://storage.googleapis.com/kubernetes-release/release/v1.13.4/bin/darwin/amd64/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin/
    ```

- Install [minikube](https://kubernetes.io/docs/getting-started-guides/minikube/) **version 0.35.0**
    ```
    curl -Lo minikube https://storage.googleapis.com/minikube/releases/v0.23.2/minikube-darwin-amd64 && chmod +x minikube && sudo mv minikube /usr/local/bin/
    ```

- Start minikube and enable RBAC `make start-minikube` or manually with `--extra-config=apiserver.Authorization.Mode=RBAC --kubernetes-version=v1.8.0`.
- Install Tiller `make bootstrap-peripherals`
- Wait until Tiller is running `kubectl get po --namespace trickster -w`
- Deploy all K8 artifacts `make bootstrap-trickster-dev`

#### Deployment

- Make any necessary configuration changes to `deploy/helm/values.yaml` or `deploy/helm/template/configmap.yaml`
- Set your kubectl context to your target cluster `kubectl config use-context <context>`
- Make sure Tiller is running `kubectl get po --namespace trickster -w`
- Run deployment script `./deploy` from within `deploy/helm`

## Kubernetes
---
For pure kubernetes deployment use the `deploy/kube` directory.

#### Bootstrap Local Kubernetes Dev

- Install [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/) **client server 1.8.0, client version 1.8.0**
    ```
    brew install https://raw.githubusercontent.com/Homebrew/homebrew-core/e4b03ca8689987364852d645207be16a1ec1b349/Formula/kubernetes-cli.rb
    brew pin kubernetes-cli
    ```

- Install [minikube](https://kubernetes.io/docs/getting-started-guides/minikube/) **version 0.25.0**
    ```
    brew cask install https://raw.githubusercontent.com/caskroom/homebrew-cask/903f1507e1aeea7fc826c6520a8403b4076ed6f4/Casks/minikube.rb
    ```

- Start minikube `make start-minikube` or manually with `minikube start`.
- Deploy all K8 artifacts `make bootstrap-trickster-dev`

#### Deployment

- Make any necessary configuration changes to `deploy/kube/configmap.yaml`
- Set your kubectl context to your target cluster `kubectl config use-context <context>`
- Run deployment script `./deploy` from within `deploy/kube`

## Local Binary
---
#### Binary Dev

- Use parent directory and run make, then `./trickster [-config <path>]`
