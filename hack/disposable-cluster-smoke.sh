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
make cluster-smoke IMAGE="$image"
