package createworker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/image/v5/types"
	"sigs.k8s.io/kind/pkg/errors"
)

// tagListOutput is the output format of (skopeo list-tags), primarily so that we can format it with a simple json.MarshalIndent.
type tagListOutput struct {
	Repository string `json:",omitempty"`
	Tags       []string
}

func parseDockerRepositoryReference(refString string) (types.ImageReference, error) {
	fmt.Println(">>> parseDockerRepositoryReference")
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
	fmt.Println(">>> listDockerRepoTags")

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
