FROM  istio/ci:go1.10-k8s1.10.4-helm2.7.2-minikube0.25
ARG  istio_branch=master

RUN  git clone --branch $istio_branch https://github.com/istio/istio.git /go/src/istio.io/istio

ADD  run_e2e.sh /go/

ADD  helm /go/helm
