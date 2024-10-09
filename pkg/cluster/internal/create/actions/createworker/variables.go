package createworker

var (
	//External-dns variables
	externalDNSAddonName    = "external-dns"
	externalDNSNamespace    = "external-dns"
	externalDNSCredsSecrets = "external-dns-creds"
	externalDNSImageTag     = "v0.13.6"
)

var (
	//Crossplane variables
	crossplaneAddonName = "crossplane"
	crossplaneNamespace = "crossplane-system"

	// Crossplane file variables
	crossplane_folder_path              = "/kind/cache"
	crossplane_directoy_path            = "/kind/crossplane"
	crossplane_provider_creds_file_base = "/kind/crossplane/crossplane-"
	crossplane_provider_config_file     = "/kind/crossplane/crossplane-provider-creds.yaml"
	crossplane_custom_creds_file        = "/kind/crossplane/crossplane-custom-creds.yaml"
	crossplane_crs_file_local_base      = "/kind/crossplane/crossplane-crs-local"
	crossplane_crs_file_workload_base   = "/kind/crossplane/crossplane-crs-workload"
)
