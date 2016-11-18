package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/codegangsta/cli"
	"github.com/coreos/go-semver/semver"
	"github.com/docker/engine-api/client"
	"github.com/fatih/color"
	"github.com/octoblu/beekeeper-updater-swarm/deployer"
	De "github.com/tj/go-debug"
)

var debug = De.Debug("beekeeper-updater-swarm:main")

func main() {
	app := cli.NewApp()
	app.Name = "beekeeper-updater-swarm"
	app.Version = version()
	app.Action = run
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "docker-uri, d",
			EnvVar: "DOCKER_HOST",
			Usage:  "Docker server to deploy to",
			Value:  "unix:///var/run/docker.sock",
		},
		cli.StringFlag{
			Name:   "beekeeper-uri",
			EnvVar: "BEEKEEPER_URI",
			Usage:  "Beekeeper uri, it should include authentication.",
		},
	}
	app.Run(os.Args)
}

func run(context *cli.Context) {
	dockerURI, beekeeperURI := getOpts(context)

	dockerClient := getDockerClient(dockerURI)
	debug("running version %v", version())
	debug("BEEKEEPER_URI: %s", beekeeperURI)
	debug("DOCKER_HOST: %s", dockerURI)
	theDeployer := deployer.New(dockerClient, beekeeperURI)
	sigTerm := make(chan os.Signal)
	signal.Notify(sigTerm, syscall.SIGTERM)

	sigTermReceived := false

	go func() {
		<-sigTerm
		fmt.Println("SIGTERM received, waiting to exit")
		sigTermReceived = true
	}()

	for {
		if sigTermReceived {
			fmt.Println("I'll be back.")
			os.Exit(0)
		}

		debug("theDeployer.Run()")
		err := theDeployer.Run()
		if err != nil {
			log.Panic("Run error", err)
		}
		time.Sleep(60 * time.Second)
	}
}

func getOpts(context *cli.Context) (string, string) {
	dockerURI := context.String("docker-uri")
	beekeeperURI := context.String("beekeeper-uri")

	if dockerURI == "" || beekeeperURI == "" {
		cli.ShowAppHelp(context)

		if dockerURI == "" {
			color.Red("  Missing required flag --docker-uri or DOCKER_HOST")
		}
		if beekeeperURI == "" {
			color.Red("  Missing required flag --beekeeper-uri or BEEKEEPER_URI")
		}
		os.Exit(1)
	}

	return dockerURI, beekeeperURI
}

func getDockerClient(dockerURI string) client.APIClient {
	defaultHeaders := map[string]string{"User-Agent": "beekeeper-updater-swarm"}

	dockerClient, err := client.NewClient(dockerURI, "v1.24", nil, defaultHeaders)
	if err != nil {
		panic(err)
	}
	return dockerClient
}

// ParseHost verifies that the given host strings is valid.
func ParseHost(host string) (string, string, string, error) {
	protoAddrParts := strings.SplitN(host, "://", 2)
	if len(protoAddrParts) == 1 {
		return "", "", "", fmt.Errorf("unable to parse docker host `%s`", host)
	}

	var basePath string
	proto, addr := protoAddrParts[0], protoAddrParts[1]
	if proto == "tcp" {
		parsed, err := url.Parse("tcp://" + addr)
		if err != nil {
			return "", "", "", err
		}
		addr = parsed.Host
		basePath = parsed.Path
	}
	return proto, addr, basePath, nil
}

func version() string {
	version, err := semver.NewVersion(VERSION)
	if err != nil {
		errorMessage := fmt.Sprintf("Error with version number: %v", VERSION)
		log.Panicln(errorMessage, err.Error())
	}
	return version.String()
}
