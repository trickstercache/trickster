# trickster

[Trickster](https://github.com/Comcast/trickster) is a reverse proxy cache for the Prometheus HTTP APIv1 that dramatically accelerates dashboard rendering times for any series queried from Prometheus.


## Introduction

This chart bootstraps a [Trickster](https://github.com/Comcast/trickster) deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

## Configuration

The following tables lists the configurable parameters of the prometheus chart and their default values.

Parameter | Description | Default
--- | --- | ---
`affinity` | Node/Pod affinities | `{}`
`image.repository` | Image | `hub.docker.com/tricksterio/trickster`
`image.tag` | Image tag | `1.0.1`
`image.pullPolicy` | Image pull policy | `IfNotPresent`
`ingress.enabled` | If true, Trickster Ingress will be created | `false`
`ingress.annotations` | Annotations for Trickster Ingress` | `{}`
`ingress.fqdn` | Trickster Ingress fully-qualified domain name | `""`
`ingress.tls` | TLS configuration for Trickster Ingress | `[]`
`nodeSelector` | Node labels for pod assignment | `{}`
`originURL` | Default trickster originURL, references a source Prometheus instance | `http://prometheus:9090`
`replicaCount` | Number of trickster replicas desired | `1`
`resources` | Pod resource requests & limits | `{}`
`service.annotations` | Annotations to be added to the Trickster Service | `{}`
`service.clusterIP` | Cluster-internal IP address for Trickster Service | `""`
`service.externalIPs` | List of external IP addresses at which the Trickster Service will be available | `[]`
`service.loadBalancerIP` | External IP address to assign to Trickster Service | `""`
`service.loadBalancerSourceRanges` | List of client IPs allowed to access Trickster Service | `[]`
`service.metricsPort` | Port used for exporting Trickster metrics | `8080`
`service.nodePort` | Port to expose Trickster Service on each node | ``
`service.metricsNodePort` | Port to expose Trickster Service metrics on each node | ``
`service.port` | Trickster's Service port | `9090`
`service.type` | Trickster Service type | `ClusterIP`
`tolerations` | Tolerations for pod assignment | `[]`