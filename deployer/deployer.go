package deployer

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"golang.org/x/net/context"

	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/filters"
	De "github.com/tj/go-debug"
)

var debug = De.Debug("governator:deployer")

// Deployer watches a redis queue
// and deploys services using Etcd
type Deployer struct {
	dockerClient client.APIClient
	beekeeperURI string
}

// RequestMetadata is the metadata of the request
type RequestMetadata struct {
	DockerURL string `json:"docker_url"`
}

// New constructs a new deployer instance
func New(dockerClient client.APIClient, beekeeperURI string) *Deployer {
	return &Deployer{
		dockerClient: dockerClient,
		beekeeperURI: beekeeperURI,
	}
}

// Run watches the redis queue and starts taking action
func (deployer *Deployer) Run() error {
	filters := filters.NewArgs()
	// filters.Add("label", "octoblu.beekeeper.update")
	options := types.ServiceListOptions{
		Filter: filters,
	}
	ctx := context.Background()
	services, err := deployer.dockerClient.ServiceList(ctx, options)
	if err != nil {
		return err
	}
	for _, service := range services {
		currentDockerURL := service.Spec.TaskTemplate.ContainerSpec.Image
		owner, repo, _ := deployer.parseDockerURL(currentDockerURL)
		dockerURL := deployer.getLatestDockerURL(owner, repo)

		if dockerURL != "" && currentDockerURL != dockerURL {
			deployer.deploy(repo, dockerURL)
		}
	}
	return nil
}

func (deployer *Deployer) deploy(repo string, dockerURL string) error {
	var err error
	dockerClient := deployer.dockerClient

	ctx := context.Background()
	updateOpts := types.ServiceUpdateOptions{}

	service, _, err := dockerClient.ServiceInspectWithRaw(ctx, repo)
	if err != nil {
		return err
	}

	service.Spec.TaskTemplate.ContainerSpec.Image = dockerURL

	err = dockerClient.ServiceUpdate(ctx, service.ID, service.Version, service.Spec, updateOpts)
	if err != nil {
		return err
	}

	return nil
}

func (deployer *Deployer) getLatestDockerURL(owner, repo string) string {
	var metadata RequestMetadata

	url := fmt.Sprintf("%s/deployments/%s/%s/latest", deployer.beekeeperURI, owner, repo)

	res, err := http.Get(url)

	if err != nil {
		panic(err.Error())
	}

	body, err := ioutil.ReadAll(res.Body)

	if err != nil {
		panic(err.Error())
	}
	err = json.Unmarshal(body, &metadata)

	if err != nil {
		panic(err.Error())
	}

	return metadata.DockerURL
}

func (deployer *Deployer) parseDockerURL(dockerURL string) (string, string, string) {
	var owner, repo, tag string
	dockerURLParts := strings.Split(dockerURL, ":")

	if len(dockerURLParts) != 2 {
		return "", "", ""
	}

	if dockerURLParts[1] != "" {
		tag = dockerURLParts[1]
	}

	projectParts := strings.Split(dockerURLParts[0], "/")

	if len(projectParts) == 2 {
		owner = projectParts[0]
		repo = projectParts[1]
	} else if len(projectParts) == 3 {
		owner = projectParts[1]
		repo = projectParts[2]
	} else {
		return "", "", ""
	}

	return owner, repo, tag
}
