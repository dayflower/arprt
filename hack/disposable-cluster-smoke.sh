#!/usr/bin/env bash
# Own a dedicated cluster and kubeconfig; clean them even after a failed check.
set -euo pipefail
image=${1:?controller image required}
load_image=${2:-}
helm=${ARPRT_HELM:?pinned Helm client required}
packages=${ARPRT_CHART_PACKAGES:?chart packages required}
check_dir=$(mktemp -d)
cluster_suffix=${check_dir##*.}
cluster="arprt-check-${cluster_suffix,,}"
export KUBECONFIG="$check_dir/kubeconfig"
# Ignore operator Helm connection/config overrides for this owned cluster.
for helm_variable in ${!HELM_@}; do unset "$helm_variable"; done
export HELM_CONFIG_HOME="$check_dir/helm/config" HELM_CACHE_HOME="$check_dir/helm/cache"
export HELM_DATA_HOME="$check_dir/helm/data" HELM_REGISTRY_CONFIG="$check_dir/registry.json" HELM_DRIVER=secret
cleanup() {
  status=$?
  kind delete cluster --name "$cluster" --kubeconfig "$KUBECONFIG" || status=1
  rm -rf "$check_dir"
  return "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" \
  --image "${KIND_NODE_IMAGE:?pinned kind node image required}" --wait 90s
if [[ "$load_image" == --load ]]; then
  kind load docker-image "$image" --name "$cluster"
fi
# This cluster belongs to this wrapper. Prepare shared CRDs before any suite
# starts so no suite owns/deletes them while another namespace is using them.
"$helm" install schemas "$packages"/arprt-crds-*.tgz --namespace smoke-schemas \
  --create-namespace --wait --timeout 60s
kubectl wait --for=condition=Established --timeout=30s -f config/crd/bases
python3 hack/run-cluster-smoke.py --image "$image" --helm "$helm" --packages "$packages" --jobs 2
