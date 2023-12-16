package createworker

import (
	_ "embed"
	"strings"

	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/commons"
	"sigs.k8s.io/kind/pkg/errors"
)

var (
	crossplane_folder_path = "/kind/cache"
	crossplane_providers   = map[string]string{
		"provider-family-aws": "v0.46.0",
		"provider-aws-ec2":    "v0.46.0",
	}
)

//go:embed files/crossplane/crossplane-offline-preflights.yaml
var crossplane_offline_preflights string

type CrossplaneProviderParams struct {
	Provider string
	Package  string
	Image    string
}

func configureCrossPlaneProviders(n nodes.Node, kubeconfigpath string, keosRegUrl string) error {
	c_base := "up xpkg xp-extract xpkg.upbound.io/upbound"
	for provider, version := range crossplane_providers {
		providerFile := "/kind/" + provider + ".yaml"

		c := c_base + "/" + provider + ":" + version
		_, err := commons.ExecuteCommand(n, c)
		if err != nil {
			return errors.Wrap(err, "failed to extract crossplane provider file")
		}
		c = "mv out.gz " + crossplane_folder_path + "/" + provider + ".gz"
		_, err = commons.ExecuteCommand(n, c)
		if err != nil {
			return errors.Wrap(err, "failed to move crossplane provider file")
		}
		c = "chmod 644 " + crossplane_folder_path + "/" + provider + ".gz"
		_, err = commons.ExecuteCommand(n, c)
		if err != nil {
			return errors.Wrap(err, "failed to change crossplane provider file permissions")
		}

		params := CrossplaneProviderParams{
			Provider: provider,
			Package:  provider,
			Image:    keosRegUrl + "/upbound/" + provider + ":" + version,
		}
		providerManifest, err := getManifest("aws", "crossplane-provider.tmpl", params)
		if err != nil {
			return errors.Wrap(err, "failed to generate provider manifest "+provider)
		}
		c = "echo '" + providerManifest + "' > " + providerFile
		_, err = commons.ExecuteCommand(n, c)
		if err != nil {
			return errors.Wrap(err, "failed to create provider manifest "+provider+" file")
		}

		c = "kubectl create -f " + providerFile
		if kubeconfigpath != "" {
			c += " --kubeconfig " + kubeconfigpath
		}
		_, err = commons.ExecuteCommand(n, c)
		if err != nil {
			return errors.Wrap(err, "failed to create provider "+provider)
		}

		//ESPERAR A QUE SE CREEN LOS CONTROLLERS Y EDITAR EL IMAGEPULLPOLICY

	}
	return nil
}

func installCrossplane(n nodes.Node, kubeconfigpath string, keosRegUrl string) error {

	c := "mkdir -p " + crossplane_folder_path
	_, err := commons.ExecuteCommand(n, c)
	if err != nil {
		return errors.Wrap(err, "failed to create crossplane cache folder")
	}

	c = "kubectl create ns crossplane-system"
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return errors.Wrap(err, "failed to create ns crossplane-system")
	}

	command := []string{"apply", "-f", "-"}
	if kubeconfigpath != "" {
		command = append([]string{"--kubeconfig ", kubeconfigpath}, command...)
	}
	cmd := n.Command("kubectl", command...)
	if err := cmd.SetStdin(strings.NewReader(crossplane_offline_preflights)).Run(); err != nil {
		return errors.Wrap(err, "failed to create crossplane preflights")
	}

	c = "helm install crossplane /stratio/helm/crossplane" +
		" --namespace crossplane-system" +
		" --set image.repository=" + keosRegUrl + "/crossplane/crossplane" +
		" --set packageCache.pvc=package-cache"

	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}

	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return errors.Wrap(err, "failed to deploy crossplane Helm Chart")
	}

	c = "kubectl -n crossplane-system rollout status deploy crossplane --timeout=3m"
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return errors.Wrap(err, "failed to wait for the crossplane deployment")
	}

	c = "kubectl -n crossplane-system rollout status deploy crossplane-rbac-manager --timeout=3m"
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return errors.Wrap(err, "failed to wait for the crossplane-rbac-manager deployment")
	}

	err = configureCrossPlaneProviders(n, kubeconfigpath, keosRegUrl)
	if err != nil {
		return errors.Wrap(err, "failed to configure Crossplane Provider")
	}
	return nil

}
