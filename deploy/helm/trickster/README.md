# trickster

[Trickster](https://github.com/Comcast/trickster) is a reverse proxy cache for the Prometheus HTTP APIv1 that dramatically accelerates dashboard rendering times for any series queried from Prometheus.

## Introduction

This chart bootstraps a [Trickster](https://github.com/Comcast/trickster) deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

## Configuration

The following table lists the configurable parameters of the trickster chart and their default values.

Parameter | Description | Default
--- | --- | ---
`config.originURL` | Default trickster originURL, references a source Prometheus instance | `http://prometheus:9090`
`config.cache.type` | The cache_type to use.  {boltdb, filesystem, memory, redis} | `memory`
`config.cache.redis.protocol` | The protocol for connecting to redis ('unix' or 'tcp') | `tcp`
`config.cache.redis.endpoint` | The fqdn+port or path to a unix socket file for connecting to redis | `redis:6379`
`config.cache.filesystem.path` | The directory location under which the Trickster filesystem cache will be maintained | `/tmp/trickster`
`config.cache.boltdb.file` | The filename of the BoltDB database | `db`
`config.cache.boltdb.bucket` | The name of the BoltDB bucket | `trickster`
`config.recordTTLSecs` | The relative expiration of cached queries. default is 6 hours (21600 seconds) | `21600`
`config.defaultStep` | The step (in seconds) of a query_range request if one is not provided by the client. This helps to correct improperly formed client requests. | `300`
`config.maxValueAgeSecs` | The maximum age of specific datapoints in seconds. Default is 86400 (24 hours). | `86400`
`config.fastForwardDisable` | Whether to disable fastforwarding (partial step to get latest data). | `false`
`config.logLevel` | The verbosity of the logger. Possible values are 'debug', 'info', 'warn', 'error'. | `info`
`name` | trickster container name | `trickster`
`image.repository` | trickster container image repository | `tricksterio/trickster`
`image.tag` | trickster container image tag | `0.1.7`
`image.pullPolicy` | trickster container image pull policy | `IfNotPresent`
`extraArgs` | Additional trickster container arguments | `{}`
`ingress.enabled` | If true, trickster Ingress will be created | `false`
`ingress.annotations` | trickster Ingress annotations | `{}`
`ingress.extraLabels` | trickster Ingress additional labels | `{}`
`ingress.hosts` | trickster Ingress hostnames | `[]`
`ingress.tls` | trickster Ingress TLS configuration (YAML) | `[]`
`nodeSelector` | node labels for trickster pod assignment | `{}`
`tolerations` | node taints to tolerate (requires Kubernetes >=1.6) | `[]`
`affinity` | pod affinity | `{}`
`schedulerName` | trickster alternate scheduler name | `nil`
`persistentVolume.enabled` | If true, trickster will create a Persistent Volume Claim | `true`
`persistentVolume.accessModes` | trickster data Persistent Volume access modes | `[ReadWriteOnce]`
`persistentVolume.annotations` | Annotations for trickster Persistent Volume Claim | `{}`
`persistentVolume.existingClaim` | trickster data Persistent Volume existing claim name | `""`
`persistentVolume.mountPath` | trickster data Persistent Volume mount root path | `/tmp/trickster`
`persistentVolume.size` | trickster data Persistent Volume size | `15Gi`
`persistentVolume.storageClass` | trickster data Persistent Volume Storage Class | `unset`
`podAnnotations` | annotations to be added to trickster pods | `{}`
`replicaCount` | desired number of trickster pods | `1`
`statefulSet.enabled` | If true, use a statefulset instead of a deployment for pod management | `false`
`priorityClassName` | trickster priorityClassName | `nil`
`resources` | trickster pod resource requests & limits | `{}`
`securityContext` | Custom [security context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/) for trickster containers | `{}`
`service.annotations` | annotations for trickster service | `{}`
`service.clusterIP` | internal trickster cluster service IP | `""`
`service.externalIPs` | trickster service external IP addresses | `[]`
`service.loadBalancerIP` | IP address to assign to load balancer (if supported) | `""`
`service.loadBalancerSourceRanges` | list of IP CIDRs allowed access to load balancer (if supported) | `[]`
`service.metricsPort` | trickster service port | `8080`
`service.servicePort` | trickster service port | `9090`
`service.type` | type of trickster service to create | `ClusterIP`
