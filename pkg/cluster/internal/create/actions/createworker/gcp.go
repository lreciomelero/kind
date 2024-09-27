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

package createworker

import (
	"context"
	_ "embed"
	b64 "encoding/base64"
	"encoding/json"
	"net/url"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/commons"
	"sigs.k8s.io/kind/pkg/errors"
	"sigs.k8s.io/kind/pkg/exec"
)

//go:embed files/gcp/internal-ingress-nginx.yaml
var gcpInternalIngress []byte

//go:embed files/gcp/compositereourcedefinition-hostedzones-gcp.yaml
var gcpCRDHostedZones []byte

type GCPBuilder struct {
	capxProvider        string
	capxVersion         string
	capxImageVersion    string
	capxManaged         bool
	capxName            string
	capxEnvVars         []string
	scParameters        commons.SCParameters
	scProvisioner       string
	csiNamespace        string
	crossplaneProviders map[string]string
}

type CrossplaneGCPParams struct {
	ClusterName    string
	ExternalDomain string
	Addon          string
	ProjectName    string
	Managed        bool
}

var crossplaneGCPAddons = []string{"external-dns"}
var crossplaneGKEAddons = []string{"external-dns"}

func newGCPBuilder() *GCPBuilder {
	return &GCPBuilder{}
}

func (b *GCPBuilder) setCapx(managed bool) {
	b.capxProvider = "gcp"
	b.capxVersion = "v1.6.1"
	b.capxImageVersion = "1.6.1-0.1"
	b.capxName = "capg"
	b.capxManaged = managed
	b.csiNamespace = "kube-system"
}

func (b *GCPBuilder) setCapxEnvVars(p ProviderParams) {
	data := map[string]interface{}{
		"type":                        "service_account",
		"project_id":                  p.Credentials["ProjectID"],
		"private_key_id":              p.Credentials["PrivateKeyID"],
		"private_key":                 p.Credentials["PrivateKey"],
		"client_email":                p.Credentials["ClientEmail"],
		"client_id":                   p.Credentials["ClientID"],
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://accounts.google.com/o/oauth2/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/" + url.QueryEscape(p.Credentials["ClientEmail"]),
	}
	jsonData, _ := json.Marshal(data)
	b.capxEnvVars = []string{
		"GCP_B64ENCODED_CREDENTIALS=" + b64.StdEncoding.EncodeToString([]byte(jsonData)),
	}
	if p.Managed {
		b.capxEnvVars = append(b.capxEnvVars, "EXP_MACHINE_POOL=true")
		b.capxEnvVars = append(b.capxEnvVars, "EXP_CAPG_GKE=true")
	}
	if p.GithubToken != "" {
		b.capxEnvVars = append(b.capxEnvVars, "GITHUB_TOKEN="+p.GithubToken)
	}
}

func (b *GCPBuilder) setSC(p ProviderParams) {
	if (p.StorageClass.Parameters != commons.SCParameters{}) {
		b.scParameters = p.StorageClass.Parameters
	}

	b.scProvisioner = "pd.csi.storage.gke.io"

	if b.scParameters.Type == "" {
		if p.StorageClass.Class == "premium" {
			b.scParameters.Type = "pd-ssd"
		} else {
			b.scParameters.Type = "pd-standard"
		}
	}

	if p.StorageClass.EncryptionKey != "" {
		b.scParameters.DiskEncryptionKmsKey = p.StorageClass.EncryptionKey
	}
}

func (b *GCPBuilder) getProvider() Provider {
	return Provider{
		capxProvider:     b.capxProvider,
		capxVersion:      b.capxVersion,
		capxImageVersion: b.capxImageVersion,
		capxManaged:      b.capxManaged,
		capxName:         b.capxName,
		capxEnvVars:      b.capxEnvVars,
		scParameters:     b.scParameters,
		scProvisioner:    b.scProvisioner,
		csiNamespace:     b.csiNamespace,
	}
}

func (b *GCPBuilder) installCloudProvider(n nodes.Node, k string, privateParams PrivateParams) error {
	return nil
}

func (b *GCPBuilder) installCSI(n nodes.Node, k string, privateParams PrivateParams) error {
	var c string
	var err error
	var cmd exec.Cmd

	// Create CSI secret in CSI namespace
	secret, _ := b64.StdEncoding.DecodeString(strings.Split(b.capxEnvVars[0], "GCP_B64ENCODED_CREDENTIALS=")[1])
	c = "kubectl --kubeconfig " + k + " -n " + b.csiNamespace + " create secret generic cloud-sa --from-literal=cloud-sa.json='" + string(secret) + "'"
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return errors.Wrap(err, "failed to create CSI secret in CSI namespace")
	}

	csiManifests, err := getManifest(privateParams.KeosCluster.Spec.InfraProvider, "gcp-compute-persistent-disk-csi-driver.tmpl", privateParams)
	if err != nil {
		return errors.Wrap(err, "failed to get CSI driver manifests")
	}

	// Deploy CSI driver
	cmd = n.Command("kubectl", "--kubeconfig", k, "apply", "-f", "-")
	if err = cmd.SetStdin(strings.NewReader(csiManifests)).Run(); err != nil {
		return errors.Wrap(err, "failed to deploy CSI driver")
	}

	return nil
}

func (b *GCPBuilder) getRegistryCredentials(p ProviderParams, u string) (string, string, error) {
	var registryUser = "oauth2accesstoken"
	var ctx = context.Background()
	scope := "https://www.googleapis.com/auth/cloud-platform"
	key, _ := b64.StdEncoding.DecodeString(strings.Split(b.capxEnvVars[0], "GCP_B64ENCODED_CREDENTIALS=")[1])
	creds, err := google.CredentialsFromJSON(ctx, key, scope)
	if err != nil {
		return "", "", err
	}
	token, err := creds.TokenSource.Token()
	if err != nil {
		return "", "", err
	}
	return registryUser, token.AccessToken, nil
}

func (b *GCPBuilder) configureStorageClass(n nodes.Node, k string) error {
	var c string
	var err error
	var cmd exec.Cmd

	if b.capxManaged {
		// Remove annotation from default storage class
		c = "kubectl --kubeconfig " + k + ` get sc -o jsonpath='{.items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")].metadata.name}'`
		output, err := commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return errors.Wrap(err, "failed to get default storage class")
		}
		if strings.TrimSpace(output) != "" && strings.TrimSpace(output) != "No resources found" {
			c = "kubectl --kubeconfig " + k + " annotate sc " + strings.TrimSpace(output) + " " + defaultScAnnotation + "-"
			_, err = commons.ExecuteCommand(n, c, 3, 5)
			if err != nil {
				return errors.Wrap(err, "failed to remove annotation from default storage class")
			}
		}
	}

	scTemplate.Parameters = b.scParameters
	scTemplate.Provisioner = b.scProvisioner

	scBytes, err := yaml.Marshal(scTemplate)
	if err != nil {
		return err
	}
	storageClass := strings.Replace(string(scBytes), "fsType", "csi.storage.k8s.io/fstype", -1)

	cmd = n.Command("kubectl", "--kubeconfig", k, "apply", "-f", "-")
	if err = cmd.SetStdin(strings.NewReader(storageClass)).Run(); err != nil {
		return errors.Wrap(err, "failed to create default storage class")
	}

	return nil
}

func (b *GCPBuilder) internalNginx(p ProviderParams, networks commons.Networks) (bool, error) {
	var ctx = context.Background()

	secrets, _ := b64.StdEncoding.DecodeString(strings.Split(b.capxEnvVars[0], "GCP_B64ENCODED_CREDENTIALS=")[1])
	cfg := option.WithCredentialsJSON(secrets)
	computeService, err := compute.NewService(ctx, cfg)
	if err != nil {
		return false, err
	}
	if len(networks.Subnets) > 0 {
		for _, s := range networks.Subnets {
			publicSubnetID, _ := GCPFilterPublicSubnet(computeService, p.Credentials["ProjectID"], p.Region, s.SubnetId)
			if len(publicSubnetID) > 0 {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

func GCPFilterPublicSubnet(computeService *compute.Service, projectID string, region string, subnetID string) (string, error) {
	subnet, err := computeService.Subnetworks.Get(projectID, region, subnetID).Do()
	if err != nil {
		return "", err
	}
	if subnet.PrivateIpGoogleAccess {
		return "", nil
	} else {
		return subnetID, nil
	}
}

func (b *GCPBuilder) getOverrideVars(p ProviderParams, networks commons.Networks, clusterConfigSpec commons.ClusterConfigSpec) (map[string][]byte, error) {
	var overrideVars = make(map[string][]byte)

	requiredInternalNginx, err := b.internalNginx(p, networks)
	if err != nil {
		return nil, err
	}
	if requiredInternalNginx {
		overrideVars = addOverrideVar("ingress-nginx.yaml", gcpInternalIngress, overrideVars)
	}
	return overrideVars, nil
}

func (b *GCPBuilder) postInstallPhase(n nodes.Node, k string) error {
	var coreDNSPDBName = "coredns"

	c := "kubectl --kubeconfig " + kubeconfigPath + " get pdb " + coreDNSPDBName + " -n kube-system"
	_, err := commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		err = installCorednsPdb(n)
		if err != nil {
			return errors.Wrap(err, "failed to add core dns PDB")
		}
	}

	return nil
}

func (b *GCPBuilder) getCrossplaneProviderConfigContent(credentials map[string]*map[string]string, addon string, clusterName string, kubeconfigString string) (string, bool, error) {
	credentialsFound := true
	addonCredentials := credentials[addon]
	if isEmptyCredsMap(*addonCredentials, b.capxProvider) {
		credentialsFound = false
		addonCredentials = credentials["crossplane"]
	}
	gcpCredentialsMap := map[string]interface{}{
		"type":                        "service_account",
		"project_id":                  (*addonCredentials)["ProjectID"],
		"private_key_id":              (*addonCredentials)["PrivateKeyID"],
		"private_key":                 formatPrivateKey((*addonCredentials)["PrivateKey"]),
		"client_email":                (*addonCredentials)["ClientEmail"],
		"client_id":                   (*addonCredentials)["ClientID"],
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://accounts.google.com/o/oauth2/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/" + url.QueryEscape((*addonCredentials)["ClientEmail"]),
	}

	gcpCredentials, err := json.Marshal(gcpCredentialsMap)
	if err != nil {
		return "", false, err
	}
	return string(gcpCredentials), credentialsFound, nil
}

func (b *GCPBuilder) getAddons(clusterManaged bool, addonsParams map[string]*bool) []string {
	var addons []string
	addonsReference := crossplaneGKEAddons
	if !clusterManaged {
		addonsReference = crossplaneGCPAddons
	}
	for _, addon := range addonsReference {
		enabled := addonsParams[addon]
		if (enabled != nil && *enabled) || enabled == nil {
			addons = append(addons, addon)
		}
	}

	return addons
}

func (b *GCPBuilder) getCrossplaneCRManifests(keosCluster commons.KeosCluster, credentials map[string]string, workloadClusterInstallation bool, credentialsFound bool, addon string, customParams map[string]string) ([]string, map[string]string, error) {
	var manifests = []string{}
	compositionsToWait := make(map[string]string)
	params := CrossplaneGCPParams{
		ClusterName:    keosCluster.Metadata.Name,
		ExternalDomain: keosCluster.Spec.ExternalDomain,
		Addon:          addon,
		ProjectName:    credentials["ProjectID"],
		Managed:        keosCluster.Spec.ControlPlane.Managed,
	}

	switch addon {
	case "external-dns":
		manifests = append(manifests, string(gcpCRDHostedZones))
		compositionsToWait["xGCPZonesConfig"] = keosCluster.Metadata.Name + "-zones-config"
		compositionHostedZones, err := getManifest("gcp", "composition-hostedzones-gcp.tmpl", params)
		if err != nil {
			return nil, nil, err
		}
		manifests = append(manifests, compositionHostedZones)
		hostedZone, err := getManifest("gcp", "hostedzone.gcp.tmpl", params)
		if err != nil {
			return nil, nil, err
		}
		manifests = append(manifests, hostedZone)
	}

	return manifests, compositionsToWait, nil
}

func (b *GCPBuilder) setCrossplaneProviders(addons []string) {

	b.crossplaneProviders = map[string]string{
		"provider-family-gcp": "v1.7.0",
	}

	for _, addon := range addons {
		switch addon {
		case "external-dns":
			b.crossplaneProviders["provider-gcp-cloudplatform"] = "v1.7.0"
			b.crossplaneProviders["provider-gcp-dns"] = "v1.7.0"
		}
	}
}

func (b *GCPBuilder) getCrossplaneProviders(addons []string) map[string]string {
	b.setCrossplaneProviders(addons)
	return b.crossplaneProviders
}

func (b *GCPBuilder) getExternalDNSCreds(n nodes.Node, clusterName string, clientset *kubernetes.Clientset, credentials map[string]string) (map[string]string, error) {

	secret, err := clientset.CoreV1().Secrets("crossplane-system").Get(context.Background(), "sa-key-external-dns-"+clusterName+"-secret", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	externalDnsCreds := map[string]string{
		"gcp.json": string(secret.Data["private_key"]),
	}
	return externalDnsCreds, nil
}

func formatPrivateKey(key string) string {

	nk := strings.TrimSpace(key)
	nk = strings.ReplaceAll(nk, "\n", "\\n")
	formattedKey := "-----BEGIN PRIVATE KEY-----\\n" +
		nk[len("-----BEGIN PRIVATE KEY-----\\n"):len(nk)-len("\\n-----END PRIVATE KEY-----")] +
		"\\n-----END PRIVATE KEY-----\\n"

	return formattedKey
}

func (b *GCPBuilder) getAddonsReleaseInstallation(addon string) []InstallationReleases {
	switch addon {
	case "external-dns":
		return []InstallationReleases{{Provider: "google", Releases: []string{"external-dns", "private-external-dns"}}}
	}
	return []InstallationReleases{}
}

func (b *GCPBuilder) createExternalDNSCredsSecret(n nodes.Node, kubeconfigPath string, credentials map[string]string, managed bool, clusterName string) error {
	clientset, err := getClientSet(n, "", "")
	if err != nil {
		return err
	}
	newSecret := &corev1.Secret{}
	if credentials["gcp.json"] != "" {
		newSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "external-dns-creds",
			},
			Data: map[string][]byte{
				"gcp.json": []byte(credentials["gcp.json"]),
			},
		}
	} else if credentials["ProjectID"] != "" && credentials["PrivateKeyID"] != "" && credentials["PrivateKey"] != "" && credentials["ClientEmail"] != "" && credentials["ClientID"] != "" {
		data := map[string]interface{}{
			"type":                        "service_account",
			"project_id":                  credentials["ProjectID"],
			"private_key_id":              credentials["PrivateKeyID"],
			"private_key":                 credentials["PrivateKey"],
			"client_email":                credentials["ClientEmail"],
			"client_id":                   credentials["ClientID"],
			"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
			"token_uri":                   "https://accounts.google.com/o/oauth2/token",
			"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
			"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/" + url.QueryEscape(credentials["ClientEmail"]),
		}
		jsonData, _ := json.Marshal(data)
		newSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "external-dns-creds",
			},
			Data: map[string][]byte{
				"gcp.json": jsonData,
			},
		}
	} else {
		return errors.New("no credentials found to create external-dns-creds secret")
	}

	_, err = clientset.CoreV1().Secrets("external-dns").Create(context.TODO(), newSecret, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func jsonStringToMap(jsonString string) (map[string]string, error) {
	var resultMap map[string]string
	err := json.Unmarshal([]byte(jsonString), &resultMap)
	if err != nil {
		return nil, err
	}
	return resultMap, nil
}
