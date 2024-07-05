package createworker

import (
	_ "embed"
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
	crossplane_crs_file_workload    = "/kind/crossplane-crs-workload.yaml"

	crossplane_providers = map[string]string{
		"provider-family-aws":  "v0.46.0",
		"provider-aws-ec2":     "v0.46.0",
		"provider-aws-efs":     "v0.46.0",
		"provider-aws-route53": "v0.46.0",
	}
)

type CrossplaneProviderParams struct {
	Provider string
	Package  string
	Image    string
	Private  bool
}

type CrossplaneProviderConfigParams struct {
	Secret string
}

func configureCrossPlaneProviders(n nodes.Node, kubeconfigpath string, keosRegUrl string, privateRegistry bool) error {
	for provider, version := range infra.GetCrossplaneProviders() {
		providerFile := "/kind/" + provider + ".yaml"

		params := CrossplaneProviderParams{
			Provider: provider,
			Package:  provider,
			Image:    keosRegUrl + "/upbound/" + provider + ":" + version,
			Private:  privateRegistry,
		}
		providerManifest, err := getManifest("aws", "crossplane-provider.tmpl", params)
		if err != nil {
			return errors.Wrap(err, "failed to generate provider manifest "+provider)
		}
		c := "echo '" + providerManifest + "' > " + providerFile
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return errors.Wrap(err, "failed to create provider manifest "+provider+" file")
		}

		c = "kubectl create -f " + providerFile
		if kubeconfigpath != "" {
			c += " --kubeconfig " + kubeconfigpath
		}
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return errors.Wrap(err, "failed to create provider "+provider)
		}

		c = "kubectl wait providers.pkg.crossplane.io/" + provider + " --for=condition=healthy=False --timeout=3m"
		if kubeconfigpath != "" {
			c += " --kubeconfig " + kubeconfigpath
		}
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return errors.Wrap(err, "failed to wait provider "+provider)
		}

		time.Sleep(40 * time.Second)

		c = "kubectl patch deploy -n crossplane-system " + provider + " -p '{\"spec\": {\"template\": {\"spec\":{\"containers\":[{\"name\":\"package-runtime\",\"imagePullPolicy\":\"IfNotPresent\"}]}}}}'"
		if kubeconfigpath != "" {
			c += " --kubeconfig " + kubeconfigpath
		}
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return errors.Wrap(err, "failed to patch deployment provider "+provider)
		}

	}
	return nil
}

func installCrossplane(n nodes.Node, kubeconfigpath string, keosRegUrl string, credentials map[string]string, infra *Infra, privateParams PrivateParams, workloadClusterInstallation bool, allowAllEgressNetPolPath string) (commons.KeosCluster, error) {

	c := "mkdir -p " + crossplane_folder_path
	_, err := commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to create crossplane cache folder")
	}

	c = "kubectl create ns crossplane-system"
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to create ns crossplane-system")
	}

	if workloadClusterInstallation {
		// Allow egress in CAPX's Namespace
		c = "kubectl --kubeconfig " + kubeconfigPath + " -n crossplane-system apply -f " + allowAllEgressNetPolPath
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return privateParams.KeosCluster, errors.Wrap(err, "failed to apply CAPX's NetworkPolicy in workload cluster")
		}
	}

	// RESPONSE: Cuantos paquetes entran en el configmap?
	c = "kubectl create configmap package-cache -n crossplane-system --from-file=" + crossplane_folder_path
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to create crossplane preflights")
	}

	c = "helm install crossplane /stratio/helm/crossplane" +
		" --namespace crossplane-system" +
		" --set packageCache.configMap=package-cache"

	if privateParams.Private {
		c += " --set image.repository=" + keosRegUrl + "/crossplane/crossplane"
	}

	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath +
			" --set replicas=2" +
			" --set rbacManager.replicas=2"
	}

	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to deploy crossplane Helm Chart")
	}

	c = "kubectl -n crossplane-system rollout status deploy crossplane --timeout=3m"
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to wait for the crossplane deployment")
	}

	c = "kubectl -n crossplane-system rollout status deploy crossplane-rbac-manager --timeout=3m"
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to wait for the crossplane-rbac-manager deployment")
	}

	err = configureCrossPlaneProviders(n, kubeconfigpath, keosRegUrl, privateParams.Private)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to configure Crossplane Provider")
	}

	credsContent, err := infra.getCrossplaneProviderConfigContent(credentials)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to get Crossplane Provider config content")
	}
	c = "echo '" + credsContent + "' > " + crossplane_provider_creds_file
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to create Crossplane Provider config file")
	}

	c = "kubectl create secret generic " + infra.builder.getProvider().capxProvider + "-secret -n crossplane-system --from-file=creds=" + crossplane_provider_creds_file
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to create Crossplane Provider config secret: "+infra.builder.getProvider().capxProvider+"-secret")
	}

	params := CrossplaneProviderConfigParams{
		Secret: infra.builder.getProvider().capxProvider + "-secret",
	}

	providerConfigManifest, err := getManifest("aws", "crossplane-provider-config.tmpl", params)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to generate provider config manifest ")
	}
	c = "echo '" + providerConfigManifest + "' > " + crossplane_provider_config_file
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to create provider config manifest file")
	}

	c = "kubectl create -f " + crossplane_provider_config_file
	if kubeconfigpath != "" {
		c += " --kubeconfig " + kubeconfigpath
	}
	_, err = commons.ExecuteCommand(n, c, 3, 5)
	if err != nil {
		return privateParams.KeosCluster, errors.Wrap(err, "failed to create provider config ")
	}

	keosCluster, err := createCrossplaneCustomResources(n, kubeconfigpath, credentials, infra, privateParams, workloadClusterInstallation)
	if err != nil {
		return privateParams.KeosCluster, err
	}

	return keosCluster, nil

}

func createCrossplaneCustomResources(n nodes.Node, kubeconfigpath string, credentials map[string]string, infra *Infra, privateParams PrivateParams, workloadClusterInstallation bool) (commons.KeosCluster, error) {
	crossplaneCRManifests, err := infra.getCrossplaneCRManifests(privateParams, credentials, workloadClusterInstallation)
	if err != nil {
		return privateParams.KeosCluster, err
	}
	if crossplaneCRManifests != "" {
		c := "echo '" + crossplaneCRManifests + "' > " + crossplane_crs_file
		if workloadClusterInstallation {
			c = "echo '" + crossplaneCRManifests + "' > " + crossplane_crs_file_workload
		}
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return privateParams.KeosCluster, errors.Wrap(err, "failed to create crossplane crs file")
		}

		c = "kubectl create -f " + crossplane_crs_file
		if workloadClusterInstallation {
			c = "kubectl create -f " + crossplane_crs_file_workload
		}
		if kubeconfigpath != "" {
			c += " --kubeconfig " + kubeconfigpath
		}
		_, err = commons.ExecuteCommand(n, c, 3, 5)
		if err != nil {
			return privateParams.KeosCluster, errors.Wrap(err, "failed to create crossplane crs ")
		}

		// if !workloadClusterInstallation {
		// 	keosCluster, err := infra.addCrsReferences(n, kubeconfigpath, privateParams.KeosCluster)
		// 	if err != nil {
		// 		return commons.KeosCluster{}, err
		// 	}

		// 	return keosCluster, nil

		// }
	}

	return privateParams.KeosCluster, nil
}
