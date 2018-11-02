# Testing CNI plugin with Istio

This page describes the testing strategy for the CNI plugin and how to run the tests.

## Types of tests

The CNI will have the following tests available.

1. Unit tests

2. Manually triggered functional tests (without any Istio components)

3. Istio e2e tests triggered on a istio/cni change

4. Istio e2e tests with the CNI enabled triggered on a istio/istio change

5. Manually executed e2e tests

## Manually executed e2e tests

The istio/cni repo will not have its own e2e tests.  The istio/istio e2e
tests will be utilized with the CNI enabled to validate that the CNI correctly interoperates
with the istio/istio code base.  Both the istio/istio repo and the istio/cni repo will have gate tests that run one or more of the Istio e2e tests with the CNI enabled to validate
that the CNI works properly.

In order to run the e2e tests in a local environment confirm:

1. That you can successfully run the desired Istio e2e tests in your local environment without the CNI enabled

2. That your local environment supports the requirements for the CNI plugin

To run the Istio e2e test first, clone the Istio repo in your local environment.  Then, there are two options to run the tests.

1. Run the Istio make target  
```console
make e2e_simple_cni
```

2. Run any of the Istio e2e targets after setting up a few environment variables:
```console
export ENABLE_ISTIO_CNI=true
export E2E_ARGS=--kube_inject_configmap=istio-sidecar-injector
export EXTRA_HELM_SETTINGS=--set istio-cni.excludeNamespaces={} --set istio-cni.tag=$YOUR_CNI_TAG --set istio-cni.hub=$YOUR_CNI_HUB
```
The value for `EXTRA_HELM_SETTINGS` will depend on your specific environment.

The tag `$YOUR_CNI_TAG` should be set to the `$TAG` value you used when you built your CNI image.
The hub `$YOUR_CNI_HUB` should be set to the location you used when you built your CNI image.
Istio in most cases runs the e2e tests in the `istio-system` namespace and therefore the `excludeNamespaces` must be set to `NULL`.
The e2e tests normally use `istioctl` to inject the sidecar and it is necessary to use a `ConfigMap` without the `initContainers` section.
Depending on your environment you may need to override other default settings.  Any additional override settings can be added via the `EXTRA_HELM_SETTINGS`

If the `tag` and `hub` is not set, the test will use the latest hub and tag values checked into the istio/cni repository.  The default `tag` and `hub` values are fine to use if you do not want to build your own CNI images.
