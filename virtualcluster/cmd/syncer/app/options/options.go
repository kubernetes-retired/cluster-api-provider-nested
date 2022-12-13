/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package options

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"k8s.io/utils/pointer"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	cliflag "k8s.io/component-base/cli/flag"
	componentbaseconfig "k8s.io/component-base/config"
	"k8s.io/klog/v2"

	syncerappconfig "sigs.k8s.io/cluster-api-provider-nested/virtualcluster/cmd/syncer/app/config"
	"sigs.k8s.io/cluster-api-provider-nested/virtualcluster/pkg/apis"
	vcclient "sigs.k8s.io/cluster-api-provider-nested/virtualcluster/pkg/client/clientset/versioned"
	vcinformers "sigs.k8s.io/cluster-api-provider-nested/virtualcluster/pkg/client/informers/externalversions"
	syncerconfig "sigs.k8s.io/cluster-api-provider-nested/virtualcluster/pkg/syncer/apis/config"
	"sigs.k8s.io/cluster-api-provider-nested/virtualcluster/pkg/syncer/util/featuregate"
	"sigs.k8s.io/cluster-api-provider-nested/virtualcluster/pkg/util/constants"
)

// ResourceSyncerOptions is the main context object for the resource syncer.
type ResourceSyncerOptions struct {
	// The syncer configuration.
	ComponentConfig syncerconfig.SyncerConfiguration

	MetaClusterAddress string
	// MetaClusterClientConnection specifies the kubeconfig file and client connection
	// settings for the proxy server to use when communicating with the meta cluster apiserver.
	MetaClusterClientConnection componentbaseconfig.ClientConnectionConfiguration

	DeployOnMetaCluster bool
	SuperClusterAddress string
	SyncerName          string
	Address             string
	Port                string
	CertFile            string
	KeyFile             string
	DNSOptions          map[string]string
}

// NewResourceSyncerOptions creates a new resource syncer with a default config.
func NewResourceSyncerOptions() (*ResourceSyncerOptions, error) {
	return &ResourceSyncerOptions{
		ComponentConfig: syncerconfig.SyncerConfiguration{
			LeaderElection: syncerconfig.SyncerLeaderElectionConfiguration{
				LeaderElectionConfiguration: componentbaseconfig.LeaderElectionConfiguration{
					LeaderElect:   true,
					LeaseDuration: metav1.Duration{Duration: 15 * time.Second},
					RenewDeadline: metav1.Duration{Duration: 10 * time.Second},
					RetryPeriod:   metav1.Duration{Duration: 2 * time.Second},
					ResourceLock:  resourcelock.ConfigMapsResourceLock,
				},
				LockObjectName: "syncer-leaderelection-lock",
			},
			ClientConnection:           componentbaseconfig.ClientConnectionConfiguration{},
			Timeout:                    "",
			DisableServiceAccountToken: true,
			DefaultOpaqueMetaDomains:   []string{"kubernetes.io", "k8s.io"},
			ExtraSyncingResources:      []string{},
			ExtraNodeLabels:            []string{},
			OpaqueTaintKeys:            []string{},
			VNAgentPort:                int32(10550),
			VNAgentNamespacedName:      "vc-manager/vn-agent",
			VNAgentLabelSelector:       "app=vn-agent",
			FeatureGates: map[string]bool{
				featuregate.SuperClusterPooling:        false,
				featuregate.SuperClusterServiceNetwork: false,
				featuregate.VNodeProviderService:       false,
			},
		},
		SyncerName: "vc",
		Address:    "",
		Port:       "80",
		CertFile:   "",
		KeyFile:    "",
		DNSOptions: map[string]string{
			"ndots": "5",
		},
	}, nil
}

func (o *ResourceSyncerOptions) Flags() cliflag.NamedFlagSets {
	fss := cliflag.NamedFlagSets{}

	fs := fss.FlagSet("server")
	fs.StringVar(&o.SuperClusterAddress, "super-master", o.SuperClusterAddress, "The address of the super cluster Kubernetes API server (overrides any value in super-master-kubeconfig).")
	fs.StringVar(&o.ComponentConfig.ClientConnection.Kubeconfig, "super-master-kubeconfig", o.ComponentConfig.ClientConnection.Kubeconfig, "Path to kubeconfig file with authorization and control plane location information.")
	fs.StringVar(&o.ComponentConfig.Timeout, "super-master-timeout", o.ComponentConfig.Timeout, "Timeout of the super cluster Kubernetes API server, Valid time units are 'ns', 'us' (or 'µs'), 'ms', 's', 'm', 'h'. (overrides any value in super-master-kubeconfig).")
	fs.StringVar(&o.MetaClusterAddress, "meta-cluster-address", o.MetaClusterAddress, "The address of the meta cluster Kubernetes API server (overrides any value in meta-cluster-kubeconfig).")
	fs.StringVar(&o.MetaClusterClientConnection.Kubeconfig, "meta-cluster-kubeconfig", o.MetaClusterClientConnection.Kubeconfig, "Path to kubeconfig file of the meta cluster. If it is not provided, the super cluster is used")
	fs.BoolVar(&o.DeployOnMetaCluster, "deployment-on-meta", o.DeployOnMetaCluster, "Whether vc-syncer deploy on meta cluster")
	fs.StringVar(&o.SyncerName, "syncer-name", o.SyncerName, "Syncer name (default vc).")
	fs.BoolVar(&o.ComponentConfig.DisableServiceAccountToken, "disable-service-account-token", o.ComponentConfig.DisableServiceAccountToken, "DisableServiceAccountToken indicates whether to disable super cluster service account tokens being auto generated and mounted in vc pods.")
	fs.BoolVar(&o.ComponentConfig.DisablePodServiceLinks, "disable-service-links", o.ComponentConfig.DisablePodServiceLinks, "DisablePodServiceLinks indicates whether to disable the `EnableServiceLinks` field in pPod spec.")
	fs.StringSliceVar(&o.ComponentConfig.DefaultOpaqueMetaDomains, "default-opaque-meta-domains", o.ComponentConfig.DefaultOpaqueMetaDomains, "DefaultOpaqueMetaDomains is the default opaque meta configuration for each Virtual Cluster.")
	fs.StringSliceVar(&o.ComponentConfig.ExtraSyncingResources, "extra-syncing-resources", o.ComponentConfig.ExtraSyncingResources, "ExtraSyncingResources defines additional resources that need to be synced for each Virtual Cluster. (priorityclass, ingress, crd)")
	fs.Var(cliflag.NewMapStringBool(&o.ComponentConfig.FeatureGates), "feature-gates", "A set of key=value pairs that describe feature gates for various features."+
		"Options are:\n"+strings.Join(featuregate.DefaultFeatureGate.KnownFeatures(), "\n"))
	fs.StringSliceVar(&o.ComponentConfig.ExtraNodeLabels, "extra-node-labels", o.ComponentConfig.ExtraNodeLabels, "ExtraNodeLabels defines additional node labels that need to be synced for each Virtual Cluster")
	fs.StringSliceVar(&o.ComponentConfig.OpaqueTaintKeys, "opaque-taint-keys", o.ComponentConfig.OpaqueTaintKeys, "OpaqueTaintKeys defines taint keys that need to be synced for each Virtual Cluster")
	fs.Int32Var(&o.ComponentConfig.VNAgentPort, "vn-agent-port", 10550, "Port the vn-agent listens on")
	fs.StringVar(&o.ComponentConfig.VNAgentNamespacedName, "vn-agent-namespace-name", "vc-manager/vn-agent", "Namespace/Name of the vn-agent running in cluster, used for VNodeProviderService")
	fs.Var(cliflag.NewMapStringString(&o.DNSOptions), "dns-options", "DNSOptions is the default DNS options attached to each pod")
	fs.StringVar(&o.ComponentConfig.VNAgentLabelSelector, "vn-agent-label-selector", "app=vn-agent", "Label key=value of the vn-agent running in cluster, used for VNodeProviderPodIP")

	serverFlags := fss.FlagSet("metricsServer")
	serverFlags.StringVar(&o.Address, "address", o.Address, "The server address.")
	serverFlags.StringVar(&o.Port, "port", o.Port, "The server port.")
	serverFlags.StringVar(&o.CertFile, "cert-file", o.CertFile, "CertFile is the file containing x509 Certificate for HTTPS.")
	serverFlags.StringVar(&o.KeyFile, "key-file", o.KeyFile, "KeyFile is the file containing x509 private key matching certFile.")

	BindFlags(&o.ComponentConfig.LeaderElection, fss.FlagSet("leader election"))

	return fss
}

// BindFlags binds the LeaderElectionConfiguration struct fields to a flagset
func BindFlags(l *syncerconfig.SyncerLeaderElectionConfiguration, fs *pflag.FlagSet) {
	fs.BoolVar(&l.LeaderElect, "leader-elect", l.LeaderElect, ""+
		"Start a leader election client and gain leadership before "+
		"executing the main loop. Enable this when running replicated "+
		"components for high availability.")
	fs.DurationVar(&l.LeaseDuration.Duration, "leader-elect-lease-duration", l.LeaseDuration.Duration, ""+
		"The duration that non-leader candidates will wait after observing a leadership "+
		"renewal until attempting to acquire leadership of a led but unrenewed leader "+
		"slot. This is effectively the maximum duration that a leader can be stopped "+
		"before it is replaced by another candidate. This is only applicable if leader "+
		"election is enabled.")
	fs.DurationVar(&l.RenewDeadline.Duration, "leader-elect-renew-deadline", l.RenewDeadline.Duration, ""+
		"The interval between attempts by the acting leader to renew a leadership slot "+
		"before it stops leading. This must be less than or equal to the lease duration. "+
		"This is only applicable if leader election is enabled.")
	fs.DurationVar(&l.RetryPeriod.Duration, "leader-elect-retry-period", l.RetryPeriod.Duration, ""+
		"The duration the clients should wait between attempting acquisition and renewal "+
		"of a leadership. This is only applicable if leader election is enabled.")
	fs.StringVar(&l.ResourceLock, "leader-elect-resource-lock", l.ResourceLock, ""+
		"The type of resource object that is used for locking during "+
		"leader election. Supported options are `endpoints` and `configmaps` (default).")
	fs.StringVar(&l.LockObjectNamespace, "lock-object-namespace", l.LockObjectNamespace, "DEPRECATED: define the namespace of the lock object.")
	fs.StringVar(&l.LockObjectName, "lock-object-name", l.LockObjectName, "DEPRECATED: define the name of the lock object.")
}

// Config return a syncer config object
func (o *ResourceSyncerOptions) Config() (*syncerappconfig.Config, error) {
	c := &syncerappconfig.Config{}
	c.ComponentConfig = o.ComponentConfig

	// Prepare kube clients
	var (
		metaRestConfig, superRestConfig *restclient.Config
		leaderElectionRestConfig        restclient.Config
		err                             error
	)
	superRestConfig, err = getClientConfig(c.ComponentConfig.ClientConnection, o.SuperClusterAddress, o.ComponentConfig.Timeout, !o.DeployOnMetaCluster)
	if err != nil {
		return nil, err
	}
	if o.DeployOnMetaCluster || o.MetaClusterClientConnection.Kubeconfig != "" {
		metaRestConfig, err = getClientConfig(o.MetaClusterClientConnection, o.MetaClusterAddress, o.ComponentConfig.Timeout, o.DeployOnMetaCluster)
		if err != nil {
			return nil, err
		}
	} else {
		metaRestConfig = superRestConfig
	}

	if o.DeployOnMetaCluster {
		leaderElectionRestConfig = *metaRestConfig
	} else {
		leaderElectionRestConfig = *superRestConfig
	}

	superClusterClient, err := clientset.NewForConfig(restclient.AddUserAgent(superRestConfig, constants.ResourceSyncerUserAgent))
	if err != nil {
		return nil, err
	}
	metaClusterClient, err := clientset.NewForConfig(restclient.AddUserAgent(metaRestConfig, constants.ResourceSyncerUserAgent))
	if err != nil {
		return nil, err
	}

	// using deployment side cluster for leader election for better stability
	leaderElectionRestConfig.Timeout = c.ComponentConfig.LeaderElection.RenewDeadline.Duration
	leaderElectionClient, err := clientset.NewForConfig(restclient.AddUserAgent(&leaderElectionRestConfig, constants.ResourceSyncerUserAgent+"-leader-election"))
	if err != nil {
		return nil, err
	}

	virtualClusterClient, err := vcclient.NewForConfig(metaRestConfig)
	if err != nil {
		return nil, err
	}

	// Prepare event clients.
	eventBroadcaster := record.NewBroadcaster()
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: constants.ResourceSyncerUserAgent})
	leaderElectionBroadcaster := record.NewBroadcaster()
	leaderElectionRecorder := leaderElectionBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: constants.ResourceSyncerUserAgent})

	// Set up leader election if enabled.
	var leaderElectionConfig *leaderelection.LeaderElectionConfig
	if c.ComponentConfig.LeaderElection.LeaderElect {
		leaderElectionConfig, err = makeLeaderElectionConfig(c.ComponentConfig.LeaderElection, leaderElectionClient, leaderElectionRecorder, o.SyncerName)
		if err != nil {
			return nil, err
		}
	}

	featuregate.DefaultFeatureGate, err = featuregate.NewFeatureGate(c.ComponentConfig.FeatureGates)
	if err != nil {
		return nil, err
	}

	// Setup Scheme for all resources
	if err := apis.AddToScheme(scheme.Scheme); err != nil {
		return nil, err
	}
	c.ComponentConfig.RestConfig = superRestConfig
	c.ComponentConfig.DNSOptions = dnsOptionsConvert(o.DNSOptions)
	c.VirtualClusterClient = virtualClusterClient
	c.VirtualClusterInformer = vcinformers.NewSharedInformerFactory(virtualClusterClient, 0).Tenancy().V1alpha1().VirtualClusters()
	c.MetaClusterClient = metaClusterClient
	c.SuperClusterClient = superClusterClient
	c.SuperClusterInformerFactory = informers.NewSharedInformerFactory(superClusterClient, 0)
	c.Broadcaster = eventBroadcaster
	c.Recorder = recorder
	c.LeaderElectionClient = leaderElectionClient
	c.LeaderElection = leaderElectionConfig

	c.Address = o.Address
	c.Port = o.Port
	c.CertFile = o.CertFile
	c.KeyFile = o.KeyFile

	return c, nil
}

// makeLeaderElectionConfig builds a leader election configuration. It will
// create a new resource lock associated with the configuration.
func makeLeaderElectionConfig(config syncerconfig.SyncerLeaderElectionConfiguration, client clientset.Interface, recorder record.EventRecorder, syncername string) (*leaderelection.LeaderElectionConfig, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("unable to get hostname: %v", err)
	}
	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id := hostname + "_" + string(uuid.NewUUID())

	if config.LockObjectNamespace == "" {
		var err error
		config.LockObjectNamespace, err = getInClusterNamespace()
		if err != nil {
			return nil, fmt.Errorf("unable to find leader election namespace: %v", err)
		}
	}
	config.LockObjectName = syncername + "-" + "syncer-leaderelection-lock"
	rl, err := resourcelock.New(config.ResourceLock,
		config.LockObjectNamespace,
		config.LockObjectName,
		client.CoreV1(),
		client.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		})
	if err != nil {
		return nil, fmt.Errorf("couldn't create resource lock: %v", err)
	}

	return &leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: config.LeaseDuration.Duration,
		RenewDeadline: config.RenewDeadline.Duration,
		RetryPeriod:   config.RetryPeriod.Duration,
		WatchDog:      leaderelection.NewLeaderHealthzAdaptor(time.Second * 20),
		Name:          constants.ResourceSyncerUserAgent,
	}, nil
}

func getInClusterNamespace() (string, error) {
	// Check whether the namespace file exists.
	// If not, we are not running in cluster so can't guess the namespace.
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if os.IsNotExist(err) {
		return "", fmt.Errorf("not running in-cluster, please specify LeaderElectionNamespace")
	} else if err != nil {
		return "", fmt.Errorf("error checking namespace file: %v", err)
	}

	// Load the namespace file and return its content
	namespace, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("error reading namespace file: %v", err)
	}
	return string(namespace), nil
}

// getClientConfig creates a Kubernetes client rest config from the given config and serverAddrOverride.
func getClientConfig(config componentbaseconfig.ClientConnectionConfiguration, serverAddrOverride, timeout string, inCluster bool) (*restclient.Config, error) {
	// This creates a client, first loading any specified kubeconfig
	// file, and then overriding the serverAddr flag, if non-empty.
	var (
		restConfig *restclient.Config
		err        error
	)
	if len(config.Kubeconfig) == 0 && len(serverAddrOverride) == 0 && inCluster {
		klog.Info("Neither kubeconfig file nor control plane URL was specified. Falling back to in-cluster config.")
		restConfig, err = restclient.InClusterConfig()
	} else {
		// This creates a client, first loading any specified kubeconfig
		// file, and then overriding the serverAddr flag, if non-empty.
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: config.Kubeconfig},
			&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: serverAddrOverride}}).ClientConfig()
	}

	if err != nil {
		return nil, err
	}

	// Allow Syncer CLI Flag timeout override
	if len(timeout) == 0 {
		if restConfig.Timeout == 0 {
			restConfig.Timeout = constants.DefaultRequestTimeout
		}
	} else {
		timeoutDuration, err := time.ParseDuration(timeout)
		if err != nil {
			return nil, err
		}

		restConfig.Timeout = timeoutDuration
	}

	restConfig.ContentConfig.ContentType = config.AcceptContentTypes
	restConfig.QPS = config.QPS
	if restConfig.QPS == 0 {
		restConfig.QPS = constants.DefaultSyncerClientQPS
	}
	restConfig.Burst = int(config.Burst)
	if restConfig.Burst == 0 {
		restConfig.Burst = constants.DefaultSyncerClientBurst
	}

	return restConfig, nil
}

func dnsOptionsConvert(dnsoptions map[string]string) []corev1.PodDNSConfigOption {
	podDNSOptions := []corev1.PodDNSConfigOption{}
	for k, v := range dnsoptions {
		podDNSOptions = append(podDNSOptions, corev1.PodDNSConfigOption{Name: k, Value: pointer.StringPtr(v)})
	}
	return podDNSOptions
}
