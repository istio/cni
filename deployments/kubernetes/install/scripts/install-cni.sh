#!/bin/sh

# Script to install Istio CNI on a Kubernetes host.
# - Expects the host CNI binary path to be mounted at /host/opt/cni/bin.
# - Expects the host CNI network config path to be mounted at /host/etc/cni/net.d.
# - Expects the desired CNI config in the CNI_NETWORK_CONFIG env variable.

# Ensure all variables are defined, and that the script fails when an error is hit.
set -u -e

# Capture the usual signals and exit from the script
trap 'echo "SIGINT received, simply exiting..."; exit 0' SIGINT
trap 'echo "SIGTERM received, simply exiting..."; exit 0' SIGTERM
trap 'echo "SIGHUP received, simply exiting..."; exit 0' SIGHUP

# Helper function for raising errors
# Usage:
# some_command || exit_with_error "some_command_failed: maybe try..."
exit_with_error(){
  echo $1
  exit 1
}

# The directory on the host where CNI networks are installed. Defaults to
# /etc/cni/net.d, but can be overridden by setting CNI_NET_DIR.  This is used
# for populating absolute paths in the CNI network config to assets
# which are installed in the CNI network config directory.
HOST_CNI_NET_DIR=${CNI_NET_DIR:-/etc/cni/net.d}
MOUNTED_CNI_NET_DIR=${MOUNTED_CNI_NET_DIR:-/host/etc/cni/net.d}

# Clean up any existing binaries / config / assets.
rm -f /host/opt/cni/bin/istio-cni

# Choose which default cni binaries should be copied
SKIP_CNI_BINARIES=${SKIP_CNI_BINARIES:-""}
SKIP_CNI_BINARIES=",$SKIP_CNI_BINARIES,"
UPDATE_CNI_BINARIES=${UPDATE_CNI_BINARIES:-"true"}

# Place the new binaries if the directory is writeable.
for dir in /host/opt/cni/bin /host/secondary-bin-dir
do
  if [ ! -w "$dir" ];
  then
    echo "$dir is non-writeable, skipping"
    continue
  fi
  for path in /opt/cni/bin/*;
  do
    filename="$(basename $path)"
    tmp=",$filename,"
    if [ "${SKIP_CNI_BINARIES#*$tmp}" != "$SKIP_CNI_BINARIES" ];
    then
      echo "$filename is in SKIP_CNI_BINARIES, skipping"
      continue
    fi
    if [ "${UPDATE_CNI_BINARIES}" != "true" -a -f $dir/$filename ];
    then
      echo "$dir/$filename is already here and UPDATE_CNI_BINARIES isn't true, skipping"
      continue
    fi
    cp $path $dir/ || exit_with_error "Failed to copy $path to $dir. This may be caused by selinux configuration on the host, or something else."
  done

  echo "Wrote Istio CNI binaries to $dir"
  #echo "CNI plugin version: $($dir/istio-cni -v)"
done

TMP_CONF='/istio-cni.conf.tmp'
# If specified, overwrite the network configuration file.
: ${CNI_NETWORK_CONFIG_FILE:=}
: ${CNI_NETWORK_CONFIG:=}
if [ -e "${CNI_NETWORK_CONFIG_FILE}" ]; then
  echo "Using CNI config template from ${CNI_NETWORK_CONFIG_FILE}."
  cp "${CNI_NETWORK_CONFIG_FILE}" "${TMP_CONF}"
elif [ "${CNI_NETWORK_CONFIG}" != "" ]; then
  echo "Using CNI config template from CNI_NETWORK_CONFIG environment variable."
  cat >$TMP_CONF <<EOF
${CNI_NETWORK_CONFIG}
EOF
fi

KUBECFG_FILE_NAME=${KUBECFG_FILE_NAME:-ZZZ-istio-cni-kubeconfig}

SERVICE_ACCOUNT_PATH=/var/run/secrets/kubernetes.io/serviceaccount
KUBE_CA_FILE=${KUBE_CA_FILE:-$SERVICE_ACCOUNT_PATH/ca.crt}
SKIP_TLS_VERIFY=${SKIP_TLS_VERIFY:-false}
# Pull out service account token.
SERVICEACCOUNT_TOKEN=$(cat $SERVICE_ACCOUNT_PATH/token)

# Check if we're running as a k8s pod.
if [ -f "$SERVICE_ACCOUNT_PATH/token" ]; then
  # We're running as a k8d pod - expect some variables.
  if [ -z ${KUBERNETES_SERVICE_HOST} ]; then
    echo "KUBERNETES_SERVICE_HOST not set"; exit 1;
  fi
  if [ -z ${KUBERNETES_SERVICE_PORT} ]; then
    echo "KUBERNETES_SERVICE_PORT not set"; exit 1;
  fi

  if [ "$SKIP_TLS_VERIFY" == "true" ]; then
    TLS_CFG="insecure-skip-tls-verify: true"
  elif [ -f "$KUBE_CA_FILE" ]; then
    TLS_CFG="certificate-authority-data: $(cat $KUBE_CA_FILE | base64 | tr -d '\n')"
  fi

  # Write a kubeconfig file for the CNI plugin.  Do this
  # to skip TLS verification for now.  We should eventually support
  # writing more complete kubeconfig files. This is only used
  # if the provided CNI network config references it.
  touch ${MOUNTED_CNI_NET_DIR}/${KUBECFG_FILE_NAME}
  chmod ${KUBECONFIG_MODE:-600} ${MOUNTED_CNI_NET_DIR}/${KUBECFG_FILE_NAME}
  cat > ${MOUNTED_CNI_NET_DIR}/${KUBECFG_FILE_NAME} <<EOF
# Kubeconfig file for Istio CNI plugin.
apiVersion: v1
kind: Config
clusters:
- name: local
  cluster:
    server: ${KUBERNETES_SERVICE_PROTOCOL:-https}://[${KUBERNETES_SERVICE_HOST}]:${KUBERNETES_SERVICE_PORT}
    $TLS_CFG
users:
- name: istio-cni
  user:
    token: "${SERVICEACCOUNT_TOKEN}"
contexts:
- name: istio-cni-context
  context:
    cluster: local
    user: istio-cni
current-context: istio-cni-context
EOF

fi


# Insert any of the supported "auto" parameters.
grep "__KUBERNETES_SERVICE_HOST__" $TMP_CONF && sed -i s/__KUBERNETES_SERVICE_HOST__/${KUBERNETES_SERVICE_HOST}/g $TMP_CONF
grep "__KUBERNETES_SERVICE_PORT__" $TMP_CONF && sed -i s/__KUBERNETES_SERVICE_PORT__/${KUBERNETES_SERVICE_PORT}/g $TMP_CONF
sed -i s/__KUBERNETES_NODE_NAME__/${KUBERNETES_NODE_NAME:-$(hostname)}/g $TMP_CONF
sed -i s/__KUBECONFIG_FILENAME__/${KUBECFG_FILE_NAME}/g $TMP_CONF

# Use alternative command character "~", since these include a "/".
sed -i s~__KUBECONFIG_FILEPATH__~${HOST_CNI_NET_DIR}/${KUBECFG_FILE_NAME}~g $TMP_CONF
sed -i s~__LOG_LEVEL__~${LOG_LEVEL:-warn}~g $TMP_CONF

# default to first file in `ls` output
CNI_CONF_NAME=${CNI_CONF_NAME:-$(ls ${MOUNTED_CNI_NET_DIR} | head -n 1)}
CNI_CONF_NAME=${CNI_CONF_NAME:-10-calico.conflist}
CNI_OLD_CONF_NAME=${CNI_OLD_CONF_NAME:-${CNI_CONF_NAME}}

# Log the config file before inserting service account token.
# This way auth token is not visible in the logs.
echo "CNI config: $(cat ${TMP_CONF})"

sed -i s/__SERVICEACCOUNT_TOKEN__/${SERVICEACCOUNT_TOKEN:-}/g $TMP_CONF

CNI_CONF_FILE=${MOUNTED_CNI_NET_DIR}/${CNI_CONF_NAME}
if [ -e "${MOUNTED_CNI_NET_DIR}/${CNI_CONF_NAME}" ]; then
    # This section overwrites an existing plugins list entry to for istio-cni
    CNI_TMP_CONF_DATA=$(cat ${TMP_CONF})
    CNI_CONF_DATA=$(cat ${CNI_CONF_FILE} | jq 'del( .plugins[]? | select(.type == "istio-cni"))' | jq ".plugins += [${CNI_TMP_CONF_DATA}]")
    echo "${CNI_CONF_DATA}" > ${TMP_CONF}
fi

# Delete old CNI config files for upgrades.
if [ "${CNI_CONF_NAME}" != "${CNI_OLD_CONF_NAME}" ]; then
    rm -f "${MOUNTED_CNI_NET_DIR}/${CNI_OLD_CONF_NAME}"
fi
# Move the temporary CNI config into place.
mv $TMP_CONF ${MOUNTED_CNI_NET_DIR}/${CNI_CONF_NAME} || \
  exit_with_error "Failed to mv files. This may be caused by selinux configuration on the host, or something else."

echo "Created CNI config ${CNI_CONF_NAME}"

# Unless told otherwise, sleep forever.
# This prevents Kubernetes from restarting the pod repeatedly.
should_sleep=${SLEEP:-"true"}
echo "Done configuring CNI.  Sleep=$should_sleep"
while [ "$should_sleep" == "true"  ]; do
  sleep 10
done
