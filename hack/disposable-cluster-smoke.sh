#!/usr/bin/env bash
# Own a dedicated cluster and kubeconfig; clean them even after a failed check.
set -euo pipefail
image=${1:?controller image required}
load_image=${2:-}
check_dir=$(mktemp -d)
cluster_suffix=${check_dir##*.}
cluster="arprt-check-${cluster_suffix,,}"
export KUBECONFIG="$check_dir/kubeconfig"
cleanup() {
  status=$?
  kind delete cluster --name "$cluster" --kubeconfig "$KUBECONFIG" || status=1
  rm -rf "$check_dir"
  return "$status"
}
trap cleanup EXIT
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" \
  --image "${KIND_NODE_IMAGE:?pinned kind node image required}" --wait 90s
if [[ "$load_image" == --load ]]; then
  kind load docker-image "$image" --name "$cluster"
fi
# This cluster belongs to this wrapper. Prepare shared CRDs before any suite
# starts so no suite owns/deletes them while another namespace is using them.
kubectl create --validate=strict -k config/crd
kubectl wait --for=condition=Established --timeout=30s -f config/crd/bases
python3 hack/run-cluster-smoke.py --image "$image" --jobs 2
