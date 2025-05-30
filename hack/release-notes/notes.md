# <img src="https://github.com/${REPO}/raw/v${TAG}/docs/images/logos/trickster-logo.svg" width=60 />&nbsp;&nbsp;&nbsp;&nbsp;<img src="https://github.com/${REPO}/raw/v${TAG}/docs/images/logos/trickster-text.svg" width=280 />

Welcome to Trickster ${TAG}! :tada:

<!-- In this release, we:
* Summary of high-level changes -->

<!-- Thanks to:
* @${GITHUB_USER} -->

<details>
<summary>Run via docker</summary>

```bash
# via ghcr.io
docker run --name trickster -d -v /path/to/trickster.yaml:/etc/trickster/trickster.yaml -p 0.0.0.0:8480:8480 ghcr.io/${REPO}:${TAG}

# via docker.io
docker run --name trickster -d -v /path/to/trickster.yaml:/etc/trickster/trickster.yaml -p 0.0.0.0:8480:8480 docker.io/${REPO}:${TAG}
```
</details>

<details>
<summary>Run via kubernetes/helm</summary>

```bash
helm install trickster oci://ghcr.io/trickstercache/charts/trickster --version '^2' --set image.tag="${TAG}"
```

Note:

* The trickster chart version is managed separately from the Trickster version.
* The latest major version is 2.x, and supports a minimum Trickster version of 2.0.0 (beta3 or later).

</details>

---

For **more information**, see:
* [Trying Out Trickster](https://github.com/${REPO}/tree/v${TAG}#trying-out-trickster)
* trickster's [helm chart](https://github.com/trickstercache/helm-charts).
