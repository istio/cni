#!/bin/bash

WD=$(dirname "$0")
WD=$(cd "$WD"; pwd)
ROOT=$(dirname "$WD")

TEST_WORK_ROOTDIR=${TEST_WORK_ROOTDIR:-/tmp}

TMP_CNICONFDIR=$(mktemp -d ${TEST_WORK_ROOTDIR}/cni-confXXXXX)
TMP_CNIBINDIR=$(mktemp -d ${TEST_WORK_ROOTDIR}/cni-binXXXXX)
TMP_K8S_SVCACCTDIR=$(mktemp -d ${TEST_WORK_ROOTDIR}/kube-svcacctXXXXX)

DEFAULT_CNICONF=${DEFAULT_CNICONF:-10-calico.conflist}

echo "conf-dir=${TMP_CNICONFDIR} ; bin-dir=${TMP_CNIBINDIR} ; k8s-serviceaccount=${TMP_K8S_SVCACCTDIR}"

HAS_FAILED=0

function populate_tempdirs() {
    echo "--------------------------------------------------"
    echo " Pre-populate working dirs"
    echo "--------------------------------------------------"
    for item in ${WD}/data/pre/*; do
        echo "Copying ${item} into temp config dir: ${TMP_CNICONFDIR}"
        cp $item ${TMP_CNICONFDIR}/
    done
    for item in ${WD}/data/k8s_svcacct/*; do
        echo "Copying ${item} into temp k8s serviceaccount dir: ${TMP_K8S_SVCACCTDIR}"
        cp $item ${TMP_K8S_SVCACCTDIR}/
    done
    echo "_________________________________________________________________________"
    printf "\n\n"
}

function compare_conf_result() {
    local result=$1
    local expected=$2

    # Check the result v. expected
    if cmp -s ${TMP_CNICONFDIR}/${result} ${expected}; then
        echo "PASS: result matches expected: ${TMP_CNICONFDIR}/${result} v. ${expected}"
    else
        TMP_CONF=$(mktemp ${TEST_WORK_ROOTDIR}/${result}.fail.XXXX)
        echo "FAIL: result doesn't match expected: ${TMP_CNICONFDIR}/${result} v. ${expected}"
        cp ${TMP_CNICONFDIR}/${result} ${TMP_CONF}
        echo "Check ${TMP_CONF} for diff contents"
        HAS_FAILED=1
    fi
}

function check_bin_dir() {
    # call with op=add or del
    local op="$1"; shift
    local files=$@

    if [[ "${op}" == "add" ]]; then
        for item in $files; do
            if [ -e ${TMP_CNIBINDIR}/${item} ]; then
                echo "PASS: File $item was added to ${TMP_CNIBINDIR}"
            else
                echo "FAIL: File $item was not added to ${TMP_CNIBINDIR}"
                HAS_FAILED=1
            fi
        done
    elif [[ "${op}" == "del" ]]; then
        # check that it's clean
        for item in $files; do
            if [ -e ${TMP_CNIBINDIR}/${item} ]; then
                echo "FAIL: File $item was not removed from ${TMP_CNIBINDIR}"
                HAS_FAILED=1
            else
                echo "PASS: File $item was removed from ${TMP_CNIBINDIR}"
            fi
        done
    fi
}

function do_test() {
    local test_num=$1
    local pre_conf=$2
    local result_filename=$3
    local expected_conf=$4
    if [[ $# > 4 ]]; then 
        local expected_clean=$5
    fi

    echo "--------------------------------------------------"
    echo "Test $1:  prior cni-conf='$2', expected result='$3'"
    echo "--------------------------------------------------"

    # Don't set the CNI conf file env var if $pre_conf not set
    if [[ "${pre_conf}" != "NONE" ]]; then
        export CNI_CONF_NAME=${pre_conf}
    else
        pre_conf=${result_filename}
    fi

    c_id=$(docker run --name test-istio-cni-install -v "$PWD":/usr/src/project-config -v ${TMP_CNICONFDIR}:/host/etc/cni/net.d -v ${TMP_CNIBINDIR}:/host/opt/cni/bin -v ${TMP_K8S_SVCACCTDIR}:/var/run/secrets/kubernetes.io/serviceaccount --env-file ${WD}/data/env_vars.sh -d -e CNI_NETWORK_CONFIG ${CNI_CONF_NAME:+ -e CNI_CONF_NAME} ${HUB}/install-cni:${TAG} /install-cni.sh 2> ${TMP_CNICONFDIR}/docker_run_stderr)

    if [ $? -ne 0 ] ; then
        echo "Test #${test_num} ERROR:  failed to start docker container '${HUB}/install-cni:${TAG}', see ${TMP_CNICONFDIR}/docker_run_stderr"
        exit 1
    fi
    echo "container ID: ${c_id}"

    sleep 10

    compare_conf_result ${result_filename} ${expected_conf}
    check_bin_dir add "istio-cni" "istio-iptables.sh"

    docker stop ${c_id}

    sleep 10

    echo "Test #${test_num}: Check the cleanup worked"
    if [[ -z ${expected_clean} ]]; then
        compare_conf_result ${result_filename} ${WD}/data/pre/${pre_conf}
    else
        compare_conf_result ${result_filename} ${expected_clean}
    fi

    check_bin_dir del "istio-cni" "istio-iptables.sh"
    
    docker logs ${c_id}

    docker rm ${c_id}
    echo "_________________________________________________________________________"
    printf "\n\n"
}

populate_tempdirs

export CNI_NETWORK_CONFIG=$(cat <<EOF 
{
  "type": "istio-cni",
  "log_level": "info",
  "kubernetes": {
      "kubeconfig": "__KUBECONFIG_FILEPATH__",
      "cni_bin_dir": "/opt/cni/bin",
      "exclude_namespaces": [ "istio-system" ]
  }
}
EOF
)


# run the test
do_test $@

exit $HAS_FAILED
