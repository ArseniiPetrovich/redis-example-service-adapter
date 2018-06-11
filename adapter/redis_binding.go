package adapter

import (
	"errors"
	"fmt"
	"log"
	"regexp"

	"github.com/pivotal-cf/on-demand-services-sdk/bosh"
	"github.com/pivotal-cf/on-demand-services-sdk/serviceadapter"
)

type Binder struct {
	StderrLogger *log.Logger
}

func (b Binder) CreateBinding(bindingID string, deploymentTopology bosh.BoshVMs, manifest bosh.BoshManifest, requestParams serviceadapter.RequestParameters, secrets serviceadapter.ManifestSecrets) (serviceadapter.Binding, error) {
	ctx := requestParams.ArbitraryContext()
	platform := requestParams.Platform()
	if len(ctx) == 0 || platform == "" || platform != "cloudfoundry" {
		b.StderrLogger.Println("Non Cloud Foundry platform (or pre OSBAPI 2.13) detected")
	}
	redisHost, err := getRedisHost(deploymentTopology)
	if err != nil {
		b.StderrLogger.Println(err.Error())
		return serviceadapter.Binding{}, errors.New("")
	}

	var generatedSecret string
	if secrets != nil {
		var ok bool
		generatedSecret, ok = secrets["secret_pass"]
		if !ok {
			err := errors.New("manifest wasn't correctly interpolated: missing value for `secret_pass`")
			b.StderrLogger.Println(err.Error())
			return serviceadapter.Binding{}, err
		}
	}
	var secretFromConfigStore string
	if redisPlanProperties(manifest)["secret"] != nil {
		pathWithParens, ok := redisPlanProperties(manifest)["secret"].(string)
		if !ok {
			err := errors.New("secret in manifest was not a string. expecting a credhub ref string")
			b.StderrLogger.Println(err.Error())
			return serviceadapter.Binding{}, err
		}
		re := regexp.MustCompile(`^\(\(([^()]+)\)\)$`)
		match := re.FindAllStringSubmatch(pathWithParens, -1)
		if len(match) != 1 || len(match[0]) != 2 {
			err := fmt.Errorf("expecting a credhub ref string with format ((xxx)), but got: %s", pathWithParens)
			b.StderrLogger.Println(err.Error())
			return serviceadapter.Binding{}, err
		}
		path := match[0][1]
		secretFromConfigStore, ok = secrets[path]
		if !ok {
			err := fmt.Errorf("secret '%s' not present in manifest secrets passed to bind", path)
			b.StderrLogger.Println(err.Error())
			return serviceadapter.Binding{}, err
		}
	}
	return serviceadapter.Binding{
		Credentials: map[string]interface{}{
			"host":             redisHost,
			"port":             RedisServerPort,
			"generated_secret": generatedSecret,
			"password":         redisPlanProperties(manifest)["password"].(string),
			"secret":           secretFromConfigStore,
		},
	}, nil
}

func (b Binder) DeleteBinding(bindingID string, deploymentTopology bosh.BoshVMs, manifest bosh.BoshManifest, requestParams serviceadapter.RequestParameters) error {
	return nil
}

func getRedisHost(deploymentTopology bosh.BoshVMs) (string, error) {
	if len(deploymentTopology) != 1 {
		return "", fmt.Errorf("expected 1 instance group in the Redis deployment, got %d", len(deploymentTopology))
	}

	redisServerIPs := deploymentTopology["redis-server"]
	if len(redisServerIPs) != 1 {
		return "", fmt.Errorf("expected redis-server instance group to have only 1 instance, got %d", len(redisServerIPs))
	}
	return redisServerIPs[0], nil

}
