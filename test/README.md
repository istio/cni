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

The istio/cni repo will not have it's own e2e tests.  The istio/istio e2e
tests will be utilized with the CNI enabled to validate the CNI correctly interoperates
with the istio/istio code base.  Both the istio/istio repo and the istio/cni repo will have gate tests that run one or more of the istio e2e tests with the CNI enabled to validate
that the CNI works properly.

In order to run the e2e tests in a local environment first confirm:

1. That you can run any of the Istio e2e tests in your local environment

2. That your local environment can support the CNI plugin

To run the Istio e2e test first clone the Istio repo in your local environment.  Then there are two options to run the tests.

1. Run the Istio make target  ```sh make e2e_simple_cni```

2. Run any of the Istio e2e targets after setting up a few environmental variables:
	1. ENABLE_ISTIO_CNI=true
	2. E2E_ARGS=--kube_inject_configmap=istio-sidecar-injector
	3. EXTRA_HELM_SETTINGS
		1. istio-cni.tag
		2. istio-cni.hub
		3. istio-cni.excludeNamespaces

The value for EXTRA_HELM_SETTINGS will depend on your specific environment.

The tag should be set to the value you have used when pushing you CNI image.
The hub should be set to the location you have pushed your CNI image.
Istio in most cases runs the e2e tests in the istio-system namespace and therefore the excludedNamespaces must be set to NULL.
The e2e tests normally use istioctl to inject the sidecar and it is necessary to use a conffigmap without the initContianers section.

If the tag and hub is not set the testwill use the latest default hub and tag values checking into the istio/cni rep.
