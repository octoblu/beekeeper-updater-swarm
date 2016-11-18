package deployer

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/context"

	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/filters"
	"github.com/docker/engine-api/types/swarm"
	De "github.com/tj/go-debug"
)

var debug = De.Debug("beekeeper-updater-swarm:deployer")

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
	filters.Add("label", "octoblu.beekeeper.update")
	options := types.ServiceListOptions{
		Filter: filters,
	}
	ctx := context.Background()
	services, err := deployer.dockerClient.ServiceList(ctx, options)
	if err != nil {
		return err
	}
	for _, service := range services {
		shouldUpdate, err := deployer.shouldUpdateService(service)
		if err != nil {
			return err
		}
		debug("found service %s", getCurrentDockerURL(service))
		if shouldUpdate {
			err = deployer.updateService(service)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (deployer *Deployer) shouldUpdateService(service swarm.Service) (bool, error) {
	if getCurrentDockerURL(service) == "" {
		log.Println("Could not get currentDockerURL for service", service.ID)
		return false, nil
	}
	if isUpdateInProcess(service) {
		debug("Update already in progress", service.ID)
		return false, nil
	}
	return true, nil
}

func (deployer *Deployer) updateService(service swarm.Service) error {
	currentDockerURL := getCurrentDockerURL(service)
	owner, repo, _ := deployer.parseDockerURL(currentDockerURL)
	if owner == "" || repo == "" {
		return fmt.Errorf("Could not parse docker URL %v %v", currentDockerURL, service.ID)
	}
	dockerURL, err := deployer.getLatestDockerURL(owner, repo)
	if err != nil {
		return fmt.Errorf("Error getting latest docker URL for %v/%v: %v", owner, repo, err.Error())
	}
	if dockerURL == "" {
		log.Println("No latest docker url from the beekeeper service")
		return nil
	}
	if doesDockerURLMatchCurrent(dockerURL, service) {
		return nil
	}
	if !didLastUpdatePass(service) {
		debug("Last update failed", service.ID)
		if doesDockerURLMatchLast(dockerURL, service) {
			debug("Update already has been done", service.ID)
			return nil
		}
	}
	return deployer.deploy(service, dockerURL)
}

func (deployer *Deployer) deploy(service swarm.Service, dockerURL string) error {
	var err error
	dockerClient := deployer.dockerClient

	ctx := context.Background()
	updateOpts := types.ServiceUpdateOptions{}

	service.Spec.TaskTemplate.ContainerSpec.Image = dockerURL
	currentDate := time.Now().Format(time.RFC3339)
	if service.Spec.TaskTemplate.ContainerSpec.Labels == nil {
		service.Spec.TaskTemplate.ContainerSpec.Labels = map[string]string{}
	}
	service.Spec.TaskTemplate.ContainerSpec.Labels["octoblu.beekeeper.lastDockerURL"] = dockerURL
	service.Spec.TaskTemplate.ContainerSpec.Labels["octoblu.beekeeper.lastUpdatedAt"] = currentDate
	err = dockerClient.ServiceUpdate(ctx, service.ID, service.Version, service.Spec, updateOpts)
	if err != nil {
		return err
	}

	return nil
}

func (deployer *Deployer) getLatestDockerURL(owner, repo string) (string, error) {
	var metadata RequestMetadata

	url := fmt.Sprintf("%s/deployments/%s/%s/latest", deployer.beekeeperURI, owner, repo)

	debug("get latest docker url %s", url)

	res, err := http.Get(url)

	if err != nil {
		debug("got error from beekeeper-service %v", err)
		return "", err
	}

	debug("get latest: got status code %v", res.StatusCode)
	if res.StatusCode != 200 {
		return "", fmt.Errorf("Invalid response status code %v", res.StatusCode)
	}

	body, err := ioutil.ReadAll(res.Body)

	if err != nil {
		return "", err
	}

	if len(body) == 0 {
		return "", nil
	}

	err = json.Unmarshal(body, &metadata)
	if err != nil {
		return "", err
	}

	return metadata.DockerURL, nil
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

func getCurrentDockerURL(service swarm.Service) string {
	return service.Spec.TaskTemplate.ContainerSpec.Image
}

func getLastUpdatedAt(service swarm.Service) (time.Time, error) {
	lastUpdatedAt := service.Spec.TaskTemplate.ContainerSpec.Labels["octoblu.beekeeper.lastUpdatedAt"]
	return time.Parse(time.RFC3339, lastUpdatedAt)
}

func getLastDockerURL(service swarm.Service) string {
	return service.Spec.TaskTemplate.ContainerSpec.Labels["octoblu.beekeeper.lastDockerURL"]
}

func isUpdateInProcess(service swarm.Service) bool {
	return service.UpdateStatus.State == swarm.UpdateStateUpdating
}

func didLastUpdatePass(service swarm.Service) bool {
	if service.UpdateStatus.State != swarm.UpdateStatePaused {
		return true
	}
	return false
}

func doesDockerURLMatchCurrent(dockerURL string, service swarm.Service) bool {
	currentDockerURL := getCurrentDockerURL(service)
	if currentDockerURL == "" {
		return false
	}
	return dockerURL == currentDockerURL
}

func doesDockerURLMatchLast(dockerURL string, service swarm.Service) bool {
	lastDockerURL := getLastDockerURL(service)
	if lastDockerURL == "" {
		return false
	}
	return dockerURL == lastDockerURL
}
