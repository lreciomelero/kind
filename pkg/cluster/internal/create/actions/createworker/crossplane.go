package createworker

import (
	_ "embed"
	"strings"
	"time"

	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/commons"
	"sigs.k8s.io/kind/pkg/errors"
)

var (
	crossplane_folder_path          = "/kind/cache"
	crossplane_provider_creds_file  = "/kind/crossplane-provider-creds.txt"
	crossplane_provider_config_file = "/kind/crossplane-provider-creds.yaml"
	crossplane_crs_file             = "/kind/crossplane-crs.yaml"

	crossplane_providers = map[string]string{
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

type CrossplaneProviderConfigParams struct {
	Secret string
}

func configureCrossPlaneProviders(n nodes.Node, kubeconfigpath string, keosRegUrl string) error {
	// c_base := "up xpkg xp-extract xpkg.upbound.io/upbound"
	for provider, version := range crossplane_providers {
		providerFile := "/kind/" + provider + ".yaml"

		// c := c_base + "/" + provider + ":" + version
		// _, err := commons.ExecuteCommand(n, c)
		// if err != nil {
		// 	return errors.Wrap(err, "failed to extract crossplane provider file")
		// }
		// c = "mv out.gz " + crossplane_folder_path + "/" + provider + ".gz"
		// _, err = commons.ExecuteCommand(n, c)
		// if err != nil {
		// 	return errors.Wrap(err, "failed to move crossplane provider file")
		// }
		// c = "chmod 644 " + crossplane_folder_path + "/" + provider + ".gz"
		// _, err = commons.ExecuteCommand(n, c)
		// if err != nil {
		// 	return errors.Wrap(err, "failed to change crossplane provider file permissions")
		// }

		params := CrossplaneProviderParams{
			Provider: provider,
			Package:  provider,
			Image:    keosRegUrl + "/upbound/" + provider + ":" + version,
		}
		providerManifest, err := getManifest("aws", "crossplane-provider.tmpl", params)
		if err != nil {
			return errors.Wrap(err, "failed to generate provider manifest "+provider)
		}
		c := "echo '" + providerManifest + "' > " + providerFile
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

		c = "kubectl wait providers.pkg.crossplane.io/" + provider + " --for=condition=healthy=False --timeout=3m"
		if kubeconfigpath != "" {
			c += " --kubeconfig " + kubeconfigpath
		}
		_, err = commons.ExecuteCommand(n, c)
		if err != nil {
			return errors.Wrap(err, "failed to wait provider "+provider)
		}

		time.Sleep(40 * time.Second)

		c = "kubectl patch deploy -n crossplane-system " + provider + " -p '{\"spec\": {\"template\": {\"spec\":{\"containers\":[{\"name\":\"package-runtime\",\"imagePullPolicy\":\"IfNotPresent\"}]}}}}'"
		_, err = commons.ExecuteCommand(n, c)
		if err != nil {
			return errors.Wrap(err, "failed to patch deployment provider "+provider)
		}

	}
	return nil
}

func installCrossplane(n nodes.Node, kubeconfigpath string, keosRegUrl string, credentials map[string]string, infra *Infra, offlineParams OfflineParams) (commons.KeosCluster, error) {

	c := "mkdir -p " + crossplane_folder_path
	_, err := commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create crossplane cache folder")
	}

	c = "kubectl create ns crossplane-system"
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create ns crossplane-system")
	}

	command := []string{"apply", "-f", "-"}
	if kubeconfigpath != "" {
		command = append([]string{"--kubeconfig ", kubeconfigpath}, command...)
	}
	cmd := n.Command("kubectl", command...)
	if err := cmd.SetStdin(strings.NewReader(crossplane_offline_preflights)).Run(); err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create crossplane preflights")
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
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to deploy crossplane Helm Chart")
	}

	c = "kubectl -n crossplane-system rollout status deploy crossplane --timeout=3m"
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to wait for the crossplane deployment")
	}

	c = "kubectl -n crossplane-system rollout status deploy crossplane-rbac-manager --timeout=3m"
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to wait for the crossplane-rbac-manager deployment")
	}

	err = configureCrossPlaneProviders(n, kubeconfigpath, keosRegUrl)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to configure Crossplane Provider")
	}

	credsContent, err := infra.getCrossplaneProviderConfigContent(credentials)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to get Crossplane Provider config content")
	}
	c = "echo '" + credsContent + "' > " + crossplane_provider_creds_file
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create Crossplane Provider config file")
	}

	c = "kubectl create secret generic " + infra.builder.getProvider().capxProvider + "-secret -n crossplane-system --from-file=creds=" + crossplane_provider_creds_file
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create Crossplane Provider config secret: "+infra.builder.getProvider().capxProvider+"-secret")
	}

	params := CrossplaneProviderConfigParams{
		Secret: infra.builder.getProvider().capxProvider + "-secret",
	}

	providerConfigManifest, err := getManifest("aws", "crossplane-provider-config.tmpl", params)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to generate provider config manifest ")
	}
	c = "echo '" + providerConfigManifest + "' > " + crossplane_provider_config_file
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create provider config manifest file")
	}

	c = "kubectl create -f " + crossplane_provider_config_file
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create provider config ")
	}

	keosCluster, err := createCrossplaneCustomResources(n, kubeconfigpath, credentials, infra, offlineParams)
	if err != nil {
		return offlineParams.KeosCluster, err
	}

	return keosCluster, nil

}

func createCrossplaneCustomResources(n nodes.Node, kubeconfigpath string, credentials map[string]string, infra *Infra, offlineParams OfflineParams) (commons.KeosCluster, error) {
	crossplaneCRManifests, err := infra.getCrossplaneCRManifests(offlineParams, credentials)
	if err != nil {
		return offlineParams.KeosCluster, err
	}
	c := "echo '" + crossplaneCRManifests + "' > " + crossplane_crs_file
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create crossplane crs file")
	}

	c = "kubectl create -f " + crossplane_crs_file
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c)
	if err != nil {
		return offlineParams.KeosCluster, errors.Wrap(err, "failed to create crossplane crs ")
	}

	keosCluster, err := infra.addCrsReferences(n, kubeconfigpath, offlineParams.KeosCluster)
	if err != nil {
		return commons.KeosCluster{}, err
	}

	return keosCluster, nil
}
