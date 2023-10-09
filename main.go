package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	_ "net/http/pprof"

	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	statsapi "k8s.io/kubelet/pkg/apis/stats/v1alpha1"
)

var (
	Version       = "unknown"
	podUsageBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubelet_stats_ephemeral_storage_pod_usage",
		Help: "Used to expose Ephemeral Storage metrics for pod",
	},
		[]string{
			// name of pod for Ephemeral Storage
			"pod_name",
			"pod_namespace",
			// Name of Node where pod is placed.
			"node_name",
		},
	)

	podCapacityBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubelet_stats_ephemeral_storage_pod_capacity",
		Help: "Used to expose Ephemeral Storage capacity for a pod",
	},
		[]string{
			// name of pod for Ephemeral Storage
			"pod_name",
			"pod_namespace",
			// Name of Node where pod is placed.
			"node_name",
		},
	)

	podAvailableBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubelet_stats_ephemeral_storage_pod_available",
		Help: "Used to expose Ephemeral Storage available for a pod",
	},
		[]string{
			// name of pod for Ephemeral Storage
			"pod_name",
			"pod_namespace",
			// Name of Node where pod is placed.
			"node_name",
		},
	)

	containerUsageBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubelet_stats_rootfs_pod_container_usage",
		Help: "Used to expose rootfs metrics for containers",
	},
		[]string{
			// name of pod for Ephemeral Storage
			"pod_name",
			"pod_namespace",
			"container_name",
			// Name of Node where pod is placed.
			"node_name",
		},
	)
)

type Config struct {
	logFormat   string
	logLevel    string
	kubeconfig  string
	metricsPort int32
	inCluster   bool
	interval    time.Duration
}

func setupClient(cfg Config) *kubernetes.Clientset {
	var (
		err    error
		config *rest.Config
	)
	if cfg.inCluster {
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Fatalf("Failed to get rest config for in cluster client %s", err.Error())
		}

	} else {
		if home := homedir.HomeDir(); home != "" && cfg.kubeconfig == "" {
			cfg.kubeconfig = filepath.Join(home, ".kube", "config")
		}
		flag.Parse()

		// use the current context in kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", cfg.kubeconfig)
		if err != nil {
			log.Fatalf("Failed to get rest config from kubeconfig %s", err.Error())
		}
	}
	// create the clientset
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to get client set from kubeconfig %s", err.Error())
	}
	return cs
}

func metricsLoop(interval time.Duration, cs *kubernetes.Clientset) {
	log.Debug("metricsLoop has been invoked")

	for {
		ctx := context.Background()
		err := singleRun(ctx, cs)
		if err != nil {
			log.Errorf("ErrorBadRequst : %s", err.Error())
		}
		time.Sleep(interval)
	}
}

func singleRun(ctx context.Context, cs *kubernetes.Clientset) error {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	podUsageBytes.Reset()
	podCapacityBytes.Reset()
	containerUsageBytes.Reset()
	podAvailableBytes.Reset()
	var pods []ephemeralStoragePodData
	for i := range nodes.Items {
		currentNode := nodes.Items[i].Name
		node, err := scrapeSingleNode(ctx, cs, currentNode)
		if err != nil {
			log.Errorf("ErrorBadRequst : %s", err.Error())
			continue
		}
		pods = append(pods, node...)
	}
	for i := range pods {
		pod := &pods[i]
		if pod.ephemeral {
			setGauge(podUsageBytes, prometheus.Labels{
				"pod_name":      pod.name,
				"pod_namespace": pod.namespace,
				"node_name":     pod.nodeName,
			}, pod.usedBytes)
			setGauge(podCapacityBytes, prometheus.Labels{
				"pod_name":      pod.name,
				"pod_namespace": pod.namespace,
				"node_name":     pod.nodeName,
			}, pod.capacityBytes)
			setGauge(podAvailableBytes, prometheus.Labels{
				"pod_name":      pod.name,
				"pod_namespace": pod.namespace,
				"node_name":     pod.nodeName,
			}, pod.availableBytes)
		}
		for k := range pod.containers {
			container := &pod.containers[k]
			setGauge(containerUsageBytes, prometheus.Labels{
				"pod_name":       pod.name,
				"pod_namespace":  pod.namespace,
				"node_name":      pod.nodeName,
				"container_name": container.name,
			}, container.usedBytes)
		}
	}
	return nil
}

func setGauge(gauge *prometheus.GaugeVec, labels prometheus.Labels, data uint64) {
	gauge.With(labels).Set(float64(data))
}

type ephemeralStorageContainerData struct {
	name      string
	usedBytes uint64
}

type ephemeralStoragePodData struct {
	name           string
	nodeName       string
	namespace      string
	ephemeral      bool
	usedBytes      uint64
	capacityBytes  uint64
	availableBytes uint64
	containers     []ephemeralStorageContainerData
}

func scrapeSingleNode(
	ctx context.Context,
	cs *kubernetes.Clientset,
	currentNode string,
) ([]ephemeralStoragePodData, error) {
	content, err := cs.RESTClient().Get().AbsPath(fmt.Sprintf("/api/v1/nodes/%s/proxy/stats/summary", currentNode)).DoRaw(ctx)
	if err != nil {
		return []ephemeralStoragePodData{}, err
	}
	log.Debugf("Fetched proxy stats from node : %s", currentNode)
	var summary statsapi.Summary
	if err = json.Unmarshal(content, &summary); err != nil {
		return []ephemeralStoragePodData{}, err
	}
	var pods []ephemeralStoragePodData
	nodeName := summary.Node.NodeName
	for i := range summary.Pods {
		pod := &summary.Pods[i]
		pods = append(pods, extractSinglePodData(pod, nodeName))
	}
	return pods, nil
}

func extractSinglePodData(pod *statsapi.PodStats, nodeName string) ephemeralStoragePodData {
	var podData = ephemeralStoragePodData{}
	podData.name = pod.PodRef.Name
	podData.namespace = pod.PodRef.Namespace
	podData.nodeName = nodeName
	if pod.EphemeralStorage != nil {
		podData.ephemeral = true
		podData.usedBytes = *pod.EphemeralStorage.UsedBytes
		podData.capacityBytes = *pod.EphemeralStorage.CapacityBytes
		podData.availableBytes = *pod.EphemeralStorage.AvailableBytes
	}

	for k := range pod.Containers {
		container := &pod.Containers[k]
		if container.Rootfs != nil {
			podData.containers = append(podData.containers, ephemeralStorageContainerData{
				name:      container.Name,
				usedBytes: *container.Rootfs.UsedBytes,
			})
		}
	}
	return podData
}

// allLogLevelsAsStrings returns all logrus levels as a list of strings
func allLogLevelsAsStrings() []string {
	var levels []string
	for _, level := range log.AllLevels {
		levels = append(levels, level.String())
	}
	return levels
}

func (cfg *Config) ParseFlags(args []string) error {
	app := kingpin.New("kubelet-stats-metrics", "extract metrics from kubelet summary")
	app.Version(Version)
	app.DefaultEnvars()
	app.Flag("log-format", "The format in which log messages are printed (default: text, options: text, json)").
		Default("text").EnumVar(&cfg.logFormat, "text", "json")
	app.Flag("log-level", "Set the level of logging. (default: info, options: panic, debug, info, warning, error, fatal").
		Default("debug").EnumVar(&cfg.logLevel, allLogLevelsAsStrings()...)
	app.Flag("metrics-port", "Define metrics port (default: 9100)").Default("9100").Int32Var(&cfg.metricsPort)
	app.Flag("in-cluster", "use in cluster selection (default: true)").Default("true").BoolVar(&cfg.inCluster)
	app.Flag("kubeconfig", "Allows to define a path to a kubeconfig (default: \"\")").
		Default("").StringVar(&cfg.kubeconfig)
	app.Flag("interval", "Defines scrape interval (default: 30s)").
		Default("30s").DurationVar(&cfg.interval)

	_, err := app.Parse(args)
	if err != nil {
		return err
	}

	log.Infof("config: %s", cfg)

	if cfg.logFormat == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	}

	ll, err := log.ParseLevel(cfg.logLevel)
	if err != nil {
		log.Fatalf("failed to parse log level: %v", err)
	}
	log.SetLevel(ll)

	return nil
}

func main() {
	cfg := Config{}
	if err := cfg.ParseFlags(os.Args[1:]); err != nil {
		log.Fatalf("flag parsing error: %v", err)
	}

	prometheus.MustRegister(podUsageBytes, podCapacityBytes, podAvailableBytes, containerUsageBytes)
	cs := setupClient(cfg)
	go metricsLoop(cfg.interval, cs)
	http.Handle("/metrics", promhttp.Handler())

	if err := http.ListenAndServe(fmt.Sprintf(":%d", cfg.metricsPort), nil); err != nil {
		log.Fatalf("Listener Falied : %s\n", err.Error())
	}
}
