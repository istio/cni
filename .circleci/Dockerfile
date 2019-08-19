FROM gcr.io/istio-testing/circleci:2019-04-22
ARG  istio_branch=master

RUN  git clone --branch $istio_branch https://github.com/istio/istio.git /go/src/istio.io/istio

ADD  run_e2e.sh /go/

ADD  helm /go/helm
