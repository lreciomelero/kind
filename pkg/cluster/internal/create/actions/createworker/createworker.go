/*
Copyright 2019 The Kubernetes Authors.

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

// Package createworker implements the create worker action
package createworker

import (
	"bytes"
	"context"
	_ "embed"
	"os"
	"regexp"
	"strings"

	"sigs.k8s.io/kind/pkg/cluster/internal/create/actions"
	"sigs.k8s.io/kind/pkg/commons"
	"sigs.k8s.io/kind/pkg/errors"
	"sigs.k8s.io/kind/pkg/exec"
)

type action struct {
	vaultPassword      string
	descriptorPath     string
	moveManagement     bool
	avoidCreation      bool
	keosCluster        commons.KeosCluster
	clusterCredentials commons.ClusterCredentials
	clusterConfig      *commons.ClusterConfig
}

type KeosRegistry struct {
	url          string
	user         string
	pass         string
	registryType string
}

type HelmRegistry struct {
	URL  string
	User string
	Pass string
	Type string
}

type CMHelmRelease struct {
	CMName      string
	CMNamespace string
	CMValue     string
}

const (
	kubeconfigPath           = "/kind/worker-cluster.kubeconfig"
	workKubeconfigPath       = ".kube/config"
	CAPILocalRepository      = "/root/.cluster-api/local-repository"
	cloudProviderBackupPath  = "/kind/backup/objects"
	localBackupPath          = "backup"
	manifestsPath            = "/kind/manifests"
	cniDefaultFile           = "/kind/manifests/default-cni.yaml"
	storageDefaultPath       = "/kind/manifests/default-storage.yaml"
	infraGCPVersion          = "v1.6.1"
	infraGKEVersion          = "v1.6.1"
	infraAWSVersion          = "v2.5.2"
	infraAzureVersion        = "v1.12.4"
	GKECoreDNSDeploymentPath = "/kind/manifests/coredns-deployment.yaml"
)

var PathsToBackupLocally = []string{
	cloudProviderBackupPath,
	"/kind/manifests",
}

var majorVersion = ""

//go:embed files/common/allow-all-egress_netpol.yaml
var allowCommonEgressNetPol string

//go:embed files/gcp/rbac-loadbalancing.yaml
var rbacInternalLoadBalancing string

//go:embed files/aws/aws-node_rbac.yaml
var rbacAWSNode string

// NewAction returns a new action for installing default CAPI
func NewAction(vaultPassword string, descriptorPath string, moveManagement bool, avoidCreation bool, keosCluster commons.KeosCluster, clusterCredentials commons.ClusterCredentials, clusterConfig *commons.ClusterConfig) actions.Action {
	return &action{
		vaultPassword:      vaultPassword,
		descriptorPath:     descriptorPath,
		moveManagement:     moveManagement,
		avoidCreation:      avoidCreation,
		keosCluster:        keosCluster,
		clusterCredentials: clusterCredentials,
		clusterConfig:      clusterConfig,
	}
}

// Execute runs the action
func (a *action) Execute(ctx *actions.ActionContext) error {
	var c string
	var err error
	var keosRegistry KeosRegistry
	var helmRegistry HelmRegistry
	majorVersion = strings.Split(a.keosCluster.Spec.K8SVersion, ".")[1]

	// Get the target node
	n, err := ctx.GetNode()
	if err != nil {
		return err
	}

	providerParams := ProviderParams{
		ClusterName:  a.keosCluster.Metadata.Name,
		Region:       a.keosCluster.Spec.Region,
		Managed:      a.keosCluster.Spec.ControlPlane.Managed,
		Credentials:  a.clusterCredentials.ProviderCredentials,
		GithubToken:  a.clusterCredentials.GithubToken,
		StorageClass: a.keosCluster.Spec.StorageClass,
	}

	providerBuilder := getBuilder(a.keosCluster.Spec.InfraProvider)
	infra := newInfra(providerBuilder)
	provider := infra.buildProvider(providerParams)

	ctx.Status.Start("Pulling initial Helm Charts 🧭")

	err = loginHelmRepo(n, a.keosCluster, a.clusterCredentials, &helmRegistry, infra, providerParams)
	if err != nil {
		return err
	}

	err = infra.pullProviderCharts(n, &a.clusterConfig.Spec, a.keosCluster.Spec, a.clusterCredentials)
	if err != nil {
		return err
	}

	ctx.Status.End(true)

	for _, registry := range a.keosCluster.Spec.DockerRegistries {
		if registry.KeosRegistry {
			keosRegistry.url = registry.URL
			keosRegistry.registryType = registry.Type
			continue
		}
	}

	if keosRegistry.registryType != "generic" {
		keosRegistry.user, keosRegistry.pass, err = infra.getRegistryCredentials(providerParams, keosRegistry.url)
		if err != nil {
			return errors.Wrap(err, "failed to get docker registry credentials")
		}
	} else {
		keosRegistry.user = a.clusterCredentials.KeosRegistryCredentials["User"]
		keosRegistry.pass = a.clusterCredentials.KeosRegistryCredentials["Pass"]
	}

	awsEKSEnabled := a.keosCluster.Spec.InfraProvider == "aws" && a.keosCluster.Spec.ControlPlane.Managed
	// azAKSEnabled := a.keosCluster.Spec.InfraProvider == "azure" && a.keosCluster.Spec.ControlPlane.Managed
	isMachinePool := a.keosCluster.Spec.InfraProvider != "aws" && a.keosCluster.Spec.ControlPlane.Managed
	gcpGKEEnabled := a.keosCluster.Spec.InfraProvider == "gcp" && a.keosCluster.Spec.ControlPlane.Managed

	var privateParams PrivateParams
	if a.clusterConfig != nil {
		privateParams = PrivateParams{
			KeosCluster: a.keosCluster,
			KeosRegUrl:  keosRegistry.url,
			Private:     a.clusterConfig.Spec.Private,
			HelmPrivate: a.clusterConfig.Spec.PrivateHelmRepo,
		}
	} else {
		privateParams = PrivateParams{
			KeosCluster: a.keosCluster,
			KeosRegUrl:  keosRegistry.url,
			Private:     false,
		}
	}

	if privateParams.Private {
		ctx.Status.Start("Installing Private CNI 🎖️")
		defer ctx.Status.End(false)

		c = `sed -i 's/@sha256:[[:alnum:]_-].*$//g' ` + cniDefaultFile
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return err
		}
		c = `sed -i 's|docker.io|` + keosRegistry.url + `|g' /kind/manifests/default-cni.yaml`

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return err
		}
		c = `sed -i 's/{{ .PodSubnet }}/10.244.0.0\/16/g' /kind/manifests/default-cni.yaml`

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return err
		}
		c = `cat /kind/manifests/default-cni.yaml | kubectl apply -f -`

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return err
		}
		ctx.Status.End(true)

		ctx.Status.Start("Deleting local storage plugin 🎖️")
		defer ctx.Status.End(false)
		c = `kubectl delete -f ` + storageDefaultPath + ` --force`

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return err
		}
		ctx.Status.End(true)

	}

	chartsList := infra.getProviderCharts(&a.clusterConfig.Spec, a.keosCluster.Spec)

	ctx.Status.Start("Installing CAPx 🎖️")
	defer ctx.Status.End(false)

	for _, registry := range a.keosCluster.Spec.DockerRegistries {
		if registry.KeosRegistry {
			keosRegistry.url = registry.URL
			keosRegistry.registryType = registry.Type
			continue
		}
	}

	if keosRegistry.registryType != "generic" {
		keosRegistry.user, keosRegistry.pass, err = infra.getRegistryCredentials(providerParams, keosRegistry.url)
		if err != nil {
			return errors.Wrap(err, "failed to get docker registry credentials")
		}
	} else {
		keosRegistry.user = a.clusterCredentials.KeosRegistryCredentials["User"]
		keosRegistry.pass = a.clusterCredentials.KeosRegistryCredentials["Pass"]
	}

	// Create docker-registry secret for keos cluster
	c = "kubectl -n kube-system create secret docker-registry regcred" +
		" --docker-server=" + strings.Split(keosRegistry.url, "/")[0] +
		" --docker-username=" + keosRegistry.user +
		" --docker-password=" + keosRegistry.pass

	_, err = commons.ExecuteCommand(n, c, 5, 3)
	if err != nil {
		return errors.Wrap(err, "failed to create docker-registry secret")
	}

	if provider.capxVersion != provider.capxImageVersion {

		infraComponents := CAPILocalRepository + "/infrastructure-" + provider.capxProvider + "/" + provider.capxVersion + "/infrastructure-components.yaml"

		// Create provider-system namespace
		c = "kubectl create namespace " + provider.capxName + "-system"

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to create "+provider.capxName+"-system namespace")
		}

		// Create docker-registry secret in provider-system namespace
		c = "kubectl create secret docker-registry regcred" +
			" --docker-server=" + strings.Split(keosRegistry.url, "/")[0] +
			" --docker-username=" + keosRegistry.user +
			" --docker-password=" + keosRegistry.pass +
			" --namespace=" + provider.capxName + "-system"

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to create docker-registry secret")
		}

		// Add imagePullSecrets to infrastructure-components.yaml
		c = "sed -i '/containers:/i\\      imagePullSecrets:\\n      - name: regcred' " + infraComponents

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to add imagePullSecrets to infrastructure-components.yaml")
		}
	}

	certManagerVersion := getChartVersion(a.clusterConfig.Spec.Charts, "cert-manager")
	if certManagerVersion == "" {
		return errors.New("Cert manager helm chart version cannot be found ")
	}
	err = provider.deployCertManager(n, keosRegistry.url, "", privateParams, make(map[string]commons.ChartEntry))
	if err != nil {
		return err
	}

	c = "echo \"cert-manager:\" >> /root/.cluster-api/clusterctl.yaml && " +
		"echo \"  version: " + certManagerVersion + "\" >> /root/.cluster-api/clusterctl.yaml "

	_, err = commons.ExecuteCommand(n, c, 5, 3)
	if err != nil {
		return errors.Wrap(err, "failed to set cert-manager version in clusterctl config")
	}

	if privateParams.Private {

		gcpVersion := infraGCPVersion
		if gcpGKEEnabled {
			gcpVersion = provider.capxImageVersion
		}

		c = "echo \"images:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  cluster-api:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cluster-api\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  bootstrap-kubeadm:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cluster-api\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  control-plane-kubeadm:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cluster-api\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  infrastructure-aws:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cluster-api-aws\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    tag: " + infraAWSVersion + "\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  infrastructure-gcp:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cluster-api-gcp\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    tag: " + gcpVersion + "\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  infrastructure-azure:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cluster-api-azure\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  cert-manager:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cert-manager\" >> /root/.cluster-api/clusterctl.yaml "
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to add private image registry clusterctl config")
		}

		c = `sed -i 's/@sha256:[[:alnum:]_-].*$//g' /root/.cluster-api/local-repository/infrastructure-gcp/` + infraGCPVersion + `/infrastructure-components.yaml`
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return err
		}
	} else if gcpGKEEnabled {
		c = "echo \"images:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"  infrastructure-gcp:\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    repository: " + keosRegistry.url + "/cluster-api-gcp\" >> /root/.cluster-api/clusterctl.yaml && " +
			"echo \"    tag: " + provider.capxImageVersion + "\" >> /root/.cluster-api/clusterctl.yaml "

		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to overwrite image registry clusterctl config")
		}
	}

	err = provider.installCAPXLocal(n)
	if err != nil {
		return err
	}

	capiClustersNamespace := "cluster-" + a.keosCluster.Metadata.Name

	ctx.Status.End(true) // End Installing CAPx

	ctx.Status.Start("Generating secrets file 📝🗝️")
	defer ctx.Status.End(false)

	err = commons.EnsureSecretsFile(a.keosCluster.Spec, a.vaultPassword, a.clusterCredentials)
	if err != nil {
		return errors.Wrap(err, "failed to ensure the secrets file")
	}

	err = commons.RewriteDescriptorFile(a.descriptorPath)
	if err != nil {
		return errors.Wrap(err, "failed to rewrite the descriptor file")
	}

	defer ctx.Status.End(true) // End Generating secrets file

	// Create namespace for CAPI clusters (it must exists)
	c = "kubectl create ns " + capiClustersNamespace

	_, err = commons.ExecuteCommand(n, c, 5, 3)
	if err != nil {
		return errors.Wrap(err, "failed to create cluster's Namespace")
	}

	// Create the allow-all-egress network policy file in the container
	allowCommonEgressNetPolPath := "/kind/allow-all-egress_netpol.yaml"
	c = "echo \"" + allowCommonEgressNetPol + "\" > " + allowCommonEgressNetPolPath

	_, err = commons.ExecuteCommand(n, c, 5, 3)
	if err != nil {
		return errors.Wrap(err, "failed to write the allow-all-egress network policy")
	}

	ctx.Status.Start("Installing keos cluster operator 💻")
	defer ctx.Status.End(false)

	err = provider.deployClusterOperator(n, privateParams, a.clusterCredentials, keosRegistry, a.clusterConfig, "", true, helmRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to deploy cluster operator")
	}

	defer ctx.Status.End(true) // End installing keos cluster operator

	if !a.avoidCreation {
		if a.keosCluster.Spec.InfraProvider == "aws" && a.keosCluster.Spec.Security.AWS.CreateIAM {
			ctx.Status.Start("[CAPA] Ensuring IAM security 👮")
			defer ctx.Status.End(false)

			err = createCloudFormationStack(n, provider.capxEnvVars)
			if err != nil {
				return errors.Wrap(err, "failed to create the IAM security")
			}
			ctx.Status.End(true)
		}

		ctx.Status.Start("Creating the workload cluster 💥")
		defer ctx.Status.End(false)

		if a.clusterConfig != nil {
			// Apply cluster manifests
			c = "kubectl apply -f " + manifestsPath + "/clusterconfig.yaml"
			_, err = commons.ExecuteCommand(n, c, 5, 5)
			if err != nil {
				return errors.Wrap(err, "failed to apply clusterconfig manifests")
			}
		}

		// Apply cluster manifests
		c = "kubectl apply -f " + manifestsPath + "/keoscluster.yaml"
		_, err = commons.ExecuteCommand(n, c, 10, 5)
		if err != nil {
			return errors.Wrap(err, "failed to apply keoscluster manifests")
		}

		c = "kubectl -n " + capiClustersNamespace + " get cluster " + a.keosCluster.Metadata.Name
		_, err = commons.ExecuteCommand(n, c, 25, 5)
		if err != nil {
			return errors.Wrap(err, "failed to wait for cluster")
		}

		// Wait for the control plane initialization
		c = "kubectl -n " + capiClustersNamespace + " wait --for=condition=ControlPlaneInitialized --timeout=25m cluster " + a.keosCluster.Metadata.Name
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to create the workload cluster")
		}

		ctx.Status.End(true) // End Creating the workload cluster

		ctx.Status.Start("Saving the workload cluster kubeconfig 📝")
		defer ctx.Status.End(false)

		// Get the workload cluster kubeconfig
		c = "clusterctl -n " + capiClustersNamespace + " get kubeconfig " + a.keosCluster.Metadata.Name + " | tee " + kubeconfigPath
		kubeconfig, err := commons.ExecuteCommand(n, c, 5, 3)
		if err != nil || kubeconfig == "" {
			return errors.Wrap(err, "failed to get workload cluster kubeconfig")
		}

		// Create worker-kubeconfig secret for keos cluster
		c = "kubectl -n " + capiClustersNamespace + " create secret generic worker-kubeconfig --from-file " + kubeconfigPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to create worker-kubeconfig secret")
		}

		workKubeconfigBasePath := strings.Split(workKubeconfigPath, "/")[0]
		_, err = os.Stat(workKubeconfigBasePath)
		if err != nil {
			err := os.Mkdir(workKubeconfigBasePath, os.ModePerm)
			if err != nil {
				return err
			}
		}
		err = os.WriteFile(workKubeconfigPath, []byte(kubeconfig), 0600)
		if err != nil {
			return errors.Wrap(err, "failed to save the workload cluster kubeconfig")
		}

		ctx.Status.End(true) // End Saving the workload cluster kubeconfig

		// Install unmanaged cluster addons
		if !a.keosCluster.Spec.ControlPlane.Managed {

			ctx.Status.Start("Installing cloud-provider in workload cluster ☁️")
			defer ctx.Status.End(false)

			err = infra.installCloudProvider(n, kubeconfigPath, privateParams)
			if err != nil {
				return errors.Wrap(err, "failed to install external cloud-provider in workload cluster")
			}

			ctx.Status.End(true) // End Installing cloud-provider in workload cluster
		}

		if !a.keosCluster.Spec.ControlPlane.Managed || a.keosCluster.Spec.InfraProvider == "aws" {
			ctx.Status.Start("Installing Calico in workload cluster 🔌")
			defer ctx.Status.End(false)

			isNetPolEngine := gcpGKEEnabled || awsEKSEnabled

			err = installCalico(n, kubeconfigPath, privateParams, isNetPolEngine, false)

			if err != nil {
				return errors.Wrap(err, "failed to install Calico in workload cluster")
			}

			ctx.Status.End(true) // End Installing Calico in workload cluster
		}

		ctx.Status.Start("Preparing nodes in workload cluster 📦")
		defer ctx.Status.End(false)

		if awsEKSEnabled {
			c = "kubectl -n capa-system rollout restart deployment capa-controller-manager"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to reload capa-controller-manager")
			}
			c = "kubectl -n capa-system rollout status deployment capa-controller-manager"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to wait for capa-controller-manager")
			}
			// Patch aws-node clusterrole with the required permissions
			// https://github.com/aws/amazon-vpc-cni-k8s?tab=readme-ov-file#annotate_pod_ip-v193
			rbacAWSNodePath := "/kind/aws-node_rbac.yaml"

			// Deploy Kubernetes additional RBAC aws node
			c = "echo \"" + rbacAWSNode + "\" > " + rbacAWSNodePath
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to write the kubernetes additional RBAC aws node")
			}
			c = "kubectl --kubeconfig " + kubeconfigPath + " apply -f " + rbacAWSNodePath
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to apply the kubernetes additional RBAC aws node")
			}
		}

		if isMachinePool {
			// Wait for all the machine pools to be ready
			c = "kubectl -n " + capiClustersNamespace + " wait --for=condition=Ready --timeout=15m --all mp"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to create the worker Cluster")
			}
			// Wait for container metrics to be available
			c = "kubectl --kubeconfig " + kubeconfigPath + " -n kube-system rollout status deployment -l k8s-app=metrics-server --timeout=90s"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to wait for container metrics to be available")
			}
		} else {
			// Wait for all the machine deployments to be ready
			c = "kubectl -n " + capiClustersNamespace + " wait --for=condition=Ready --timeout=15m --all md"

			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to create the worker Cluster")
			}
		}

		if !a.keosCluster.Spec.ControlPlane.Managed && *a.keosCluster.Spec.ControlPlane.HighlyAvailable {
			// Wait for all control planes to be ready
			c = "kubectl -n " + capiClustersNamespace +
				" wait --for=jsonpath=\"{.status.readyReplicas}\"=3" +
				" --timeout 10m kubeadmcontrolplanes " + a.keosCluster.Metadata.Name + "-control-plane"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to create the worker Cluster")
			}
		}

		ctx.Status.End(true) // End Preparing nodes in workload cluster

		if gcpGKEEnabled {
			ctx.Status.Start("Enabling CoreDNS as DNS server 📡")
			defer ctx.Status.End(false)

			gcpCoreDNSTemplate, err := getManifest(a.keosCluster.Spec.InfraProvider, "coredns_deployment.tmpl", majorVersion, privateParams)

			coreDNSTemplate := "/kind/coredns-configmap.yaml"
			coreDNSConfigmap, err := getManifest(a.keosCluster.Spec.InfraProvider, "coredns_configmap.tmpl", majorVersion, a.keosCluster.Spec)
			if err != nil {
				return errors.Wrap(err, "failed to get CoreDNS file")
			}
			c = "echo '" + coreDNSConfigmap + "' > " + coreDNSTemplate
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to create CoreDNS configmap file")
			}
			c = "kubectl --kubeconfig " + kubeconfigPath + " apply -f " + coreDNSTemplate
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to apply CoreDNS configmap")
			}

			c := "echo '" + gcpCoreDNSTemplate + "' > " + GKECoreDNSDeploymentPath
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to create CoreDNS deployment and RBAC file")
			}
			c = "kubectl --kubeconfig " + kubeconfigPath + " apply -f " + GKECoreDNSDeploymentPath
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to apply CoreDNS deployment and RBAC")
			}
			c = "kubectl --kubeconfig " + kubeconfigPath + " -n kube-system rollout status deploy/coredns --timeout=3m"
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to wait for the CoreDNS deployment to be ready")
			}

			c = "kubectl --kubeconfig " + kubeconfigPath + " scale deployment kube-dns-autoscaler -n kube-system --replicas=0"
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to disable kube-dns-autoscaler deployment")
			}
			c = "kubectl --kubeconfig " + kubeconfigPath + " scale deployment kube-dns -n kube-system --replicas=0"
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to disable kube-dns deployment")
			}
		}

		// Ensure CoreDNS replicas are assigned to different nodes
		// once more than 2 control planes or workers are running
		c = "kubectl --kubeconfig " + kubeconfigPath + " -n kube-system rollout restart deployment coredns"
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return errors.Wrap(err, "failed to restart coredns deployment")
		}

		// Wait for CoreDNS deployment to be ready
		c = "kubectl --kubeconfig " + kubeconfigPath + " -n kube-system rollout status deployment coredns"
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return errors.Wrap(err, "failed to wait for coredns ready")
		}

		ctx.Status.Start("Installing CAPx in workload cluster 🎖️")
		defer ctx.Status.End(false)
		err = provider.deployCertManager(n, keosRegistry.url, kubeconfigPath, privateParams, chartsList)
		if err != nil {
			return err
		}

		err = provider.installCAPXWorker(n, a.keosCluster, kubeconfigPath)
		if err != nil {
			return err
		}

		err = provider.configCAPIWorker(n, a.keosCluster, kubeconfigPath)
		if err != nil {
			return err
		}
		ctx.Status.End(true) // End Installing CAPx in workload cluster

		ctx.Status.Start("Configuring Flux in workload cluster 🧭")
		defer ctx.Status.End(false)

		err = configureFlux(n, kubeconfigPath, privateParams, helmRegistry, a.keosCluster.Spec, chartsList)
		if err != nil {
			return errors.Wrap(err, "failed to install Flux in workload cluster")
		}
		ctx.Status.End(true) // End Installing Flux in workload cluster

		ctx.Status.Start("Reconciling the existing Helm charts in workload cluster 🧲")
		defer ctx.Status.End(false)

		err = reconcileCharts(n, kubeconfigPath, privateParams, a.keosCluster.Spec, chartsList)
		if err != nil {
			return errors.Wrap(err, "failed to reconcile with Flux the existing Helm charts in workload cluster")
		}
		ctx.Status.End(true) // End Installing Flux in workload cluster

		ctx.Status.Start("Enabling workload cluster's self-healing 🏥")
		defer ctx.Status.End(false)

		err = enableSelfHealing(n, a.keosCluster, capiClustersNamespace, a.clusterConfig)
		if err != nil {
			return errors.Wrap(err, "failed to enable workload cluster's self-healing")
		}

		ctx.Status.End(true) // End Enabling workload cluster's self-healing

		//// <<<<<<< HEAD
		ctx.Status.Start("Configuring Network Policy Engine in workload cluster 🚧")
		defer ctx.Status.End(false)

		if !a.keosCluster.Spec.ControlPlane.Managed || a.keosCluster.Spec.InfraProvider == "aws" {
			// Allow egress in tigera-operator namespace
			c = "kubectl --kubeconfig " + kubeconfigPath + " -n tigera-operator apply -f " + allowCommonEgressNetPolPath
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to apply tigera-operator egress NetworkPolicy")
			}

			// Allow egress in calico-system namespace
			c = "kubectl --kubeconfig " + kubeconfigPath + " -n calico-system apply -f " + allowCommonEgressNetPolPath
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to apply calico-system egress NetworkPolicy")
			}
		}

		// Allow egress in CAPX's Namespace
		c = "kubectl --kubeconfig " + kubeconfigPath + " -n " + provider.capxName + "-system apply -f " + allowCommonEgressNetPolPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to apply CAPX's NetworkPolicy in workload cluster")
		}

		capiDeployments := []struct {
			name      string
			namespace string
		}{
			{name: "capi-controller-manager", namespace: "capi-system"},
			{name: "capi-kubeadm-control-plane-controller-manager", namespace: "capi-kubeadm-control-plane-system"},
			{name: "capi-kubeadm-bootstrap-controller-manager", namespace: "capi-kubeadm-bootstrap-system"},
		}
		allowedNamePattern := regexp.MustCompile(`^capi-kubeadm-(control-plane|bootstrap)-controller-manager$`)

		// Allow egress in CAPI's Namespaces
		for _, deployment := range capiDeployments {
			if !provider.capxManaged || (provider.capxManaged && !allowedNamePattern.MatchString(deployment.name)) {
				c = "kubectl --kubeconfig " + kubeconfigPath + " -n " + deployment.namespace + " apply -f " + allowCommonEgressNetPolPath
				_, err = commons.ExecuteCommand(n, c, 5, 3)
				if err != nil {
					return errors.Wrap(err, "failed to apply CAPI's egress NetworkPolicy in namespace "+deployment.namespace)
				}
			}
		}

		// Allow egress in cert-manager Namespace
		c = "kubectl --kubeconfig " + kubeconfigPath + " -n cert-manager apply -f " + allowCommonEgressNetPolPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)

		if err != nil {
			return errors.Wrap(err, "failed to apply cert-manager's NetworkPolicy")
		}

		// Set the deny-all-traffic-to-imds and allow-selected-namespace-to-imds as the default global network policy
		// Create the allow and deny (global) network policy file in the container
		denyallEgressIMDSGNetPolPath := "/kind/deny-all-egress-imds_gnetpol.yaml"
		allowCAPXEgressIMDSGNetPolPath := "/kind/allow-egress-imds_gnetpol.yaml"

		// Allow egress in kube-system Namespace
		c = "kubectl --kubeconfig " + kubeconfigPath + " -n kube-system apply -f " + allowCommonEgressNetPolPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to apply kube-system egress NetworkPolicy")
		}
		denyEgressIMDSGNetPol, err := provider.getDenyAllEgressIMDSGNetPol()
		if err != nil {
			return err
		}

		c = "echo \"" + denyEgressIMDSGNetPol + "\" > " + denyallEgressIMDSGNetPolPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to write the deny-all-traffic-to-aws-imds global network policy")
		}
		allowEgressIMDSGNetPol, err := provider.getAllowCAPXEgressIMDSGNetPol()
		if err != nil {
			return err
		}

		c = "echo \"" + allowEgressIMDSGNetPol + "\" > " + allowCAPXEgressIMDSGNetPolPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to write the allow-traffic-to-aws-imds-capa global network policy")
		}

		// Deny CAPA egress to AWS IMDS
		c = "kubectl --kubeconfig " + kubeconfigPath + " apply -f " + denyallEgressIMDSGNetPolPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to apply deny IMDS traffic GlobalNetworkPolicy")
		}

		// Allow CAPA egress to AWS IMDS
		c = "kubectl --kubeconfig " + kubeconfigPath + " apply -f " + allowCAPXEgressIMDSGNetPolPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to apply allow CAPX as egress GlobalNetworkPolicy")
		}

		ctx.Status.End(true) // End Configuring Network Policy Engine in workload cluster

		if !a.keosCluster.Spec.ControlPlane.Managed {

			ctx.Status.Start("Installing CSI in workload cluster 💾")
			defer ctx.Status.End(false)

			err = infra.installCSI(n, kubeconfigPath, privateParams, providerParams, chartsList)
			if err != nil {
				return errors.Wrap(err, "failed to install CSI in workload cluster")
			}

			ctx.Status.End(true)

		}

		ctx.Status.Start("Installing StorageClass in workload cluster 💾")
		defer ctx.Status.End(false)

		err = infra.configureStorageClass(n, kubeconfigPath)
		if err != nil {
			return errors.Wrap(err, "failed to configure StorageClass in workload cluster")
		}
		ctx.Status.End(true) // End Installing StorageClass in workload cluster

		if a.keosCluster.Spec.DeployAutoscaler && !isMachinePool {
			ctx.Status.Start("Installing cluster-autoscaler in workload cluster 🗚")
			defer ctx.Status.End(false)

			err = deployClusterAutoscaler(n, chartsList, privateParams, capiClustersNamespace, a.moveManagement)
			if err != nil {
				return errors.Wrap(err, "failed to install cluster-autoscaler in workload cluster")
			}

			ctx.Status.End(true) // End Installing cluster-autoscaler in workload cluster
		}

		ctx.Status.Start("Installing keos cluster operator in workload cluster 💻")
		defer ctx.Status.End(false)

		err = provider.deployClusterOperator(n, privateParams, a.clusterCredentials, keosRegistry, a.clusterConfig, kubeconfigPath, true, helmRegistry)
		if err != nil {
			return errors.Wrap(err, "failed to deploy cluster operator in workload cluster")
		}

		ctx.Status.End(true) // Installing keos cluster operator in workload cluster

		// Apply custom CoreDNS configuration
		if len(a.keosCluster.Spec.Dns.Forwarders) > 0 && (!awsEKSEnabled || !gcpGKEEnabled) {
			ctx.Status.Start("Customizing CoreDNS configuration 🪡")
			defer ctx.Status.End(false)

			err = customCoreDNS(n, a.keosCluster)
			if err != nil {
				return errors.Wrap(err, "failed to customized CoreDNS configuration")
			}

			ctx.Status.End(true) // End Customizing CoreDNS configuration
		}

		if provider.capxProvider == "gcp" {
			// XXX Ref kubernetes/kubernetes#86793 Starting from v1.18, gcp cloud-controller-manager requires RBAC to patch,update service/status (in-tree)
			ctx.Status.Start("Creating Kubernetes RBAC for internal loadbalancing 🔐")
			defer ctx.Status.End(false)

			requiredInternalNginx, err := infra.internalNginx(providerParams, a.keosCluster.Spec.Networks)
			if err != nil {
				return err
			}

			if requiredInternalNginx {
				rbacInternalLoadBalancingPath := "/kind/internalloadbalancing_rbac.yaml"

				// Deploy Kubernetes RBAC internal loadbalancing
				c = "echo \"" + rbacInternalLoadBalancing + "\" > " + rbacInternalLoadBalancingPath
				_, err = commons.ExecuteCommand(n, c, 5, 3)
				if err != nil {
					return errors.Wrap(err, "failed to write the kubernetes RBAC internal loadbalancing")
				}

				c = "kubectl --kubeconfig " + kubeconfigPath + " apply -f " + rbacInternalLoadBalancingPath
				_, err = commons.ExecuteCommand(n, c, 5, 3)
				if err != nil {
					return errors.Wrap(err, "failed to the kubernetes RBAC internal loadbalancing")
				}
			}
			ctx.Status.End(true) // End Creating Kubernetes RBAC for internal loadbalancing
		}

		if awsEKSEnabled && a.clusterConfig.Spec.EKSLBController {
			ctx.Status.Start("Installing AWS LB controller in workload cluster ⚖️")
			defer ctx.Status.End(false)
			err = installLBController(n, kubeconfigPath, privateParams, providerParams, chartsList)

			if err != nil {
				return errors.Wrap(err, "failed to install AWS LB controller in workload cluster")
			}
			ctx.Status.End(true) // End Installing AWS LB controller in workload cluster
		}

		// Create cloud-provisioner Objects backup
		ctx.Status.Start("Creating cloud-provisioner Objects backup 🗄️")
		defer ctx.Status.End(false)

		if _, err := os.Stat(localBackupPath); os.IsNotExist(err) {
			if err := os.MkdirAll(localBackupPath, 0755); err != nil {
				return errors.Wrap(err, "failed to create local backup directory")
			}
		}

		c = "mkdir -p " + cloudProviderBackupPath + " && chmod -R 0755 " + cloudProviderBackupPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to create cloud-provisioner backup directory")
		}

		c = "clusterctl move -n " + capiClustersNamespace + " --to-directory " + cloudProviderBackupPath
		_, err = commons.ExecuteCommand(n, c, 5, 3)
		if err != nil {
			return errors.Wrap(err, "failed to backup cloud-provisioner Objects")
		}

		for _, path := range PathsToBackupLocally {
			raw := bytes.Buffer{}
			cmd := exec.CommandContext(context.Background(), "sh", "-c", "docker cp "+n.String()+":"+path+" "+localBackupPath)
			if err := cmd.SetStdout(&raw).Run(); err != nil {
				return errors.Wrap(err, "failed to copy "+path+" to local host")
			}
		}

		ctx.Status.End(true)

		if !a.moveManagement {
			ctx.Status.Start("Moving the management role 🗝️")
			defer ctx.Status.End(false)

			c = "helm uninstall cluster-operator -n kube-system"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "Uninstalling cluster-operator")
			}

			// Create namespace, if not exists, for CAPI clusters in worker cluster
			c = "kubectl --kubeconfig " + kubeconfigPath + " get ns " + capiClustersNamespace
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				c = "kubectl --kubeconfig " + kubeconfigPath + " create ns " + capiClustersNamespace
				_, err = commons.ExecuteCommand(n, c, 5, 3)
				if err != nil {
					return errors.Wrap(err, "failed to create manifests Namespace")
				}
			}

			// Pivot management role to worker cluster
			c = "clusterctl move -n " + capiClustersNamespace + " --to-kubeconfig " + kubeconfigPath
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to pivot management role to worker cluster")
			}

			// Wait for keoscluster-controller-manager deployment to be ready
			c = "kubectl --kubeconfig " + kubeconfigPath + " rollout status deploy keoscluster-controller-manager -n kube-system --timeout=5m"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to wait for keoscluster controller ready")
			}

			if a.clusterConfig != nil {

				c = "kubectl -n " + capiClustersNamespace + " patch clusterconfig " + a.clusterConfig.Metadata.Name + " -p '{\"metadata\":{\"ownerReferences\":null,\"finalizers\":null}}' --type=merge"
				_, err = commons.ExecuteCommand(n, c, 5, 3)
				if err != nil {
					return errors.Wrap(err, "failed to remove clusterconfig ownerReferences and finalizers")
				}

				// Move clusterConfig to workload cluster
				c = "kubectl -n " + capiClustersNamespace + " get clusterconfig " + a.clusterConfig.Metadata.Name + " -o json | kubectl apply --kubeconfig " + kubeconfigPath + " -f-"
				_, err = commons.ExecuteCommand(n, c, 5, 3)
				if err != nil {
					return errors.Wrap(err, "failed to move clusterconfig to workload cluster")
				}

				// Delete clusterconfig in management cluster
				c = "kubectl -n " + capiClustersNamespace + " delete clusterconfig " + a.clusterConfig.Metadata.Name
				_, err = commons.ExecuteCommand(n, c, 5, 3)
				if err != nil {
					return errors.Wrap(err, "failed to delete clusterconfig in management cluster")
				}

			}

			// Move keoscluster to workload cluster
			c = "kubectl -n " + capiClustersNamespace + " get keoscluster " + a.keosCluster.Metadata.Name + " -o json | jq 'del(.status)' | kubectl apply --kubeconfig " + kubeconfigPath + " -f-"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to move keoscluster to workload cluster")
			}

			c = "kubectl -n " + capiClustersNamespace + " patch keoscluster " + a.keosCluster.Metadata.Name + " -p '{\"metadata\":{\"finalizers\":null}}' --type=merge"
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to scale keoscluster deployment to 1")
			}

			// Delete keoscluster in management cluster
			c = "kubectl -n " + capiClustersNamespace + " delete keoscluster " + a.keosCluster.Metadata.Name
			_, err = commons.ExecuteCommand(n, c, 5, 3)
			if err != nil {
				return errors.Wrap(err, "failed to delete keoscluster in management cluster")
			}

			err = provider.deployClusterOperator(n, privateParams, a.clusterCredentials, keosRegistry, a.clusterConfig, "", false, helmRegistry)
			if err != nil {
				return errors.Wrap(err, "failed to deploy cluster operator")
			}

			ctx.Status.End(true) // End Moving the cluster-operator
		}

		ctx.Status.Start("Executing post-install steps 🎖️")
		defer ctx.Status.End(false)

		err = infra.postInstallPhase(n, kubeconfigPath)
		if err != nil {
			return err
		}

		ctx.Status.End(true)
	}

	ctx.Status.Start("Generating the KEOS descriptor 📝")
	defer ctx.Status.End(false)

	err = createKEOSDescriptor(a.keosCluster, scName)
	if err != nil {
		return err
	}

	err = override_vars(ctx, providerParams, a.keosCluster.Spec.Networks, infra, a.clusterConfig.Spec)
	if err != nil {
		return err
	}

	ctx.Status.End(true) // End Generating KEOS descriptor

	return nil
}
