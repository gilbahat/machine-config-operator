#!/usr/bin/env bash
#
# Writes a kubeconfig for the machine-config-daemon service account, for use by
# the daemon on AIX nodes (which runs as an SRC subsystem, not as a pod, so it
# has no projected service account token). Run with cluster-admin `oc`.
#
#   hack/aix/mcd-kubeconfig.sh > kubeconfig
#
# The token comes from a long-lived service-account-token Secret so that it
# survives reboots; delete the Secret to revoke every AIX daemon's access.
set -euo pipefail

ns=openshift-machine-config-operator
sa=machine-config-daemon
secret=machine-config-daemon-aix-token

oc apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${secret}
  namespace: ${ns}
  annotations:
    kubernetes.io/service-account.name: ${sa}
type: kubernetes.io/service-account-token
EOF

for _ in $(seq 30); do
	token=$(oc get secret -n "$ns" "$secret" -o jsonpath='{.data.token}' | base64 -d)
	[ -n "$token" ] && break
	sleep 1
done
[ -n "${token:-}" ] || { echo "token was not populated for secret $ns/$secret" >&2; exit 1; }

ca=$(oc get secret -n "$ns" "$secret" -o jsonpath='{.data.ca\.crt}')
server=$(oc whoami --show-server)
# Prefer the internal API endpoint that nodes use when it is published.
internal=$(oc get infrastructure cluster -o jsonpath='{.status.apiServerInternalURI}' 2>/dev/null || true)
[ -n "$internal" ] && server=$internal

cat <<EOF
apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: ${server}
    certificate-authority-data: ${ca}
users:
- name: ${sa}
  user:
    token: ${token}
contexts:
- name: ${sa}
  context:
    cluster: cluster
    user: ${sa}
current-context: ${sa}
EOF
