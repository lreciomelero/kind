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

package cluster

import (
	"bytes"
	"embed"
	"os"
	"reflect"
	"strings"
	"text/template"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*
var ctel embed.FS

// DescriptorFile represents the YAML structure in the descriptor file
type DescriptorFile struct {
	APIVersion       string `yaml:"apiVersion"`
	Kind             string `yaml:"kind"`
	ClusterID        string `yaml:"cluster_id" validate:"required,min=3,max=100"`
	DeployAutoscaler bool   `yaml:"deploy_autoscaler" validate:"boolean"`

	Bastion Bastion `yaml:"bastion"`

	Credentials Credentials `yaml:"credentials"`

	InfraProvider string `yaml:"infra_provider" validate:"required,oneof='aws' 'gcp' 'azure'"`

	K8SVersion   string `yaml:"k8s_version" validate:"required,startswith=v,min=7,max=8"`
	Region       string `yaml:"region" validate:"required"`
	SSHKey       string `yaml:"ssh_key"`
	FullyPrivate bool   `yaml:"fully_private" validate:"boolean"`

	Networks Networks `yaml:"networks"`

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
		AWS AWS `yaml:"aws"`
	} `yaml:"control_plane"`

	WorkerNodes WorkerNodes `yaml:"worker_nodes" validate:"required,dive"`
}

type Networks struct {
	VPCID                      string            `yaml:"vpc_id"`
	CidrBlock                  string            `yaml:"cidr,omitempty"`
	Tags                       map[string]string `yaml:"tags,omitempty"`
	AvailabilityZoneUsageLimit int               `yaml:"az_usage_limit" validate:"numeric"`
	AvailabilityZoneSelection  string            `yaml:"az_selection" validate:"oneof='Ordered' 'Random' '' "`

	Subnets []Subnets `yaml:"subnets"`
}

type Subnets struct {
	SubnetId         string            `yaml:"subnet_id"`
	AvailabilityZone string            `yaml:"az,omitempty"`
	IsPublic         *bool             `yaml:"is_public,omitempty"`
	RouteTableId     string            `yaml:"route_table_id,omitempty"`
	NatGatewayId     string            `yaml:"nat_id,omitempty"`
	Tags             map[string]string `yaml:"tags,omitempty"`
	CidrBlock        string            `yaml:"cidr,omitempty"`
}

type AWS struct {
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
	Name             string            `yaml:"name" validate:"required"`
	AmiID            string            `yaml:"ami_id"`
	Quantity         int               `yaml:"quantity" validate:"required,numeric,gt=0"`
	Size             string            `yaml:"size" validate:"required"`
	Image            string            `yaml:"image" validate:"required_if=InfraProvider gcp"`
	ZoneDistribution string            `yaml:"zone_distribution" validate:"omitempty,oneof='balanced' 'unbalanced'"`
	AZ               string            `yaml:"az"`
	SSHKey           string            `yaml:"ssh_key"`
	Spot             bool              `yaml:"spot" validate:"omitempty,boolean"`
	Labels           map[string]string `yaml:"labels"`
	NodeGroupMaxSize int               `yaml:"max_size" validate:"omitempty,numeric,required_with=NodeGroupMinSize,gtefield=Quantity,gt=0"`
	NodeGroupMinSize int               `yaml:"min_size" validate:"omitempty,numeric,required_with=NodeGroupMaxSize,ltefield=Quantity,gt=0"`
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

type Node struct {
	AZ      string
	QA      int
	MaxSize int
	MinSize int
}

type Credentials struct {
	AWS              AWSCredentials              `yaml:"aws"`
	GCP              GCPCredentials              `yaml:"gcp"`
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
	KeosRegistry bool   `yaml:"keos_registry" validate:"boolean"`
}

type TemplateParams struct {
	Descriptor       DescriptorFile
	Credentials      map[string]string
	DockerRegistries []map[string]interface{}
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
func GetClusterDescriptor(descriptorPath string) (*DescriptorFile, error) {
	descriptorRAW, err := os.ReadFile(descriptorPath)
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

func isNotEmpty(v interface{}) bool {
	return !reflect.ValueOf(v).IsZero()
}

func checkReference(v interface{}) bool {
	defer func() { recover() }()
	return v != nil && !reflect.ValueOf(v).IsNil() && v != "nil" && v != "<nil>"
}

func resto(n int, i int) int {
	var r int
	r = (n % 3) / (i + 1)
	if r > 1 {
		r = 1
	}
	return r
}

func GetClusterManifest(flavor string, params TemplateParams) (string, error) {

	funcMap := template.FuncMap{
		"loop": func(az string, zd string, qa int, maxsize int, minsize int) <-chan Node {
			ch := make(chan Node)
			go func() {
				var q int
				var mx int
				var mn int
				if az != "" {
					ch <- Node{AZ: az, QA: qa, MaxSize: maxsize, MinSize: minsize}
				} else {
					for i, a := range []string{"a", "b", "c"} {
						if zd == "unbalanced" {
							q = qa/3 + resto(qa, i)
							mx = maxsize/3 + resto(maxsize, i)
							mn = minsize/3 + resto(minsize, i)
							ch <- Node{AZ: a, QA: q, MaxSize: mx, MinSize: mn}
						} else {
							ch <- Node{AZ: a, QA: qa / 3, MaxSize: maxsize / 3, MinSize: minsize / 3}
						}
					}
				}
				close(ch)
			}()
			return ch
		},
		"hostname": func(s string) string {
			return strings.Split(s, "/")[0]
		},
	}

	var tpl bytes.Buffer
	t, err := template.New("").Funcs(funcMap).Funcs(template.FuncMap{"isNotEmpty": isNotEmpty}).Funcs(template.FuncMap{"checkReference": checkReference}).ParseFS(ctel, "templates/"+flavor)
	if err != nil {
		return "", err
	}

	err = t.ExecuteTemplate(&tpl, flavor, params)
	if err != nil {
		return "", err
	}
	return tpl.String(), nil
}
