package deployer

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
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
	tags         string
}

// RequestMetadata is the metadata of the request
type RequestMetadata struct {
	DockerURL string `json:"docker_url"`
}

// New constructs a new deployer instance
func New(dockerClient client.APIClient, beekeeperURI, tags string) *Deployer {
	return &Deployer{
		dockerClient: dockerClient,
		beekeeperURI: beekeeperURI,
		tags:         tags,
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
			debug("error updating service %s - %v", service, err)
			continue
		}
		debug("found service %s", getCurrentDockerURL(service))
		if shouldUpdate {
			err = deployer.updateService(service)
			if err != nil {
				debug("error updating service %s - %v", service, err)
				continue
			}
		}
	}
	return nil
}

func (deployer *Deployer) shouldUpdateService(service swarm.Service) (bool, error) {
	if service.Spec.Labels["octoblu.beekeeper.update"] != "true" {
		debug("beekeeper update label != true")
		return false, nil
	}
	if getCurrentDockerURL(service) == "" {
		debug("Could not get currentDockerURL for service", service.ID)
		return false, nil
	}
	if isUpdateInProcess(service) {
		debug("Update already in progress, skipping update", service.ID)
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
	dockerURL, err := deployer.getLatestDeployment(owner, repo)
	if err != nil {
		return fmt.Errorf("Error getting latest docker URL for %v/%v: %v", owner, repo, err.Error())
	}
	if dockerURL == "" {
		debug("No latest docker url from the beekeeper service")
		return nil
	}
	if doesDockerURLMatchCurrent(dockerURL, service) {
		debug("docker url is the same")
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
	if service.Spec.Labels == nil {
		service.Spec.Labels = make(map[string]string)
	}
	service.Spec.Labels["octoblu.beekeeper.lastDockerURL"] = dockerURL
	service.Spec.Labels["octoblu.beekeeper.lastUpdatedAt"] = currentDate
	debug("About to deploy %s at %s", dockerURL, currentDate)
	service.Spec.UpdateConfig.Parallelism = getUpdateParallelism(service)
	service.Spec.UpdateConfig.FailureAction = "pause"
	err = dockerClient.ServiceUpdate(ctx, service.ID, service.Version, service.Spec, updateOpts)
	if err != nil {
		return err
	}

	return nil
}

func (deployer *Deployer) getBeekeeperURL(owner, repo string) (string, error) {
	url := fmt.Sprintf("%s/deployments/%s/%s/latest", deployer.beekeeperURI, owner, repo)
	u, err := url.Parse(url)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if deployer.tags != "" {
		q.Set("tags", deployer.tags)
	}
	u.RawQuery = q.Encode()
	return fmt.Sprint(u), nil
}

func (deployer *Deployer) getLatestDeployment(owner, repo string) (string, error) {
	var metadata RequestMetadata

	u, err := deployer.getBeekeeperURL(owner, repo)
	if err != nil {
		return "", err
	}

	debug("get latest docker url %s", u)

	res, err := http.Get(u)

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

func getRealDockerURL(dockerURL string) string {
	return strings.Split(dockerURL, "@")[0]
}

func (deployer *Deployer) parseDockerURL(dockerURL string) (string, string, string) {
	var owner, repo, tag string
	realDockerURL := getRealDockerURL(dockerURL)
	dockerURLParts := strings.Split(realDockerURL, ":")

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

func getUpdateParallelism(service swarm.Service) uint64 {
	if service.Spec.Mode.Replicated == nil {
		return 1
	}
	if service.Spec.Mode.Replicated.Replicas == nil {
		return 1
	}
	replicas := *service.Spec.Mode.Replicated.Replicas
	return (replicas / 10) + 1
}

func getCurrentDockerURL(service swarm.Service) string {
	return getRealDockerURL(service.Spec.TaskTemplate.ContainerSpec.Image)
}

func getLastUpdatedAt(service swarm.Service) (time.Time, error) {
	if service.Spec.Labels == nil {
		return time.Now(), nil
	}
	lastUpdatedAt := service.Spec.Labels["octoblu.beekeeper.lastUpdatedAt"]
	return time.Parse(time.RFC3339, lastUpdatedAt)
}

func getLastDockerURL(service swarm.Service) string {
	if service.Spec.Labels == nil {
		return ""
	}
	return service.Spec.Labels["octoblu.beekeeper.lastDockerURL"]
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
	debug("currentDockerURL = %s, dockerURL = %s", currentDockerURL, dockerURL)
	if currentDockerURL == "" {
		return false
	}
	return dockerURL == currentDockerURL
}

func doesDockerURLMatchLast(dockerURL string, service swarm.Service) bool {
	lastDockerURL := getLastDockerURL(service)
	debug("lastDockerURL = %s, dockerURL = %s", lastDockerURL, dockerURL)
	if lastDockerURL == "" {
		return false
	}
	return dockerURL == lastDockerURL
}
