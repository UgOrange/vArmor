// Copyright 2021-2023 vArmor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	rest "k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
)

var (
	// Namespace is the vArmor namespace
	Namespace = GetNamespace()

	// ManagerName is the deployment name of vArmor manager
	ManagerName = "varmor-manager"

	// AgentName is the daemonset name of vArmor agent
	AgentName = "varmor-agent"

	// MLName is the deployment name of vArmor classifier
	MLName = "varmor-classifier"

	// StatusServiceName is the name of ML classification service
	MLServiceName = "varmor-classifier-svc"

	// MLServicePort is the port of ML classification service
	MLServicePort = 5000

	// MLPathClassifyPath is the path for classifing path
	MLPathClassifyPath = "/api/v1/path"

	// StatusServiceName is the name of status service
	StatusServiceName = "varmor-status-svc"

	// StatusServicePort is the port of status service
	StatusServicePort = 8080

	// StatusSyncPath is the path for syncing status
	StatusSyncPath = "/api/v1/status"

	// DataSyncPath is the path for syncing data
	DataSyncPath = "/api/v1/data"

	// WebhookServiceName is the name of webhook service
	WebhookServiceName = "varmor-webhook-svc"

	// WebhookServicePort is the port of webhook service
	WebhookServicePort = 3443

	// CertRenewalInterval is the renewal interval for rootCA
	CertRenewalInterval time.Duration = 12 * time.Hour

	// CertValidityDuration is the valid duration for a new cert
	CertValidityDuration time.Duration = 365 * 24 * time.Hour

	// CertCommonName is the Common Name of CA cert
	CertCommonName = "*.varmor.svc"

	// MutatingWebhookConfigurationName default resource mutating webhook configuration name
	MutatingWebhookConfigurationName = "varmor-resource-mutating-webhook-cfg"

	// MutatingWebhookConfigurationDebugName default resource mutating webhook configuration name for debug mode
	MutatingWebhookConfigurationDebugName = "varmor-resource-mutating-webhook-cfg-debug"

	// MutatingWorkloadWebhookName is the name of workload resource mutating webhook
	MutatingWorkloadWebhookName = "mutateworkload.varmor.org"

	// MutatingWorkloadWebhookName is the name of pod resource mutating webhook
	MutatingPodWebhookName = "mutatepod.varmor.org"

	// MutatingWebhookServicePath is the path for mutation webhook
	MutatingWebhookServicePath = "/mutate"

	// WebhookTimeout specifies the timeout seconds for the mutation webhook
	WebhookTimeout = 10

	// LivenessServicePath is the path for check liveness health
	LivenessServicePath = "/health/liveness"

	// ReadinessServicePath is the path for check readness health
	ReadinessServicePath = "/health/readiness"

	// PackagedAppArmorProfiles include the AppArmor feature ABI, abstractions, tunables and default profiles that come from the development environment and upstream
	PackagedAppArmorProfiles = "/varmor/apparmor.d"

	// AppArmorProfileDir is the path of AppArmor profiles for agent
	AppArmorProfileDir = "/etc/apparmor.d"

	// SeccompProfileDir is the path of Seccomp profiles in the host
	SeccompProfileDir = "/var/lib/kubelet/seccomp"

	// WebhookSelectorLabel is used for matching the admission requests
	WebhookSelectorLabel = map[string]string{}

	// OmuxSocketPath is used for recieving the audit logs of AppArmor from rsyslog
	OmuxSocketPath = "/var/run/varmor/audit/omuxsock.sock"
)

// CreateClientConfig creates client config and applies rate limit QPS and burst
func CreateClientConfig(kubeconfig string, qps float64, burst int, log logr.Logger) (*rest.Config, error) {
	logger := log.WithName("CreateClientConfig")

	clientConfig, err := createClientConfig(kubeconfig, logger)
	if err != nil {
		return nil, err
	}

	if qps > math.MaxFloat32 {
		return nil, fmt.Errorf("client rate limit QPS must not be higher than %e", math.MaxFloat32)
	}
	clientConfig.Burst = burst
	clientConfig.QPS = float32(qps)

	return clientConfig, nil
}

// createClientConfig creates client config
func createClientConfig(kubeconfig string, log logr.Logger) (*rest.Config, error) {
	if kubeconfig == "" {
		log.Info("Using in-cluster configuration")
		return rest.InClusterConfig()
	}
	log.Info("Using specified kubeconfig", "kubeconfig", kubeconfig)
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func GetNamespace() string {
	content, err := os.ReadFile("/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "varmor"
	}
	return strings.Trim(string(content), "\n")
}
