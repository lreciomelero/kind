package cluster

import (
	"bytes"
	"embed"
	"io/ioutil"
	"path/filepath"
	"strings"
	"text/template"

	"sigs.k8s.io/kind/pkg/commons"
)

//go:embed offline/*
var clusterConfig embed.FS

type RegistryParams struct {
	Url  string
	User string
	Pass string
}

func getConfigFile(keosCluster *commons.KeosCluster, clusterCredentials commons.ClusterCredentials) (string, error) {
	registryParams := RegistryParams{}

	var tpl bytes.Buffer
	funcMap := template.FuncMap{
		"hostname": func(s string) string {
			return strings.Split(s, "/")[0]
		},
	}

	templatePath := filepath.Join("offline", "offlineconfig.tmpl")
	t, err := template.New("").Funcs(funcMap).ParseFS(clusterConfig, templatePath)
	if err != nil {
		return "", err
	}
	for _, registry := range keosCluster.Spec.DockerRegistries {
		if registry.KeosRegistry {
			registryParams.Url = registry.URL
			registryParams.User = clusterCredentials.KeosRegistryCredentials["User"]
			registryParams.Pass = clusterCredentials.KeosRegistryCredentials["Pass"]
			break
		}
	}
	err = t.ExecuteTemplate(&tpl, "offlineconfig.tmpl", registryParams)
	if err != nil {
		return "", err
	}
	tempFile, err := ioutil.TempFile("", "configfile")
	if err != nil {
		return "", err
	}
	defer tempFile.Close()

	_, err = tempFile.WriteString(tpl.String())
	if err != nil {
		return "", err
	}
	return tempFile.Name(), nil
}
