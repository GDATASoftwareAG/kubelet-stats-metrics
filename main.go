package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"gopkg.in/alecthomas/kingpin.v2"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	statsapi "k8s.io/kubelet/pkg/apis/stats/v1alpha1"
)

var Version = "unknown"

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

func getMetrics(interval time.Duration, cs *kubernetes.Clientset) {
	opsQueued := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubelet_stats_ephemeral_storage_pod_usage",
		Help: "Used to expose Ephemeral Storage metrics for pod ",
	},
		[]string{
			// name of pod for Ephemeral Storage
			"pod_name",
			"pod_namespace",
			// Name of Node where pod is placed.
			"node_name",
		},
	)
	opsRootfsQueued := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubelet_stats_rootfs_pod_container_usage",
		Help: "Used to expose rootfs metrics for containers ",
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

	prometheus.MustRegister(opsQueued, opsRootfsQueued)

	log.Debug("getMetrics has been invoked")

	for {
		nodes, err := cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			log.Fatalf("ErrorBadRequst : %s", err.Error())
		}
		for i := range nodes.Items {
			currentNode := nodes.Items[i].Name
			scrapeSingleNode(cs, currentNode, opsQueued, opsRootfsQueued)
		}
		time.Sleep(interval)
	}
}

func scrapeSingleNode(
	cs *kubernetes.Clientset,
	currentNode string,
	opsQueued *prometheus.GaugeVec,
	opsRootfsQueued *prometheus.GaugeVec,
) {
	content, err := cs.RESTClient().Get().AbsPath(fmt.Sprintf("/api/v1/nodes/%s/proxy/stats/summary", currentNode)).DoRaw(context.Background())
	if err != nil {
		log.Fatalf("ErrorBadRequst : %s", err.Error())
	}
	log.Debugf("Fetched proxy stats from node : %s", currentNode)
	var summary statsapi.Summary
	_ = json.Unmarshal(content, &summary)

	nodeName := summary.Node.NodeName
	for i := range summary.Pods {
		pod := &summary.Pods[i]
		podName := pod.PodRef.Name
		podNamespace := pod.PodRef.Namespace
		if pod.EphemeralStorage != nil {
			usedBytes := float64(*pod.EphemeralStorage.UsedBytes)
			opsQueued.With(prometheus.Labels{
				"pod_name":      podName,
				"pod_namespace": podNamespace,
				"node_name":     nodeName,
			}).Set(usedBytes)
		}

		for k := range pod.Containers {
			container := &pod.Containers[k]
			containerName := container.Name
			if container.Rootfs != nil {
				rootUsedBytes := float64(*container.Rootfs.UsedBytes)
				opsRootfsQueued.With(prometheus.Labels{
					"pod_name":       podName,
					"pod_namespace":  podNamespace,
					"container_name": containerName,
					"node_name":      nodeName,
				}).Set(rootUsedBytes)
			}
		}
	}
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

	cs := setupClient(cfg)
	go getMetrics(cfg.interval, cs)
	http.Handle("/metrics", promhttp.Handler())

	if err := http.ListenAndServe(fmt.Sprintf(":%d", cfg.metricsPort), nil); err != nil {
		log.Fatalf("Listener Falied : %s\n", err.Error())
	}
}
