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

package commons

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
	vault "github.com/sosedoff/ansible-vault-go"
	"gopkg.in/yaml.v3"
)

var EtcdVolumeSize = 8
var CriVolumeSize = 128
var RootVolumeDefaultSize = 128
var RootVolumeManagedDefaultSize = 256
var DeviceNameRegex = "^/dev/(sd[a-z]|xvd([a-d]|[a-d][a-z]|[e-z]))$"

var AWSVolumeType = "gp3"
var AzureVMsVolumeType = "Standard_LRS"
var GCPVMsVolumeType = "pd-ssd"

type Resource struct {
	APIVersion string      `yaml:"apiVersion" validate:"required"`
	Kind       string      `yaml:"kind" validate:"required"`
	Metadata   Metadata    `yaml:"metadata" validate:"required"`
	Spec       interface{} `yaml:"spec" validate:"required"`
}

type ClusterConfig struct {
	APIVersion string            `yaml:"apiVersion" validate:"required"`
	Kind       string            `yaml:"kind" validate:"required"`
	Metadata   Metadata          `yaml:"metadata" validate:"required"`
	Spec       ClusterConfigSpec `yaml:"spec" validate:"required"`
}

type KeosCluster struct {
	APIVersion string   `yaml:"apiVersion" validate:"required"`
	Kind       string   `yaml:"kind" validate:"required"`
	Metadata   Metadata `yaml:"metadata" validate:"required"`
	Spec       KeosSpec `yaml:"spec" validate:"required"`
}

type Metadata struct {
	Name        string            `yaml:"name,omitempty" validate:"required,min=3,max=100"`
	Namespace   string            `yaml:"namespace,omitempty" `
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

type ClusterConfigSpec struct {
	EKSLBController             bool               `yaml:"eks_lb_controller"`
	Private                     bool               `yaml:"private_registry"`
	ControlplaneConfig          ControlplaneConfig `yaml:"controlplane_config"`
	WorkersConfig               WorkersConfig      `yaml:"workers_config"`
	ClusterOperatorVersion      string             `yaml:"cluster_operator_version,omitempty"`
	ClusterOperatorImageVersion string             `yaml:"cluster_operator_image_version,omitempty"`
	PrivateHelmRepo             bool               `yaml:"private_helm_repo"`
	Charts                      []Chart            `yaml:"charts,omitempty"`
	Capx                        CAPX               `yaml:"capx,omitempty"`
}

type CAPX struct {
	CAPIVersion string `yaml:"capi_version,omitempty"`
	CAPZVersion string `yaml:"capz_version,omitempty"`
	CAPAVersion string `yaml:"capa_version,omitempty"`
	CAPGVersion string `yaml:"capg_version,omitempty"`
}

type Chart struct {
	Name    string `yaml:"name,omitempty"`
	Version string `yaml:"version,omitempty"`
}

type ChartEntry struct {
	Repository string
	Version    string
	Namespace  string
	Pull       bool
	Reconcile  bool
}

type ControlplaneConfig struct {
	MaxUnhealthy *int `yaml:"max_unhealthy,omitempty" validate:"omitempty,numeric,gte=0,lte=100"`
}

type WorkersConfig struct {
	MaxUnhealthy *int `yaml:"max_unhealthy,omitempty" validate:"omitempty,numeric,gte=0,lte=100"`
}

type ClusterConfigRef struct {
	Name string `json:"name,omitempty"`
}

// Spec represents the YAML structure in the spec field of the descriptor file
type KeosSpec struct {
	DeployAutoscaler bool `yaml:"deploy_autoscaler" validate:"boolean"`

	Bastion Bastion `yaml:"bastion,omitempty"`

	StorageClass StorageClass `yaml:"storageclass,omitempty"`

	Credentials Credentials `yaml:"credentials,omitempty"`

	InfraProvider string `yaml:"infra_provider" validate:"required,oneof='aws' 'gcp' 'azure'"`

	K8SVersion string `yaml:"k8s_version" validate:"required"`
	Region     string `yaml:"region" validate:"required"`

	Networks Networks `yaml:"networks,omitempty"`

	Dns struct {
		ManageZone bool     `yaml:"manage_zone,omitempty" validate:"boolean"`
		Forwarders []string `yaml:"forwarders,omitempty" validate:"omitempty,dive,ip_addr"`
	} `yaml:"dns,omitempty"`

	DockerRegistries []DockerRegistry `yaml:"docker_registries" validate:"required,dive"`

	HelmRepository HelmRepository `yaml:"helm_repository" validate:"required"`

	ExternalDomain string `yaml:"external_domain" validate:"fqdn"`

	Security Security `yaml:"security,omitempty"`

	Keos Keos `yaml:"keos,omitempty"`

	ControlPlane ControlPlane `yaml:"control_plane" validate:"required,dive"`

	WorkerNodes WorkerNodes `yaml:"worker_nodes" validate:"required,dive"`

	ClusterConfigRef ClusterConfigRef `yaml:"cluster_config_ref,omitempty" validate:"dive"`
}

type ControlPlane struct {
	Managed         bool                `yaml:"managed" validate:"boolean"`
	NodeImage       string              `yaml:"node_image,omitempty"`
	HighlyAvailable *bool               `yaml:"highly_available,omitempty" validate:"boolean"`
	Size            string              `yaml:"size,omitempty" validate:"required_if=Managed false"`
	RootVolume      RootVolume          `yaml:"root_volume,omitempty"`
	Tags            []map[string]string `yaml:"tags,omitempty"`
	AWS             AWSCP               `yaml:"aws,omitempty"`
	Azure           AzureCP             `yaml:"azure,omitempty"`
	Gcp             GCPCP               `yaml:"gcp,omitempty"`
	CRIVolume       CustomVolume        `yaml:"cri_volume,omitempty"  validate:"dive"`
	ETCDVolume      CustomVolume        `yaml:"etcd_volume,omitempty"  validate:"dive"`
	ExtraVolumes    []ExtraVolume       `yaml:"extra_volumes,omitempty" validate:"dive"`
}

type GCPCP struct {
	ClusterNetwork                 ClusterNetwork                 `yaml:"cluster_network,omitempty"`
	MasterAuthorizedNetworksConfig MasterAuthorizedNetworksConfig `yaml:"master_authorized_networks_config,omitempty"`
}

type ClusterNetwork struct {
	PrivateCluster PrivateCluster `yaml:"private_cluster,omitempty"`
}

type PrivateCluster struct {
	// +kubebuilder:default=true
	EnablePrivateEndpoint bool `yaml:"enable_private_endpoint,omitempty"`
	// +kubebuilder:default=true
	EnablePrivateNodes bool `yaml:"enable_private_nodes,omitempty"`
	// +kubebuilder:validation:Pattern=`^(10\.\d{1,3}\.\d{1,3}\.\d{1,3}\/[0-9]{1,2})$|^(172\.(1[6-9]|2[0-9]|3[0-1])\.\d{1,3}\.\d{1,3}\/[0-9]{1,2})$|^(192\.168\.\d{1,3}\.\d{1,3}\/[0-9]{1,2})$`
	ControlPlaneCidrBlock string `yaml:"control_plane_cidr_block,omitempty"`
}

// MasterAuthorizedNetworksConfig represents configuration options for master authorized networks feature of the GKE cluster.
type MasterAuthorizedNetworksConfig struct {
	CIDRBlocks []CIDRBlock `yaml:"cidr_blocks,omitempty"`
	// +kubebuilder:default=false
	GCPPublicCIDRsAccessEnabled *bool `yaml:"gcp_public_cidrs_access_enabled,omitempty"`
}

type CIDRBlock struct {
	// +kubebuilder:validation:Pattern=`^(10\.\d{1,3}\.\d{1,3}\.\d{1,3}\/[0-9]{1,2})$|^(172\.(1[6-9]|2[0-9]|3[0-1])\.\d{1,3}\.\d{1,3}\/[0-9]{1,2})$|^(192\.168\.\d{1,3}\.\d{1,3}\/[0-9]{1,2})$`
	CIDRBlock string `yaml:"cidr_block"`

	// +kubebuilder:validation:Optional
	DisplayName string `yaml:"display_name,omitempty"`
}

type Keos struct {
	Flavour string `yaml:"flavour,omitempty"`
}

type Networks struct {
	VPCID         string    `yaml:"vpc_id,omitempty"`
	VPCCIDRBlock  string    `yaml:"vpc_cidr,omitempty" validate:"omitempty,cidrv4"`
	PodsCidrBlock string    `yaml:"pods_cidr,omitempty" validate:"omitempty,cidrv4"`
	PodsSubnets   []Subnets `yaml:"pods_subnets,omitempty" validate:"dive"`
	Subnets       []Subnets `yaml:"subnets,omitempty" validate:"dive"`
	ResourceGroup string    `yaml:"resource_group,omitempty"`
}

type Subnets struct {
	SubnetId  string `yaml:"subnet_id"`
	CidrBlock string `yaml:"cidr,omitempty" validate:"omitempty,cidrv4"`
	Role      string `yaml:"role,omitempty" validate:"omitempty,oneof='control-plane' 'node'"`
}

type AWSCP struct {
	AssociateOIDCProvider bool   `yaml:"associate_oidc_provider,omitempty" validate:"boolean"`
	EncryptionKey         string `yaml:"encryption_key,omitempty"`
	Logging               struct {
		ApiServer         bool `yaml:"api_server" validate:"boolean"`
		Audit             bool `yaml:"audit" validate:"boolean"`
		Authenticator     bool `yaml:"authenticator" validate:"boolean"`
		ControllerManager bool `yaml:"controller_manager" validate:"boolean"`
		Scheduler         bool `yaml:"scheduler" validate:"boolean"`
	} `yaml:"logging"`
}

type AzureCP struct {
	Tier string `yaml:"tier" validate:"omitempty,oneof='Free' 'Paid'"`
}

type Security struct {
	ControlPlaneIdentity string `yaml:"control_plane_identity,omitempty"`
	NodesIdentity        string `yaml:"nodes_identity,omitempty"`
	AWS                  struct {
		CreateIAM bool `yaml:"create_iam" validate:"boolean"`
	} `yaml:"aws,omitempty"`
}

type WorkerNodes []struct {
	Name             string            `yaml:"name" validate:"required"`
	NodeImage        string            `yaml:"node_image,omitempty"`
	Quantity         *int              `yaml:"quantity" validate:"required,numeric,gte=0"`
	Size             string            `yaml:"size" validate:"required"`
	ZoneDistribution string            `yaml:"zone_distribution,omitempty" validate:"omitempty,oneof='balanced' 'unbalanced'"`
	AZ               string            `yaml:"az,omitempty"`
	SSHKey           string            `yaml:"ssh_key,omitempty"`
	Spot             bool              `yaml:"spot,omitempty" validate:"boolean"`
	Labels           map[string]string `yaml:"labels,omitempty"`
	Taints           []string          `yaml:"taints,omitempty"`
	NodeGroupMaxSize int               `yaml:"max_size,omitempty" validate:"omitempty,required_with=NodeGroupMinSize,numeric"`
	NodeGroupMinSize *int              `yaml:"min_size,omitempty" validate:"omitempty,required_with=NodeGroupMaxSize,numeric,gte=0"`
	RootVolume       RootVolume        `yaml:"root_volume,omitempty"`
	CRIVolume        CustomVolume      `yaml:"cri_volume,omitempty"  validate:"dive"`
	ExtraVolumes     []ExtraVolume     `yaml:"extra_volumes,omitempty" validate:"dive"`
}

// Bastion represents the bastion VM
type Bastion struct {
	NodeImage         string   `yaml:"node_image"`
	VMSize            string   `yaml:"vm_size"`
	AllowedCIDRBlocks []string `yaml:"allowedCIDRBlocks"`
	SSHKey            string   `yaml:"ssh_key"`
}

type RootVolume struct {
	Size          int    `yaml:"size,omitempty"`
	Type          string `yaml:"type,omitempty"`
	Encrypted     bool   `yaml:"encrypted,omitempty"`
	EncryptionKey string `yaml:"encryption_key,omitempty"`
}

type ExtraVolume struct {
	Name          string `yaml:"name,omitempty"`
	DeviceName    string `yaml:"device_name,omitempty"`
	Size          int    `yaml:"size,omitempty"`
	Type          string `yaml:"type,omitempty"`
	Label         string `yaml:"label,omitempty"`
	Encrypted     bool   `yaml:"encrypted,omitempty" validate:"boolean"`
	EncryptionKey string `yaml:"encryption_key,omitempty"`
	MountPath     string `yaml:"mount_path,omitempty"`
}

type CustomVolume struct {
	Enabled       *bool  `yaml:"enabled,omitempty"`
	Size          int    `yaml:"size,omitempty"`
	Type          string `yaml:"type,omitempty"`
	Encrypted     bool   `yaml:"encrypted,omitempty"`
	EncryptionKey string `yaml:"encryption_key,omitempty"`
}

type ETCDVolume struct {
	Size          int    `yaml:"size" validate:"required,numeric"`
	Type          string `yaml:"type,omitempty"`
	Label         string `yaml:"label" validate:"required"`
	Encrypted     bool   `yaml:"encrypted,omitempty" validate:"boolean"`
	EncryptionKey string `yaml:"encryption_key,omitempty"`
}

type CRIVolume struct {
	Size          int    `yaml:"size" validate:"required,numeric"`
	Type          string `yaml:"type,omitempty"`
	Label         string `yaml:"label" validate:"required"`
	Encrypted     bool   `yaml:"encrypted,omitempty" validate:"boolean"`
	EncryptionKey string `yaml:"encryption_key,omitempty"`
}

type ClusterCredentials struct {
	ProviderCredentials         map[string]string
	KeosRegistryCredentials     map[string]string
	DockerRegistriesCredentials []map[string]interface{}
	HelmRepositoryCredentials   map[string]string
	GithubToken                 string
}

type Credentials struct {
	AWS              AWSCredentials              `yaml:"aws" validate:"excluded_with=AZURE GCP"`
	AZURE            AzureCredentials            `yaml:"azure" validate:"excluded_with=AWS GCP"`
	GCP              GCPCredentials              `yaml:"gcp" validate:"excluded_with=AWS AZURE"`
	GithubToken      string                      `yaml:"github_token"`
	DockerRegistries []DockerRegistryCredentials `yaml:"docker_registries"`
	HelmRepository   HelmRepositoryCredentials   `yaml:"helm_repository"`
}

type AWSCredentials struct {
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	Region    string `yaml:"region"`
	AccountID string `yaml:"account_id"`
}

type AzureCredentials struct {
	SubscriptionID string `yaml:"subscription_id"`
	TenantID       string `yaml:"tenant_id"`
	ClientID       string `yaml:"client_id"`
	ClientSecret   string `yaml:"client_secret"`
}

type GCPCredentials struct {
	ProjectID    string `yaml:"project_id"`
	PrivateKeyID string `yaml:"private_key_id"`
	PrivateKey   string `yaml:"private_key"`
	ClientEmail  string `yaml:"client_email"`
	ClientID     string `yaml:"client_id"`
}

type DockerRegistryCredentials struct {
	URL  string `yaml:"url"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
}

type DockerRegistry struct {
	AuthRequired bool   `yaml:"auth_required" validate:"boolean"`
	Type         string `yaml:"type" validate:"required,oneof='acr' 'ecr' 'gar' 'gcr' 'generic'"`
	URL          string `yaml:"url" validate:"required"`
	KeosRegistry bool   `yaml:"keos_registry" validate:"boolean"`
}

type HelmRepositoryCredentials struct {
	URL  string `yaml:"url"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
}

type HelmRepository struct {
	AuthRequired          bool   `yaml:"auth_required" validate:"boolean"`
	URL                   string `yaml:"url" validate:"required"`
	Type                  string `yaml:"type,omitempty" validate:"oneof='ecr' 'acr' 'gar' 'generic'"`
	ReleaseInterval       string `yaml:"release_interval,omitempty"`
	ReleaseRetries        *int   `yaml:"release_retries,omitempty"`
	ReleaseSourceInterval string `yaml:"release_source_interval,omitempty"`
	RepositoryInterval    string `yaml:"repository_interval,omitempty"`
}

type AWS struct {
	Credentials AWSCredentials `yaml:"credentials"`
}

type AZURE struct {
	Credentials AzureCredentials `yaml:"credentials"`
}

type GCP struct {
	Credentials GCPCredentials `yaml:"credentials"`
}

type SecretsFile struct {
	Secrets Secrets `yaml:"secrets"`
}

type Secrets struct {
	AWS              AWS                         `yaml:"aws"`
	AZURE            AZURE                       `yaml:"azure"`
	GCP              GCP                         `yaml:"gcp"`
	GithubToken      string                      `yaml:"github_token"`
	DockerRegistry   DockerRegistryCredentials   `yaml:"docker_registry"`
	DockerRegistries []DockerRegistryCredentials `yaml:"docker_registries"`
	HelmRepository   HelmRepositoryCredentials   `yaml:"helm_repository"`
}

type EFS struct {
	Name        string `yaml:"name" validate:"required_with=ID"`
	ID          string `yaml:"id" validate:"required_with=Name"`
	Permissions string `yaml:"permissions,omitempty"`
}

type StorageClass struct {
	EFS           EFS          `yaml:"efs,omitempty"`
	EncryptionKey string       `yaml:"encryptionKey,omitempty"`
	Class         string       `yaml:"class,omitempty" validate:"omitempty,oneof='standard' 'premium'"`
	Parameters    SCParameters `yaml:"parameters,omitempty"`
}

type SCParameters struct {
	// Common
	Type   string `yaml:"type,omitempty"`
	FsType string `yaml:"fsType,omitempty"`
	Labels string `yaml:"labels,omitempty"`

	// AWS
	AllowAutoIOPSPerGBIncrease string `yaml:"allowAutoIOPSPerGBIncrease,omitempty" validate:"omitempty,oneof='true' 'false'"`
	BlockExpress               string `yaml:"blockExpress,omitempty" validate:"omitempty,oneof='true' 'false'"`
	BlockSize                  string `yaml:"blockSize,omitempty"`
	Iops                       string `yaml:"iops,omitempty" validate:"omitempty,excluded_with=IopsPerGB"`
	IopsPerGB                  string `yaml:"iopsPerGB,omitempty" validate:"omitempty,excluded_with=Iops"`
	Encrypted                  string `yaml:"encrypted,omitempty" validate:"omitempty,oneof='true' 'false'"`
	KmsKeyId                   string `yaml:"kmsKeyId,omitempty"`
	Throughput                 int    `yaml:"throughput,omitempty" validate:"omitempty,gt=0"`

	// Azure
	CachingMode           string `yaml:"cachingMode,omitempty" validate:"omitempty,oneof='None' 'ReadOnly'"`
	DiskAccessID          string `yaml:"diskAccessID,omitempty"`
	DiskEncryptionSetID   string `yaml:"diskEncryptionSetID,omitempty"`
	DiskEncryptionType    string `yaml:"diskEncryptionType,omitempty" validate:"omitempty,oneof='EncryptionAtRestWithCustomerKey' 'EncryptionAtRestWithPlatformAndCustomerKeys'"`
	EnableBursting        string `yaml:"enableBursting,omitempty" validate:"omitempty,oneof='true' 'false'"`
	EnablePerformancePlus string `yaml:"enablePerformancePlus,omitempty" validate:"omitempty,oneof='true' 'false'"`
	Kind                  string `yaml:"kind,omitempty" validate:"omitempty,oneof='managed'"`
	NetworkAccessPolicy   string `yaml:"networkAccessPolicy,omitempty" validate:"omitempty,oneof='AllowAll' 'DenyAll' 'AllowPrivate'"`
	Provisioner           string `yaml:"provisioner,omitempty" validate:"omitempty,oneof='disk.csi.azure.com' 'file.csi.azure.com"`
	PublicNetworkAccess   string `yaml:"publicNetworkAccess,omitempty" validate:"omitempty,oneof='Enabled' 'Disabled'"`
	ResourceGroup         string `yaml:"resourceGroup,omitempty"`
	SkuName               string `yaml:"skuName,omitempty"`
	SubscriptionID        string `yaml:"subscriptionID,omitempty"`
	Tags                  string `yaml:"tags,omitempty"`

	// GCP
	DiskEncryptionKmsKey          string `yaml:"disk-encryption-kms-key,omitempty"`
	ProvisionedIopsOnCreate       string `yaml:"provisioned-iops-on-create,omitempty"`
	ProvisionedThroughputOnCreate string `yaml:"provisioned-throughput-on-create,omitempty"`
	ReplicationType               string `yaml:"replication-type,omitempty"`
}

func (s ClusterConfigSpec) Init() ClusterConfigSpec {
	s.Private = false
	s.WorkersConfig.MaxUnhealthy = ToPtr[int](100)
	return s
}

// Init sets default values for the Spec
func (s KeosSpec) Init() KeosSpec {
	highlyAvailable := true
	s.ControlPlane.HighlyAvailable = &highlyAvailable

	// AKS
	s.ControlPlane.Azure.Tier = "Paid"

	// Autoscaler
	s.DeployAutoscaler = true

	// EKS
	s.Security.AWS.CreateIAM = false
	s.ControlPlane.AWS.AssociateOIDCProvider = true
	s.ControlPlane.AWS.Logging.ApiServer = false
	s.ControlPlane.AWS.Logging.Audit = false
	s.ControlPlane.AWS.Logging.Authenticator = false
	s.ControlPlane.AWS.Logging.ControllerManager = false
	s.ControlPlane.AWS.Logging.Scheduler = false

	// GKE

	s.ControlPlane.Gcp.ClusterNetwork.PrivateCluster.EnablePrivateEndpoint = true
	s.ControlPlane.Gcp.ClusterNetwork.PrivateCluster.EnablePrivateNodes = true
	s.ControlPlane.Gcp.MasterAuthorizedNetworksConfig.GCPPublicCIDRsAccessEnabled = ToPtr[bool](false)

	// Helm
	s.HelmRepository.AuthRequired = false
	s.HelmRepository.Type = "generic"

	// Managed zones
	s.Dns.ManageZone = true

	return s
}

func (s KeosSpec) InitVolumes() KeosSpec {
	var volumeType string

	switch s.InfraProvider {
	case "aws":
		volumeType = AWSVolumeType

		if !s.ControlPlane.Managed {

			if s.ControlPlane.CRIVolume.Enabled == nil || *s.ControlPlane.CRIVolume.Enabled {
				s.ControlPlane.CRIVolume.Enabled = ToPtr(true)
				s = initControlPlaneCRIVolume(s, volumeType)
			}
			if s.ControlPlane.ETCDVolume.Enabled == nil || *s.ControlPlane.ETCDVolume.Enabled {
				s.ControlPlane.ETCDVolume.Enabled = ToPtr(true)
				s = initControlPlaneETCDVolume(s, volumeType)
			}
			s = initControlPlaneRootVolume(s, volumeType, !*s.ControlPlane.CRIVolume.Enabled)
		}

		for i := range s.WorkerNodes {

			if s.WorkerNodes[i].CRIVolume.Enabled == nil || *s.WorkerNodes[i].CRIVolume.Enabled {
				s.WorkerNodes[i].CRIVolume.Enabled = ToPtr(true)
				checkAndFill(&s.WorkerNodes[i].CRIVolume.Size, CriVolumeSize)
				checkAndFill(&s.WorkerNodes[i].CRIVolume.Type, volumeType)
				checkAndFill(&s.WorkerNodes[i].RootVolume.Size, RootVolumeDefaultSize)
				checkAndFill(&s.WorkerNodes[i].RootVolume.Type, volumeType)
			} else {
				checkAndFill(&s.WorkerNodes[i].RootVolume.Size, RootVolumeManagedDefaultSize)
				checkAndFill(&s.WorkerNodes[i].RootVolume.Type, volumeType)
			}

		}

	case "gcp":
		if !s.ControlPlane.Managed {
			volumeType = GCPVMsVolumeType
			if s.ControlPlane.CRIVolume.Enabled == nil || *s.ControlPlane.CRIVolume.Enabled {
				s.ControlPlane.CRIVolume.Enabled = ToPtr(true)
				s = initControlPlaneCRIVolume(s, volumeType)
			}
			if s.ControlPlane.ETCDVolume.Enabled == nil || *s.ControlPlane.ETCDVolume.Enabled {
				s.ControlPlane.ETCDVolume.Enabled = ToPtr(true)
				s = initControlPlaneETCDVolume(s, volumeType)
			}
			s = initControlPlaneRootVolume(s, volumeType, !*s.ControlPlane.CRIVolume.Enabled)
		}
		for i := range s.WorkerNodes {
			if !s.ControlPlane.Managed {
				if s.WorkerNodes[i].CRIVolume.Enabled == nil || *s.WorkerNodes[i].CRIVolume.Enabled {
					s.WorkerNodes[i].CRIVolume.Enabled = ToPtr(true)
					checkAndFill(&s.WorkerNodes[i].CRIVolume.Size, CriVolumeSize)
					checkAndFill(&s.WorkerNodes[i].CRIVolume.Type, volumeType)
					checkAndFill(&s.WorkerNodes[i].RootVolume.Size, RootVolumeDefaultSize)
					checkAndFill(&s.WorkerNodes[i].RootVolume.Type, volumeType)
				}
			} else {
				checkAndFill(&s.WorkerNodes[i].RootVolume.Size, RootVolumeManagedDefaultSize)
				checkAndFill(&s.WorkerNodes[i].RootVolume.Type, volumeType)
			}

		}

	case "azure":
		if !s.ControlPlane.Managed {
			volumeType = AzureVMsVolumeType
			if s.ControlPlane.CRIVolume.Enabled == nil || *s.ControlPlane.CRIVolume.Enabled {
				s.ControlPlane.CRIVolume.Enabled = ToPtr(true)
				s = initControlPlaneCRIVolume(s, volumeType)
			}
			if s.ControlPlane.ETCDVolume.Enabled == nil || *s.ControlPlane.ETCDVolume.Enabled {
				s.ControlPlane.ETCDVolume.Enabled = ToPtr(true)
				s = initControlPlaneETCDVolume(s, volumeType)
			}
			s = initControlPlaneRootVolume(s, volumeType, !*s.ControlPlane.CRIVolume.Enabled)
		}

		for i := range s.WorkerNodes {

			if !s.ControlPlane.Managed {
				if s.WorkerNodes[i].CRIVolume.Enabled == nil || *s.WorkerNodes[i].CRIVolume.Enabled {
					s.WorkerNodes[i].CRIVolume.Enabled = ToPtr(true)
					checkAndFill(&s.WorkerNodes[i].CRIVolume.Size, CriVolumeSize)
					checkAndFill(&s.WorkerNodes[i].CRIVolume.Type, volumeType)
					checkAndFill(&s.WorkerNodes[i].RootVolume.Size, RootVolumeDefaultSize)
					checkAndFill(&s.WorkerNodes[i].RootVolume.Type, volumeType)
				}
			} else {
				checkAndFill(&s.WorkerNodes[i].RootVolume.Size, RootVolumeManagedDefaultSize)
				checkAndFill(&s.WorkerNodes[i].RootVolume.Type, volumeType)
			}
		}

	}

	return s

}

// Read descriptor file
func GetClusterDescriptor(descriptorPath string) (*KeosCluster, *ClusterConfig, error) {
	var keosCluster KeosCluster
	var clusterConfig ClusterConfig
	findClusterConfig := false

	_, err := os.Stat(descriptorPath)
	if err != nil {
		return nil, nil, errors.New("No exists any cluster descriptor as " + descriptorPath)
	}

	descriptorRAW, err := os.ReadFile(descriptorPath)
	if err != nil {
		return nil, nil, err
	}

	validate := validator.New()
	validate.RegisterValidation("gte_param_if_exists", gteParamIfExists)
	validate.RegisterValidation("lte_param_if_exists", lteParamIfExists)
	validate.RegisterValidation("required_if_for_bool", requiredIfForBool)

	descriptorManifests := strings.Split(string(descriptorRAW), "---\n")
	for _, manifest := range descriptorManifests {
		var resource Resource
		err = yaml.Unmarshal([]byte(manifest), &resource)
		if err != nil {
			return nil, nil, err
		}
		if !reflect.DeepEqual(resource, Resource{}) {
			err = validate.Struct(resource)
			if err != nil {
				return nil, nil, err
			}

			switch resource.Kind {
			case "KeosCluster":
				keosCluster.Spec = new(KeosSpec).Init()
				err = yaml.Unmarshal([]byte(manifest), &keosCluster)
				if err != nil {
					return nil, nil, err
				}
				keosCluster.Spec = keosCluster.Spec.InitVolumes()
				err = validate.Struct(keosCluster)
				if err != nil {
					return nil, nil, err
				}

				keosCluster.Metadata.Namespace = "cluster-" + keosCluster.Metadata.Name
			case "ClusterConfig":
				findClusterConfig = true
				clusterConfig.Spec = new(ClusterConfigSpec).Init()
				err = yaml.Unmarshal([]byte(manifest), &clusterConfig)
				if err != nil {
					return nil, nil, err
				}
				err = validate.Struct(clusterConfig)
				if err != nil {
					return nil, nil, err
				}
				clusterConfig.Metadata.Namespace = "cluster-" + keosCluster.Metadata.Name
			default:
				return nil, nil, errors.New("Unsupported manifest kind: " + resource.Kind)
			}
		}
	}

	if reflect.DeepEqual(keosCluster, KeosCluster{}) {
		return nil, nil, errors.New("Keoscluster's manifest has not been found.")
	}

	if !findClusterConfig {
		clusterConfig = ClusterConfig{}
		clusterConfig.APIVersion = "installer.stratio.com/v1beta1"
		clusterConfig.Kind = "ClusterConfig"
		clusterConfig.Metadata.Name = keosCluster.Spec.InfraProvider + "-config"
		clusterConfig.Metadata.Namespace = "cluster-" + keosCluster.Metadata.Name
		clusterConfig.Spec = new(ClusterConfigSpec).Init()
		if !keosCluster.Spec.ControlPlane.Managed {
			clusterConfig.Spec.ControlplaneConfig.MaxUnhealthy = ToPtr[int](34)
		}
	}

	return &keosCluster, &clusterConfig, nil
}

func DecryptFile(filePath string, vaultPassword string) (string, error) {
	data, err := vault.DecryptFile(filePath, vaultPassword)

	if err != nil {
		return "", err
	}
	return data, nil
}

func GetSecretsFile(secretsPath string, vaultPassword string) (*SecretsFile, error) {
	secretRaw, err := DecryptFile(secretsPath, vaultPassword)
	var secretFile SecretsFile
	if err != nil {
		err := errors.New("the vaultPassword is incorrect")
		return nil, err
	}

	err = yaml.Unmarshal([]byte(secretRaw), &secretFile)
	if err != nil {
		return nil, err
	}
	return &secretFile, nil
}

func IfExistsStructField(fl validator.FieldLevel) bool {
	structValue := reflect.ValueOf(fl.Parent().Interface())

	excludeFieldName := fl.Param()

	// Get the value of the exclude field
	excludeField := structValue.FieldByName(excludeFieldName)

	// Exclude field is set to false or invalid, so don't exclude this field
	return reflect.DeepEqual(excludeField, reflect.Zero(reflect.TypeOf(excludeField)).Interface())
}

func gteParamIfExists(fl validator.FieldLevel) bool {
	field := fl.Field()
	fieldCompared := fl.Param()

	if field.Kind() == reflect.Int && field.Int() == 0 {
		return true
	}

	var paramFieldValue reflect.Value

	if fl.Parent().Kind() == reflect.Ptr {
		paramFieldValue = fl.Parent().Elem().FieldByName(fieldCompared)
	} else {
		paramFieldValue = fl.Parent().FieldByName(fieldCompared)
	}

	if paramFieldValue.Kind() != reflect.Int {
		return false
	}
	if paramFieldValue.Int() == 0 {
		return true
	}

	if paramFieldValue.Int() > 0 {
		return field.Int() >= paramFieldValue.Int()
	}
	return false
}

func lteParamIfExists(fl validator.FieldLevel) bool {
	field := fl.Field()
	fieldCompared := fl.Param()

	//omitEmpty
	if field.Kind() == reflect.Int && field.Int() == 0 {
		return true
	}

	var paramFieldValue reflect.Value

	if fl.Parent().Kind() == reflect.Ptr {
		paramFieldValue = fl.Parent().Elem().FieldByName(fieldCompared)
	} else {
		paramFieldValue = fl.Parent().FieldByName(fieldCompared)
	}

	if paramFieldValue.Kind() != reflect.Int {
		return false
	}

	if paramFieldValue.Int() == 0 {
		return true
	}

	if paramFieldValue.Int() > 0 {
		return field.Int() <= paramFieldValue.Int()
	}

	return false
}

func requiredIfForBool(fl validator.FieldLevel) bool {
	params := strings.Split(fl.Param(), " ")
	if len(params) != 2 {
		panic(fmt.Sprintf("Bad param number for required_if %s", fl.FieldName()))
	}

	if !requireCheckFieldValue(fl, params[0], params[1], false) {
		return true
	}
	field := fl.Field()
	fl.Parent()
	return field.IsValid() && field.Interface() != reflect.Zero(field.Type()).Interface()
}

func requireCheckFieldValue(fl validator.FieldLevel, param string, value string, defaultNotFoundValue bool) bool {
	field, kind, _, found := fl.GetStructFieldOKAdvanced2(fl.Parent(), param)
	if !found {
		return defaultNotFoundValue
	}

	if kind == reflect.Bool {
		val, err := strconv.ParseBool(value)
		if err != nil {
			return false
		}

		return field.Bool() == val
	}

	return false

}

// Ptr returns a pointer to the provided value.
func ToPtr[T any](v T) *T {
	return &v
}
