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
	"os"

	"github.com/go-playground/validator/v10"
	vault "github.com/sosedoff/ansible-vault-go"
	"gopkg.in/yaml.v3"
)

// DescriptorFile represents the YAML structure in the descriptor file
type DescriptorFile struct {
	APIVersion       string `yaml:"apiVersion"`
	Kind             string `yaml:"kind"`
	ClusterID        string `yaml:"cluster_id" validate:"required,min=3,max=100"`
	DeployAutoscaler bool   `yaml:"deploy_autoscaler" validate:"boolean"`

	Bastion Bastion `yaml:"bastion"`

	Credentials Credentials `yaml:"credentials" validate:"omitempty,dive"`

	InfraProvider string `yaml:"infra_provider" validate:"required,oneof='aws' 'gcp' 'azure'"`

	K8SVersion   string `yaml:"k8s_version" validate:"required,startswith=v,min=7,max=8"`
	Region       string `yaml:"region" validate:"required"`
	SSHKey       string `yaml:"ssh_key"`
	FullyPrivate bool   `yaml:"fully_private" validate:"boolean"`

	Networks struct {
		VPCID   string `yaml:"vpc_id" validate:"required_with=Subnets"`
		Subnets []struct {
			AvailabilityZone string `yaml:"availability_zone"`
			Name             string `yaml:"name"`
			PrivateCIDR      string `yaml:"private_cidr" validate:"omitempty,cidrv4"`
			PublicCIDR       string `yaml:"public_cidr" validate:"omitempty,cidrv4"`
		} `yaml:"subnets" validate:"omitempty,dive"`
	} `yaml:"networks" validate:"omitempty,dive"`

	Dns struct {
		HostedZones bool `yaml:"hosted_zones" validate:"boolean"`
	} `yaml:"dns"`

	DockerRegistries []DockerRegistry `yaml:"docker_registries" validate:"dive"`

	ExternalDomain string `yaml:"external_domain" validate:"omitempty,hostname"`

	Keos struct {
		Domain  string `yaml:"domain" validate:"required,hostname"`
		Flavour string `yaml:"flavour"`
		Version string `yaml:"version"`
	} `yaml:"keos"`

	ControlPlane struct {
		Managed         bool   `yaml:"managed" validate:"boolean"`
		Name            string `yaml:"name"`
		AmiID           string `yaml:"ami_id"`
		HighlyAvailable bool   `yaml:"highly_available" validate:"boolean"`
		Size            string `yaml:"size" validate:"required_if=Managed false"`
		Image           string `yaml:"image" validate:"required_if=InfraProvider gcp"`
		RootVolume      struct {
			Size      int    `yaml:"size" validate:"numeric"`
			Type      string `yaml:"type"`
			Encrypted bool   `yaml:"encrypted" validate:"boolean"`
		} `yaml:"root_volume"`
		AWS AWSCP `yaml:"aws"`
	} `yaml:"control_plane"`

	WorkerNodes WorkerNodes `yaml:"worker_nodes" validate:"required,dive"`
}

type AWSCP struct {
	AssociateOIDCProvider bool `yaml:"associate_oidc_provider" validate:"boolean"`
	Logging               struct {
		ApiServer         bool `yaml:"api_server" validate:"boolean"`
		Audit             bool `yaml:"audit" validate:"boolean"`
		Authenticator     bool `yaml:"authenticator" validate:"boolean"`
		ControllerManager bool `yaml:"controller_manager" validate:"boolean"`
		Scheduler         bool `yaml:"scheduler" validate:"boolean"`
	} `yaml:"logging"`
}

type WorkerNodes []struct {
	Name             string `yaml:"name" validate:"required"`
	AmiID            string `yaml:"ami_id"`
	Quantity         int    `yaml:"quantity" validate:"required,numeric,gt=0"`
	Size             string `yaml:"size" validate:"required"`
	Image            string `yaml:"image" validate:"required_if=InfraProvider gcp"`
	ZoneDistribution string `yaml:"zone_distribution" validate:"omitempty,oneof='balanced' 'unbalanced'"`
	AZ               string `yaml:"az"`
	SSHKey           string `yaml:"ssh_key"`
	Spot             bool   `yaml:"spot" validate:"omitempty,boolean"`
	NodeGroupMaxSize int    `yaml:"max_size" validate:"omitempty,numeric,required_if=DeployAutoscaler true,required_with=NodeGroupMinSize,gtefield=Quantity,gt=0"`
	NodeGroupMinSize int    `yaml:"min_size" validate:"omitempty,numeric,required_if=DeployAutoscaler true,required_with=NodeGroupMaxSize,ltefield=Quantity,gt=0"`
	RootVolume       struct {
		Size      int    `yaml:"size" validate:"numeric"`
		Type      string `yaml:"type"`
		Encrypted bool   `yaml:"encrypted" validate:"boolean"`
	} `yaml:"root_volume"`
}

// Bastion represents the bastion VM
type Bastion struct {
	AmiID             string   `yaml:"ami_id"`
	VMSize            string   `yaml:"vm_size"`
	AllowedCIDRBlocks []string `yaml:"allowedCIDRBlocks"`
}

type Credentials struct {
	AWS              AWSCredentials              `yaml:"aws" validate:"excluded_with=GCP"`
	GCP              GCPCredentials              `yaml:"gcp" validate:"excluded_with=AWS"`
	GithubToken      string                      `yaml:"github_token"`
	DockerRegistries []DockerRegistryCredentials `yaml:"docker_registries"`
}

type AWSCredentials struct {
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	Region    string `yaml:"region"`
	Account   string `yaml:"account"`
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
	Type         string `yaml:"type"`
	URL          string `yaml:"url" validate:"required"`
	KeosRegistry bool   `yaml:"keos_registry" validate:"omitempty,boolean"`
}

type TemplateParams struct {
	Descriptor       DescriptorFile
	Credentials      map[string]string
	ExternalRegistry map[string]string
}

type AWS struct {
	Credentials AWSCredentials `yaml:"credentials"`
}

type GCP struct {
	Credentials GCPCredentials `yaml:"credentials"`
}

type SecretsFile struct {
	Secrets Secrets `yaml:"secrets"`
}

type Secrets struct {
	AWS              AWS                         `yaml:"aws"`
	GCP              GCP                         `yaml:"gcp"`
	GithubToken      string                      `yaml:"github_token"`
	ExternalRegistry DockerRegistryCredentials   `yaml:"external_registry"`
	DockerRegistries []DockerRegistryCredentials `yaml:"docker_registries"`
}

// Init sets default values for the DescriptorFile
func (d DescriptorFile) Init() DescriptorFile {
	d.FullyPrivate = false
	d.ControlPlane.HighlyAvailable = true

	// Autoscaler
	d.DeployAutoscaler = true

	// EKS
	d.ControlPlane.AWS.AssociateOIDCProvider = true
	d.ControlPlane.AWS.Logging.ApiServer = false
	d.ControlPlane.AWS.Logging.Audit = false
	d.ControlPlane.AWS.Logging.Authenticator = false
	d.ControlPlane.AWS.Logging.ControllerManager = false
	d.ControlPlane.AWS.Logging.Scheduler = false

	// Hosted zones
	d.Dns.HostedZones = true

	return d
}

// Read descriptor file
func GetClusterDescriptor(descriptorName string) (*DescriptorFile, error) {
	_, err := os.Stat(descriptorName)
	if err != nil {
		return nil, errors.New("No exists any cluster descriptor as " + descriptorName)
	}
	descriptorRAW, err := os.ReadFile("./" + descriptorName)
	if err != nil {
		return nil, err
	}
	descriptorFile := new(DescriptorFile).Init()
	err = yaml.Unmarshal(descriptorRAW, &descriptorFile)
	if err != nil {
		return nil, err
	}
	validate := validator.New()
	err = validate.Struct(descriptorFile)
	if err != nil {
		return nil, err
	}
	return &descriptorFile, nil
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
		err := errors.New("The vaultPassword is incorrect")
		return nil, err
	}
	err = yaml.Unmarshal([]byte(secretRaw), &secretFile)
	if err != nil {
		return nil, err
	}
	return &secretFile, nil
}