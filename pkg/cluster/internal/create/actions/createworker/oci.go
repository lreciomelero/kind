package createworker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/image/v5/types"
	"gopkg.in/yaml.v3"
	"sigs.k8s.io/kind/pkg/errors"
)

var release_pattern = "[0-9]{1,2}.[0-9]{1,3}.[0-9]{1,3}$"
var prerelease_pattern = "-[0-9a-f]{7}$"
var milestone_pattern = "-M\\d+$"
var pr_pattern = "-PR[0-9]{3,4}-SNAPSHOT$"
var snapshot_pattern = "-SNAPSHOT$"

var versions = map[string][]string{
	release_pattern:    {},
	prerelease_pattern: {},
	milestone_pattern:  {},
	pr_pattern:         {},
	snapshot_pattern:   {},
}

type Index struct {
	Entries map[string][]ChartEntry `yaml:"entries"`
}

type ChartEntry struct {
	Version string `yaml:"version"`
}

func getLastChartVersion(helmRepoCreds HelmRegistry) (string, error) {
	fmt.Println(">>> getLastChartVersion")
	if strings.HasPrefix(helmRepoCreds.URL, "oci://") || strings.HasPrefix(helmRepoCreds.URL, "docker://") {

		if url, ok := strings.CutPrefix(helmRepoCreds.URL, "oci"); ok {
			helmRepoCreds.URL = "docker" + url
		}
		return getLastChartVersionFromContainerReg(helmRepoCreds)
	}
	return getLastChartVersionByIndex(helmRepoCreds)

}

func getLastChartVersionFromContainerReg(helmRepoCreds HelmRegistry) (string, error) {
	dockerAuthConfig := types.DockerAuthConfig{
		Username: helmRepoCreds.User,
		Password: helmRepoCreds.Pass,
	}
	sys := types.SystemContext{
		DockerAuthConfig: &dockerAuthConfig,
	}
	_, tags, err := listDockerRepoTags(context.Background(), &sys, helmRepoCreds.URL+"/cluster-operator")
	if err != nil {
		return "", err
	}
	return getLastVersion(tags)
}

func getLastChartVersionByIndex(helmRepoCreds HelmRegistry) (string, error) {

	url := helmRepoCreds.URL + "/index.yaml"
	fmt.Println("url: " + url)
	resp, err := http.Get(url)
	if err != nil {
		return "", errors.Wrap(err, "Error getting index: ")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "Error reading response: ")
	}

	var index Index
	if err := yaml.Unmarshal(body, &index); err != nil {
		return "", errors.Wrap(err, "Error decoding respose: ")
	}

	entries := index.Entries["cluster-operator"]
	tags := make([]string, 0)
	for _, entry := range entries {
		tags = append(tags, entry.Version)
	}

	return getLastVersion(tags)
}

func getLastVersion(tags []string) (string, error) {
	filterTags(tags)

	if len(versions[release_pattern]) != 0 {
		return getVersion(versions[release_pattern]), nil
	} else if len(versions[prerelease_pattern]) != 0 {
		return getVersion(versions[prerelease_pattern]), nil
	} else if len(versions[milestone_pattern]) != 0 {
		return getVersion(versions[milestone_pattern]), nil
	} else if len(versions[snapshot_pattern]) != 0 {
		return getVersion(versions[snapshot_pattern]), nil
	} else if len(versions[pr_pattern]) != 0 {
		return getVersion(versions[pr_pattern]), nil
	}

	return "", errors.New("No chart version matching the patterns defined by Stratio has been found.")
}

func getVersion(tags []string) string {
	sort.Strings(tags)
	return tags[0]
}

func filterTags(tags []string) {
	for _, tag := range tags {
		for reg := range versions {
			if regexp.MustCompile(reg).MatchString(tag) {
				versions[reg] = append(versions[reg], tag)
				break
			}
		}
	}
}

func parseDockerRepositoryReference(refString string) (types.ImageReference, error) {
	if !strings.HasPrefix(refString, docker.Transport.Name()+"://") {
		return nil, fmt.Errorf("docker: image reference %s does not start with %s://", refString, docker.Transport.Name())
	}

	_, dockerImageName, hasColon := strings.Cut(refString, ":")
	if !hasColon {
		return nil, fmt.Errorf(`Invalid image name "%s", expected colon-separated transport:reference`, refString)
	}
	ref, err := reference.ParseNormalizedNamed(strings.TrimPrefix(dockerImageName, "//"))
	if err != nil {
		return nil, err
	}

	if !reference.IsNameOnly(ref) {
		return nil, errors.New(`No tag or digest allowed in reference`)
	}

	// Checks ok, now return a reference. This is a hack because the tag listing code expects a full image reference even though the tag is ignored
	return docker.NewReference(reference.TagNameOnly(ref))
}

// List the tags from a repository contained in the imgRef reference. Any tag value in the reference is ignored
func listDockerTags(ctx context.Context, sys *types.SystemContext, imgRef types.ImageReference) (string, []string, error) {
	repositoryName := imgRef.DockerReference().Name()

	tags, err := docker.GetRepositoryTags(ctx, sys, imgRef)
	if err != nil {
		return ``, nil, fmt.Errorf("Error listing repository tags: %w", err)
	}
	return repositoryName, tags, nil
}

// return the tagLists from a docker repo
func listDockerRepoTags(ctx context.Context, sys *types.SystemContext, userInput string) (repositoryName string, tagListing []string, err error) {

	// Do transport-specific parsing and validation to get an image reference
	imgRef, err := parseDockerRepositoryReference(userInput)
	if err != nil {
		return
	}
	retryOpt := retry.RetryOptions{
		MaxRetry:         5,
		IsErrorRetryable: func(err error) bool { return true },
		Delay:            5 * time.Second,
	}
	if err = retry.IfNecessary(ctx, func() error {
		repositoryName, tagListing, err = listDockerTags(ctx, sys, imgRef)
		return err
	}, &retryOpt); err != nil {
		return
	}
	return
}
