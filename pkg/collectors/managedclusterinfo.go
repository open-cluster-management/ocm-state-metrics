// Copyright (c) 2020 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package collectors

import (
	"context"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kube-state-metrics/pkg/metric"

	mcv1 "github.com/open-cluster-management/api/cluster/v1"
	mciv1beta1 "github.com/open-cluster-management/multicloud-operators-foundation/pkg/apis/internal.open-cluster-management.io/v1beta1"
	"k8s.io/klog/v2"
)

const (
	createdViaHive  = "Hive"
	createdViaOther = "Other"

	workerLabel = "node-role.kubernetes.io/worker"

	resourceSocket       mcv1.ResourceName = "socket"
	resourceCore         mcv1.ResourceName = "core"
	resourceCoreWorker   mcv1.ResourceName = "core_worker"
	resourceSocketWorker mcv1.ResourceName = "socket_worker"
	resourceCPUWorker    mcv1.ResourceName = "cpu_worker"
)

var (
	descClusterInfoName          = "acm_managed_cluster_info"
	descClusterInfoHelp          = "Managed cluster information"
	descClusterInfoDefaultLabels = []string{"hub_cluster_id",
		"managed_cluster_id",
		"vendor",
		"cloud",
		"version",
		"created_via",
		"cpu",
		"cpu_worker",
		"core",
		"core_worker",
		"socket",
		"socket_worker"}

	cdGVR = schema.GroupVersionResource{
		Group:    "hive.openshift.io",
		Version:  "v1",
		Resource: "clusterdeployments",
	}

	cvGVR = schema.GroupVersionResource{
		Group:    "config.openshift.io",
		Version:  "v1",
		Resource: "clusterversions",
	}

	mciGVR = schema.GroupVersionResource{
		Group:    "internal.open-cluster-management.io",
		Version:  "v1beta1",
		Resource: "managedclusterinfos",
	}

	mcGVR = schema.GroupVersionResource{
		Group:    "cluster.open-cluster-management.io",
		Version:  "v1",
		Resource: "managedclusters",
	}
)

func getManagedClusterInfoMetricFamilies(hubClusterID string, client dynamic.Interface) []metric.FamilyGenerator {
	return []metric.FamilyGenerator{
		{
			Name: descClusterInfoName,
			Type: metric.Gauge,
			Help: descClusterInfoHelp,
			GenerateFunc: wrapManagedClusterInfoFunc(func(obj *unstructured.Unstructured) metric.Family {
				klog.Infof("Wrap %s", obj.GetName())
				mciU, errMCI := client.Resource(mciGVR).Namespace(obj.GetName()).Get(context.TODO(), obj.GetName(), metav1.GetOptions{})
				if errMCI != nil {
					klog.Errorf("Error: %v", errMCI)
					return metric.Family{Metrics: []*metric.Metric{}}
				}
				mci := &mciv1beta1.ManagedClusterInfo{}
				err := runtime.DefaultUnstructuredConverter.FromUnstructured(mciU.UnstructuredContent(), &mci)
				if err != nil {
					klog.Errorf("Error: %v", err)
					return metric.Family{Metrics: []*metric.Metric{}}
				}
				mcU, errMC := client.Resource(mcGVR).Get(context.TODO(), mci.GetName(), metav1.GetOptions{})
				if errMC != nil {
					klog.Errorf("Error: %v", errMC)
					return metric.Family{Metrics: []*metric.Metric{}}
				}
				klog.Infof("mcU: %v", mcU)
				mc := &mcv1.ManagedCluster{}
				err = runtime.DefaultUnstructuredConverter.FromUnstructured(mcU.UnstructuredContent(), &mc)
				if err != nil {
					klog.Errorf("Error: %v", err)
					return metric.Family{Metrics: []*metric.Metric{}}
				}
				// klog.Infof("mc: %v", mc)
				createdVia := createdViaHive
				cd, errCD := client.Resource(cdGVR).Namespace(mci.GetName()).Get(context.TODO(), mci.GetName(), metav1.GetOptions{})
				if errCD != nil {
					createdVia = createdViaOther
					klog.Infof("Cluster Deployment %s not found, err: %s", mci.GetName(), errCD)
				} else {
					klog.Infof("Cluster Deployment: %v,", cd.Object)
				}
				clusterID := mci.Status.ClusterID
				if clusterID == "" && mci.Status.KubeVendor != mciv1beta1.KubeVendorOpenShift {
					clusterID = mci.GetName()
				}
				version := getVersion(mci)
				cpu, cpu_worker, core, core_worker, socket, socket_worker := getCapacity(mc)

				if clusterID == "" ||
					mci.Status.KubeVendor == "" ||
					mci.Status.CloudVendor == "" ||
					version == "" ||
					cpu == 0 ||
					(cpu_worker == 0 && hasWorker(mci)) {
					klog.Infof("Not enough information available for %s", mci.GetName())
					klog.Infof(`\tClusterID=%s,
KubeVendor=%s,
CloudVendor=%s,
Version=%s,
cpu=%d,
cpu_worker=%d,
core=%d,
core_worker=%d,
socket=%d,
socket_worker=%d`,
						clusterID,
						mci.Status.KubeVendor,
						mci.Status.CloudVendor,
						version,
						cpu,
						cpu_worker,
						core,
						core_worker,
						socket,
						socket_worker)
					return metric.Family{Metrics: []*metric.Metric{}}
				}
				labelsValues := []string{hubClusterID,
					clusterID,
					string(mci.Status.KubeVendor),
					string(mci.Status.CloudVendor),
					version,
					createdVia,
					strconv.FormatInt(cpu, 10),
					strconv.FormatInt(cpu_worker, 10),
					strconv.FormatInt(core, 10),
					strconv.FormatInt(core_worker, 10),
					strconv.FormatInt(socket, 10),
					strconv.FormatInt(socket_worker, 10),
				}

				f := metric.Family{Metrics: []*metric.Metric{
					{
						LabelKeys:   descClusterInfoDefaultLabels,
						LabelValues: labelsValues,
						Value:       1,
					},
				}}
				klog.Infof("Returning %v", string(f.ByteSlice()))
				return f
			}),
		},
	}
}

func getVersion(mci *mciv1beta1.ManagedClusterInfo) string {
	if mci.Status.KubeVendor == "" {
		return ""
	}
	switch mci.Status.KubeVendor {
	case mciv1beta1.KubeVendorOpenShift:
		return mci.Status.DistributionInfo.OCP.Version
	default:
		return mci.Status.Version
	}

}

//Get only the worker size
// func getWorkerCPU(mci *mciv1beta1.ManagedClusterInfo) (vcpu int64) {
// 	for _, n := range mci.Status.NodeList {
// 		if _, ok := n.Labels[workerLabel]; ok {
// 			if q, ok := n.Capacity[mciv1beta1.ResourceCPU]; ok {
// 				vcpu += q.Value()
// 			}
// 		}
// 	}
// 	return
// }

func hasWorker(mci *mciv1beta1.ManagedClusterInfo) bool {
	for _, n := range mci.Status.NodeList {
		if _, ok := n.Labels[workerLabel]; ok {
			return true
		}
	}
	return false
}

func getCapacity(mc *mcv1.ManagedCluster) (cpu, cpu_worker, core, core_worker, socket, socket_worker int64) {
	if q, ok := mc.Status.Capacity[mcv1.ResourceCPU]; ok {
		cpu = q.Value()
	}
	if q, ok := mc.Status.Capacity[resourceCPUWorker]; ok {
		cpu_worker = q.Value()
	}
	if q, ok := mc.Status.Capacity[resourceCore]; ok {
		core = q.Value()
	}
	if q, ok := mc.Status.Capacity[resourceCoreWorker]; ok {
		core_worker = q.Value()
	}
	if q, ok := mc.Status.Capacity[resourceSocket]; ok {
		socket = q.Value()
	}
	if q, ok := mc.Status.Capacity[resourceSocketWorker]; ok {
		socket_worker = q.Value()
	}
	return
}

func wrapManagedClusterInfoFunc(f func(*unstructured.Unstructured) metric.Family) func(interface{}) *metric.Family {
	return func(obj interface{}) *metric.Family {
		Cluster := obj.(*unstructured.Unstructured)

		metricFamily := f(Cluster)

		for _, m := range metricFamily.Metrics {
			m.LabelKeys = append([]string{}, m.LabelKeys...)
			m.LabelValues = append([]string{}, m.LabelValues...)
		}

		return &metricFamily
	}
}

func createManagedClusterInfoListWatchWithClient(client dynamic.Interface, ns string) cache.ListWatch {
	return cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			return client.Resource(mciGVR).Namespace(ns).List(context.TODO(), opts)
		},
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			return client.Resource(mciGVR).Namespace(ns).Watch(context.TODO(), opts)
		},
	}
}

func createManagedClusterListWatchWithClient(client dynamic.Interface) cache.ListWatch {
	return cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			return client.Resource(mcGVR).List(context.TODO(), opts)
		},
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			return client.Resource(mcGVR).Watch(context.TODO(), opts)
		},
	}
}
