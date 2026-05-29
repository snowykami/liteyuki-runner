## Kubernetes Docker in Docker Deployment with `gitea-runner`

NOTE: Docker in Docker (dind) requires elevated privileges on Kubernetes. The current way to achieve this is to set the pod `SecurityContext` to `privileged`. Keep in mind that this is a potential security issue that has the potential for a malicious application to break out of the container context.

NOTE: `dind-docker.yaml` uses the native sidecar pattern (init container with `restartPolicy: Always`), which requires Kubernetes 1.29+ (or 1.28 with the `SidecarContainers` feature gate).

NOTE: A helm chart for `gitea-runner` also exists for easier deployments https://gitea.com/gitea/helm-actions

Files in this directory:

- [`dind-docker.yaml`](dind-docker.yaml)
  How to create a Deployment and Persistent Volume for Kubernetes to act as a runner. The Docker credentials are re-generated each time the pod connects and does not need to be persisted.

- [`rootless-docker.yaml`](rootless-docker.yaml)
  How to create a rootless Deployment and Persistent Volume for Kubernetes to act as a runner. The Docker credentials are re-generated each time the pod connects and does not need to be persisted.
