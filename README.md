# Istio CNI plugin (WIP)

For application pods in the Istio service mesh, all traffic to/from the pods needs to go throug the
sidecar proxies (istio-proxy containers).  The `istio-cni` CNI plugin is responsible for setting
up pods' networking to fullfill this requirement.

This is currently accomplished (for IPv4) via configuring the iptables rules in the netns for the pods.

## Implementation Details

### Framework
The framework for this implementation of the CNI plugin is based on the
[containernetworking sample plugin](https://github.com/containernetworking/plugins/blob/master/plugins/sample).

**TODO** Figure out any CNI version specific semantics.

### Build
The Istio makefiles and container build logic was leveraged heavily/lifted for this repo.

Specifically:
- golang build logic
- multi-arch target logic
- k8s lib versions (Gopkg.toml)
- docker container build logic
  - setup staging dir for docker build
  - grab built executables from target dir and cp to staging dir for docker build
  - tagging and push logic

### Deployment
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

### Plugin Logic

#### cmdAdd
**IN PROGRESS**  Workflow:
1.  Get k8s pod info
    1. port list -- Copy/Use Calico method that exactly does this.
1.  Determine if the pod needs to be setup for Istio sidecar proxy
    1.  if pod has a container named `istio-proxy` AND pod has more than 1 container
        1.  Final Logic TBD -- e.g. pod labels?  namespace checks?
1.  Setup iptables with the required port list
    1.  Initially this could just be done by calls to tools/deb/istio-iptables.sh


**TBD** istioctl / auto-sidecar-inject logic for handling things like specific include/exclude IPs and any
other features.
-  Could incorporate controller to watch configmaps or CRDs and update the `istio-cni` plugin's configmap
   with these options.

#### cmdDel
Anything needed?  The netns is destroyed by kubelet so ideally this is a NOOP.

#### Logging
The plugin leverages `logrus` & directly utilizes some Calico logging lib util functions.

## How to use

1. Install Istio control-plane

1. Install `istio-cni`: `kubectl apply -f deployments/kubernetes/install/manifests/istio-cni.yaml`

1. remove the `initContainers` section from the result of helm template's rendering of
   istio/templates/sidecar-injector-configmap.yaml and apply it to replace the
   `istio-sidecar-injector` configmap.  --e.g. pull the `istio-sidecar-injector` configmap from
   `istio.yaml` and remove the `initContainers` section and `kubectl apply -f <configmap.yaml>`
   1. restart the `istio-sidecar-injector` pod via `kubectl delete pod ...`

1. With auto-sidecar injection, the init containers will no longer be added to the pods and the CNI
   will be the component setting the iptables up for the pods.
