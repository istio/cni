# Istio CNI plugin (WIP/PoC)

For application pods in the Istio service mesh, all traffic to/from the pods needs to go through the
sidecar proxies (istio-proxy containers).  This `istio-cni` CNI plugin PoC will set up the
pods' networking to fulfill this requirement in place of the current Istio injected pod `initContainers`
`istio-init` approach.

This is currently accomplished (for IPv4) via configuring the iptables rules in the netns for the pods.

The CNI handling the netns setup replaces the current Istio approach using a `NET_ADMIN` privileged
`initContainers` container, `istio-init`, injected in the pods along with `istio-proxy` sidecars.  This
removes the need for a privileged, `NET_ADMIN` container in the Istio users' application pods.

## Comparison with Pod Network Controller Approach

The proposed [Istio pod network controller](https://github.com/sabre1041/istio-pod-network-controller) has
the problem of synchronizing the netns setup with the rest of the pod init.  This approach requires implementing
custom synchronization between the controller and pod initialization.

Kubernetes has already solved this problem by not starting any containers in new pods until the full CNI plugin
chain has completed successfully.  Also, architecturally, the CNI plugins are the components responsible for network
setup for container runtimes.

## Usage
The following are the steps to install and use the CNI plugin.

1. clone this repo

1. Install Istio control-plane

1. (Manual Option) Modify [istio-cni.yaml](deployments/kubernetes/install/manifests/istio-cni.yaml)
   1. set `CNI_CONF_NAME` to the filename for your k8s cluster's CNI config file in `/etc/cni/net.d`
   1. set `exclude_namespaces` to include the namespace the Istio control-plane is installed in
   1. set `cni_bin_dir` to your kubernetes install's CNI bin location (the value of kubelet's `--cni-bin-dir`)
      1. default is `/opt/cni/bin`

1. (Helm Option) Construct a `helm template` or `helm install` command for your Kubernetes environment

   1. `helm template deployments/kubernetes/install/helm/istio-cni --values deployments/kubernetes/install/helm/istio-cni/values.yaml --namespace kube-system --set hub=$HUB --set tag=$TAG > $HOME/istio-cni.yaml`
   
      | Environment | helm values |
      |-------------|-------------|
      | default kubeadm | [values.yaml](deployments/kubernetes/install/helm/istio-cni/values.yaml) |
      | GKE | [values_gke.yaml](deployments/kubernetes/install/helm/istio-cni/values_gke.yaml) |

   1. helm chart params
   
      | Option | Values | Default | Description |
      |--------|--------|---------|-------------|
      | hub | | | The container registry to pull the `install-cni` container from. |
      | tag | | | The container tag to use to pull the `install-cni` container. |
      | logLevel | panic, fatal, error, warn, info, debug | `warn` | Logging level for CNI binary |
      | excludeNamespaces | `[]string` | `[ istio-system ]` | list of namespaces to exclude from Istio pod check |
      | cniBinDir | | `/opt/cni/bin` | Must be the same as the environment's `--cni-bin-dir` setting (kubelet param) |
      | cniConfDir | | `/etc/cni/net.d` | Must be the same as the environment's `--cni-conf-dir` setting (kubelet param) |
      | cniConfFileName | | None | Leave unset to auto-find the first file in the `cni-conf-dir` (as kubelet does).  Primarily used for testing `install-cni` plugin config.  If set, `install-cni` will inject the plugin config into this file in the `cni-conf-dir` |

1. Install `istio-cni`: `kubectl apply -f $HOME/istio-cni.yaml`

1. remove the `initContainers` section from the result of helm template's rendering of
   istio/templates/sidecar-injector-configmap.yaml and apply it to replace the
   `istio-sidecar-injector` configmap.  --e.g. pull the `istio-sidecar-injector` configmap from
   `istio.yaml` and remove the `initContainers` section and `kubectl apply -f <configmap.yaml>`
   1. restart the `istio-sidecar-injector` pod via `kubectl delete pod ...`

1. With auto-sidecar injection, the init containers will no longer be added to the pods and the CNI
   will be the component setting the iptables up for the pods.

## Validate the iptables are modified

1. Collect your pod's container id using kubectl.
```
$ ns=test-istio
$ podnm=reviews-v1-6b7f6db5c5-59jhf
$ container_id=$(kubectl get pod -n ${ns} ${podnm} -o jsonpath="{.status.containerStatuses[?(@.name=='istio-proxy')].containerID}" | sed -n 's/docker:\/\/\(.*\)/\1/p')
```

2. SSH into the Kubernetes' worker node that runs your pod.

3. Use `nsenter` to view the iptables.
```
$ cpid=$(docker inspect --format '{{ .State.Pid }}' $container_id)
$ nsenter -t $cpid -n iptables -L -t nat -n -v --line-numbers -x
```

### Hosted Kubernetes

Not all hosted Kubernetes clusters are created with the kubelet configured to use the CNI plugin so
compatibility with this `istio-cni` solution is not ubiquitous.  The `istio-cni` plugin is expected
to work with any hosted kubernetes leveraging CNI plugins.  The below table indicates the known CNI status
of hosted Kubernetes environments and whether `istio-cni` has been trialed in the cluster type.

| Hosted Cluster Type | Uses CNI | istio-cni tested? |
|---------------------|----------|-------------------|
| GKE 1.9.7-gke.6 default | N | N |
| GKE 1.9.7-gke.6 w/ [network-policy](https://cloud.google.com/kubernetes-engine/docs/how-to/network-policy) | Y | Y |
| IKS (IBM cloud) | Y | Y (on k8s 1.10) |
| EKS (AWS) | Y | N |
| AKS (Azure) | Y | N |
| Red Hat OpenShift 3.10| Y | Y |

#### GKE Setup

1. Enable [network-policy](https://cloud.google.com/kubernetes-engine/docs/how-to/network-policy) in your cluster.  NOTE: for existing clusters this redeploys the nodes.

1. Make sure your kubectl user (service-account) has a ClusterRoleBinding to the `cluster-admin` role.  This is also a typical pre-requisite for installing Istio on GKE.
   1. `kubectl create clusterrolebinding cni-cluster-admin-binding --clusterrole=cluster-admin --user=tiswanso@gmail.com`
      1. User `tiswanso@gmail.com` is an admin user associated with the gcloud GKE cluster

1. Install `istio-cni`: `kubectl apply -f deployments/kubernetes/install/manifests/istio-cni_gke.yaml`

1. Install Istio

1. remove the `initContainers` section from the result of helm template's rendering of
   istio/templates/sidecar-injector-configmap.yaml and apply it to replace the
   `istio-sidecar-injector` configmap.  --e.g. pull the `istio-sidecar-injector` configmap from
   `istio.yaml` and remove the `initContainers` section and `kubectl apply -f <configmap.yaml>`
   1. restart the `istio-sidecar-injector` pod via `kubectl delete pod ...`

1. With auto-sidecar injection, the init containers will no longer be added to the pods and the CNI
   will be the component setting the iptables up for the pods.

### IKS Setup

No special set up is required for IKS, as it is currently use the default `cni-conf-dir` and `cni-bin-dir`.

### Red Hat OpenShift Setup

1. Run the DaemonSet container as privileged so that it has proper write permission in the host filesystem: Modify [istio-cni.yaml](deployments/kubernetes/install/manifests/istio-cni.yaml#L105) adding this section within the `install-cni` container:
```yaml
securityContext:
  privileged: true
```

2. Grant privileged permission to `istio-cni` service account:
```sh
$ oc adm policy add-scc-to-user privileged -z istio-cni -n kube-system
```


## Build

First, clone this repository under `$GOPATH/src/istio.io/`.

For linux targets:

```sh
$ GOOS=linux make build
```

You can also build the project from a non-standard location like so:

```sh
$ ISTIO_CNI_RELPATH=github.com/some/cni GOOS=linux make build
```

To push the Docker image:

```sh
$ export HUB=docker.io/tiswanso
$ export TAG=dev
$ GOOS=linux make docker.push
```

**NOTE:** Set HUB and TAG per your docker registry.

### Helm

The helm package tarfile can be created via

```
helm package $GOPATH/src/istio.io/deployments/kubernetes/install/helm/istio-cni
```

#### Serve Helm Repo

An example for hosting a test repo for the helm istio-cni package:
1. Create package tarfile with `helm package $GOPATH/src/istio.io/deployments/kubernetes/install/helm/istio-cni`
1. copy tarfile to dir to serve the repo from
1. Run `helm serve --repo-path <dir where helm tarfile is>`
   1. The repo URL will be output (`http://127.0.0.1:8879`)

To use this repo via `helm install`:
1. `helm repo add local_istio http://127.0.0.1:8879`
1. `helm repo update`

At this point the `istio-cni` chart is ready for use by `helm install`.

To make use of the `istio-cni` chart from another chart:
1. Add the following to the other chart's `requirements.yaml`:

   ```
   - name: istio-cni
     version: ">=0.0.1"
     repository: http://127.0.0.1:8879/
     condition: istio-cni.enabled
   ```

1. Run `helm dependency update <chart>` on the chart that needs to depend on istio-cni.
   1. NOTE: for [istio/istio](https://github.com/istio/istio/tree/master/install/kubernetes/helm/istio) the charts
      need to be reorganized to make `helm dependency update` work.  The child charts (pilot, galley, etc) need to
      be made independent charts in the directory at the same level as the main `istio` chart
      (https://github.com/istio/istio/pull/9306).

## Implementation Details

**TODOs**
- Figure out any CNI version specific semantics.
- Add plugin parameters for included/exclude IP CIDRs
- Add plugin parameters for proxy params, ie. listen port, UID, etc.
- Make `istio-cni.yaml` into a helm chart

### Overview

- [istio-cni.yaml](deployments/kubernetes/install/manifests/istio-cni.yaml)
   - manifest for deploying `install-cni` container as daemonset
   - `istio-cni-config` configmap with CNI plugin config to add to CNI plugin chained config
   - creates service-account `istio-cni` with `ClusterRoleBinding` to allow gets on pods' info

- `install-cni` container
   - copies `istio-cni` binary and `istio-iptables.sh` to `/opt/cni/bin`
   - creates kubeconfig for the service account the pod is run under
   - injects the CNI plugin config to the config file pointed to by CNI_CONF_NAME env var
     - example: `CNI_CONF_NAME: 10-calico.conflist`
     - `jq` is used to insert `CNI_NETWORK_CONFIG` into the `plugins` list in `/etc/cni/net.d/${CNI_CONF_NAME}`

- `istio-cni`
  - CNI plugin executable copied to `/opt/cni/bin`
  - currently implemented for k8s only
  - on pod add, determines whether pod should have netns setup to redirect to Istio proxy
    - if so, calls `istio-iptables.sh` with params to setup pod netns

- [istio-iptables.sh](tools/istio-cni-docker.mk)
  - direct copy of Istio's [istio-iptables.sh0(https://github.com/istio/istio/blob/master/tools/deb/istio-iptables.sh)
  - sets up iptables to redirect a list of ports to the port envoy will listen

### Background
The framework for this implementation of the CNI plugin is based on the
[containernetworking sample plugin](https://github.com/containernetworking/plugins/blob/master/plugins/sample).

#### Build Toolchains
The Istio makefiles and container build logic was leveraged heavily/lifted for this repo.

Specifically:
- golang build logic
- multi-arch target logic
- k8s lib versions (Gopkg.toml)
- docker container build logic
  - setup staging dir for docker build
  - grab built executables from target dir and cp to staging dir for docker build
  - tagging and push logic

#### Deployment 
The details for the deployment & installation of this plugin were pretty much lifted directly from the
[Calico CNI plugin](https://github.com/projectcalico/cni-plugin).

Specifically:
  - [CNI installation script](https://github.com/projectcalico/cni-plugin/blob/master/k8s-install/scripts/install-cni.sh)
    - This does the following
      - sets up CNI conf in /host/etc/cni/net.d/*
      - copies calico CNI binaries to /host/opt/cni/bin
      - builds kubeconfig for CNI plugin from service-account info mounted in the pod:
        https://github.com/projectcalico/cni-plugin/blob/master/k8s-install/scripts/install-cni.sh#L142
      - reference: https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/
  - The CNI installation script is containerized and deployed as a daemonset in k8s.  The relevant
    calico k8s manifests were used as the model for the istio-cni plugin's manifest:
    - [daemonset and configmap](https://docs.projectcalico.org/v3.2/getting-started/kubernetes/installation/hosted/calico.yaml)
      - search for the `calico-node` Daemonset and its `install-cni` container deployment
    - [RBAC](https://docs.projectcalico.org/v3.2/getting-started/kubernetes/installation/rbac.yaml)
      - this creates the service account the CNI plugin is configured to use to access the kube-api-server

The installation script `install-cni.sh` injects the `istio-cni` plugin config at the end of the CNI plugin chain
config.  It creates or modifies the file from the configmap created by the Kubernetes manifest.

#### Plugin Logic

##### cmdAdd
Workflow:
1.  Check k8s pod namespace against exclusion list (plugin config)
    1.  Config must exclude namespace that Istio control-plane is installed in
    1.  if excluded, ignore the pod and return prevResult
1.  Get k8s pod info
    1.  determine containerPort list
1.  Determine if the pod needs to be setup for Istio sidecar proxy
    1.  if pod has a container named `istio-proxy` AND pod has more than 1 container
        1.  Final Logic TBD -- e.g. pod labels?  namespace checks?
1.  Setup iptables with the required port list
    1.  `nsenter --net=<k8s pod netns> /opt/cni/bin/istio-iptables.sh ...`
1.  Return prevResult

**TBD** istioctl / auto-sidecar-inject logic for handling things like specific include/exclude IPs and any
other features.
-  Watch configmaps or CRDs and update the `istio-cni` plugin's config
   with these options.

##### cmdDel
Anything needed?  The netns is destroyed by kubelet so ideally this is a NOOP.

##### Logging
The plugin leverages `logrus` & directly utilizes some Calico logging lib util functions.
