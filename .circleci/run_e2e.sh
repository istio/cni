#!/bin/bash

CNI_HUB=${CNI_HUB:-istio}
CNI_TAG=${CNI_TAG:-$1}

cd /go/src/istio.io/istio

HUB=${HUB:-gcr.io/istio-release}
TAG=${TAG:-master-latest-daily}

HUB=${HUB} TAG=${TAG} make istioctl

HUB=${HUB} TAG=${TAG} ENABLE_ISTIO_CNI=false EXTRA_HELM_SETTINGS="--set istio-cni.excludeNamespaces={} --set istio-cni.hub=${CNI_HUB} --set istio-cni.tag=${CNI_TAG} --set istio-cni.pullPolicy=IfNotPresent" E2E_ARGS="--kube_inject_configmap=istio-sidecar-injector" make test/local/auth/e2e_simple
