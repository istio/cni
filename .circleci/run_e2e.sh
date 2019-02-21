#!/bin/bash

CNI_HUB=${CNI_HUB:-istio}
CNI_TAG=${CNI_TAG:-$1}

chartdir=$(pwd)/charts
mkdir ${chartdir}

cd /go/src/istio.io/istio

# Install istio-cni prior to executing the Istio e2e test.  Now that the helm chart for istio/istio no longer
# depends on the istio-cni chart, we need to explicitly do these steps.
helm init --client-only
helm repo add istio.io https://storage.googleapis.com/istio-release/releases/1.1.0-snapshot.6/charts
helm fetch --untar --untardir ${chartdir} istio.io/istio-cni
helm template --values ${chartdir}/istio-cni/values.yaml --name=istio-cni --namespace=kube-system --set "excludeNamespaces={}" --set hub=${CNI_HUB} --set tag=${CNI_TAG} --set pullPolicy=IfNotPresent --set logLevel=${CNI_LOGLVL:-debug}  ${chartdir}/istio-cni > istio-cni_install.yaml
kubectl apply -f istio-cni_install.yaml

HUB=${HUB:-gcr.io/istio-release}
TAG=${TAG:-master-latest-daily}

HUB=${HUB} TAG=${TAG} make istioctl

HUB=${HUB} TAG=${TAG} ENABLE_ISTIO_CNI=true E2E_ARGS="--kube_inject_configmap=istio-sidecar-injector ${SKIP_CLEAN:+ --skip_cleanup}" make test/local/auth/e2e_simple
