#!/bin/bash

CNI_HUB=${CNI_HUB:-istio}
CNI_TAG=${CNI_TAG:-$1}

chartdir=$(pwd)/charts
mkdir ${chartdir}

cd /go/src/istio.io/istio

HUB=${HUB:-gcr.io/istio-release}
TAG=${TAG:-master-latest-daily}

helm init --client-only
helm repo add istio.io https://storage.googleapis.com/istio-release/releases/1.1.0-snapshot.6/charts
helm fetch --untar --untardir ${chartdir} istio.io/istio-cni

helm template --values ${chartdir}/istio-cni/values.yaml --name=istio-cni --namespace=istio-system --set "excludeNamespaces={}" --set hub=${CNI_HUB} --set tag=${CNI_TAG} --set pullPolicy=IfNotPresent --set logLevel=${CNI_LOGLVL:-debug}  ${chartdir}/istio-cni > istio-cni_install.yaml

kubectl create ns istio-system
kubectl apply -f istio-cni_install.yaml


HUB=${HUB} TAG=${TAG} make istioctl

# Remove any pre-existing charts
# ...This seems to get around an issue seen with 1.1 where helm dep update fails with:
# /go/out/linux_amd64/release/helm dep update --skip-refresh install/kubernetes/helm/istio
#
# Error: Unable to move current charts to tmp dir: rename /go/src/istio.io/istio/install/kubernetes/helm/istio/charts /go/src/istio.io/istio/install/kubernetes/helm/istio/tmpcharts: invalid cross-device link
#rm -rf /go/src/istio.io/istio/install/kubernetes/helm/istio/charts

HUB=${HUB} TAG=${TAG} ENABLE_ISTIO_CNI=true EXTRA_HELM_SETTINGS="--set istio-cni.excludeNamespaces={} --set istio-cni.hub=${CNI_HUB} --set istio-cni.tag=${CNI_TAG} --set istio-cni.pullPolicy=IfNotPresent --set istio-cni.logLevel=${CNI_LOGLVL:-debug}" E2E_ARGS="--kube_inject_configmap=istio-sidecar-injector ${SKIP_CLEAN:+ --skip_cleanup}" make test/local/auth/e2e_simple
