# Kubelet stats metrics.

Project based on https://github.com/jmcgrath207/k8s-ephemeral-storage-metrics

## Exported metrics

* kubelet_stats_ephemeral_storage_pod_usage (pod_namespace, pod_name, node_name)
* kubelet_stats_ephemeral_storage_pod_capacity (pod_namespace, pod_name, node_name)
* kubelet_stats_ephemeral_storage_pod_available (pod_namespace, pod_name, node_name)
* kubelet_stats_rootfs_pod_container_usage (pod_namespace, pod_name, container_name, node_name)
