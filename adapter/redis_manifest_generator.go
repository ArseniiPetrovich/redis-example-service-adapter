package adapter

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/pivotal-cf/on-demand-services-sdk/bosh"
	"github.com/pivotal-cf/on-demand-services-sdk/serviceadapter"
)

const (
	RedisServerJobName                = "redis-server"
	RedisServerPersistencePropertyKey = "persistence"
	RedisServerPort                   = 6379
)

var CurrentPasswordGenerator = randomPasswordGenerator

type ManifestGenerator struct {
	StderrLogger *log.Logger
}

func findIllegalArbitraryParams(arbitraryParams map[string]interface{}) []string {
	var illegalParams []string
	for k, _ := range arbitraryParams {
		if k != "maxclients" {
			illegalParams = append(illegalParams, k)
		}
	}
	return illegalParams
}

func (m ManifestGenerator) GenerateManifest(serviceDeployment serviceadapter.ServiceDeployment, plan serviceadapter.Plan, requestParams serviceadapter.RequestParameters, previousManifest *bosh.BoshManifest, previousPlan *serviceadapter.Plan) (bosh.BoshManifest, error) {
	arbitraryParameters := requestParams.ArbitraryParams()
	illegalArbParams := findIllegalArbitraryParams(arbitraryParameters)
	if len(illegalArbParams) != 0 {
		return bosh.BoshManifest{}, fmt.Errorf("unsupported parameter(s) for this service plan: %s", strings.Join(illegalArbParams, ", "))
	}

	if previousManifest != nil {
		if err := m.validUpgradePath(*previousManifest, serviceDeployment.Releases); err != nil {
			return bosh.BoshManifest{}, err
		}
	}

	stemcellAlias := "only-stemcell"

	redisServerInstanceGroup := findRedisServerInstanceGroup(plan)
	if redisServerInstanceGroup == nil {
		m.StderrLogger.Println(fmt.Sprintf("no %s instance group definition found", RedisServerJobName))
		return bosh.BoshManifest{}, errors.New("Contact your operator, service configuration issue occurred")
	}

	networks := []bosh.Network{}
	for _, network := range redisServerInstanceGroup.Networks {
		networks = append(networks, bosh.Network{Name: network})
	}

	redisProperties, err := m.redisServerProperties(serviceDeployment.DeploymentName, plan.Properties, arbitraryParameters, previousManifest)
	if err != nil {
		return bosh.BoshManifest{}, err
	}

	releases := []bosh.Release{}
	for _, release := range serviceDeployment.Releases {
		releases = append(releases, bosh.Release{
			Name:    release.Name,
			Version: release.Version,
		})
	}

	jobs, err := gatherJobs(serviceDeployment.Releases)
	if err != nil {
		return bosh.BoshManifest{}, err
	}

	return bosh.BoshManifest{
		Name:     serviceDeployment.DeploymentName,
		Releases: releases,
		Stemcells: []bosh.Stemcell{
			{
				Alias:   stemcellAlias,
				OS:      serviceDeployment.Stemcell.OS,
				Version: serviceDeployment.Stemcell.Version,
			},
		},
		InstanceGroups: []bosh.InstanceGroup{
			{
				Name:               RedisServerJobName,
				Instances:          redisServerInstanceGroup.Instances,
				Jobs:               jobs,
				VMType:             redisServerInstanceGroup.VMType,
				VMExtensions:       redisServerInstanceGroup.VMExtensions,
				PersistentDiskType: redisServerInstanceGroup.PersistentDiskType,
				Stemcell:           stemcellAlias,
				Networks:           networks,
				AZs:                redisServerInstanceGroup.AZs,
				Properties:         redisProperties,
			},
		},
		Update:     generateUpdateBlock(plan.Update),
		Properties: map[string]interface{}{},
	}, nil
}

func randomPasswordGenerator() (string, error) {
	length := 20
	randomBytes := make([]byte, length)
	_, err := rand.Read(randomBytes)
	if err != nil {
		log.Printf("Error generating random bytes, %v", err)
		return "", err
	}
	randomStringBytes := make([]byte, base64.StdEncoding.EncodedLen(len(randomBytes)))
	base64.StdEncoding.Encode(randomStringBytes, randomBytes)
	return string(randomStringBytes), nil
}

func findRedisServerInstanceGroup(plan serviceadapter.Plan) *serviceadapter.InstanceGroup {
	for _, instanceGroup := range plan.InstanceGroups {
		if instanceGroup.Name == RedisServerJobName {
			return &instanceGroup
		}
	}
	return nil
}

var versionRegexp = regexp.MustCompile(`^(\d+)(?:\.(\d+))?(?:\+dev\.(\d+))?$`)

func parseReleaseVersion(versionString string) (int, int, int, error) {
	submatches := versionRegexp.FindStringSubmatch(versionString)

	if len(submatches) == 0 {
		return 0, 0, 0, fmt.Errorf("%s is not a valid BOSH release version", versionString)
	}

	major, err := strconv.Atoi(submatches[1])
	if err != nil {
		return 0, 0, 0, err
	}

	minor := 0
	if submatches[2] != "" {
		minor, err = strconv.Atoi(submatches[2])
		if err != nil {
			return 0, 0, 0, err
		}
	}

	patch := 0
	if submatches[3] != "" {
		patch, err = strconv.Atoi(submatches[3])
		if err != nil {
			return 0, 0, 0, err
		}
	}

	return major, minor, patch, nil
}

func generateUpdateBlock(update *serviceadapter.Update) bosh.Update {
	if update != nil {
		return bosh.Update{
			Canaries:        update.Canaries,
			MaxInFlight:     update.MaxInFlight,
			CanaryWatchTime: update.CanaryWatchTime,
			UpdateWatchTime: update.UpdateWatchTime,
			Serial:          update.Serial,
		}
	} else {
		return bosh.Update{
			Canaries:        1,
			CanaryWatchTime: "30000-240000",
			UpdateWatchTime: "30000-240000",
			MaxInFlight:     4,
		}
	}
}

func gatherJobs(releases serviceadapter.ServiceReleases) ([]bosh.Job, error) {
	jobs := []bosh.Job{}

	release, err := findReleaseForJob(RedisServerJobName, releases)
	if err != nil {
		return nil, err
	}
	return append(jobs, bosh.Job{Name: RedisServerJobName, Release: release.Name}), nil
}

func findReleaseForJob(requiredJob string, releases serviceadapter.ServiceReleases) (serviceadapter.ServiceRelease, error) {
	releasesThatProvideRequiredJob := serviceadapter.ServiceReleases{}

	for _, release := range releases {
		for _, providedJob := range release.Jobs {
			if providedJob == requiredJob {
				releasesThatProvideRequiredJob = append(releasesThatProvideRequiredJob, release)
			}
		}
	}

	if len(releasesThatProvideRequiredJob) == 0 {
		return serviceadapter.ServiceRelease{}, fmt.Errorf("no release provided for job %s", requiredJob)
	}

	if len(releasesThatProvideRequiredJob) > 1 {
		releaseNames := []string{}
		for _, release := range releasesThatProvideRequiredJob {
			releaseNames = append(releaseNames, release.Name)
		}

		return serviceadapter.ServiceRelease{}, fmt.Errorf("job %s defined in multiple releases: %s", requiredJob, strings.Join(releaseNames, ", "))
	}

	return releasesThatProvideRequiredJob[0], nil
}

func redisPlanProperties(manifest bosh.BoshManifest) map[interface{}]interface{} {
	return manifest.InstanceGroups[0].Properties["redis"].(map[interface{}]interface{})
}

func (m ManifestGenerator) redisServerProperties(deploymentName string, planProperties serviceadapter.Properties, arbitraryParams map[string]interface{}, previousManifest *bosh.BoshManifest) (map[string]interface{}, error) {
	var previousRedisProperties map[interface{}]interface{}
	if previousManifest != nil {
		previousRedisProperties = redisPlanProperties(*previousManifest)
	}

	persistence, err := m.persistenceForRedisServer(planProperties)
	if err != nil {
		return nil, err
	}
	password, err := passwordForRedisServer(previousRedisProperties)
	if err != nil {
		return nil, err
	}
	maxClients := maxClientsForRedisServer(arbitraryParams, previousRedisProperties)

	return map[string]interface{}{
		"redis": map[interface{}]interface{}{
			"persistence": persistence,
			"password":    password,
			"maxclients":  maxClients,
		},
	}, nil
}

func passwordForRedisServer(previousManifestProperties map[interface{}]interface{}) (string, error) {
	if previousManifestProperties != nil {
		return previousManifestProperties["password"].(string), nil
	}
	return CurrentPasswordGenerator()
}

func maxClientsForRedisServer(arbitraryParams map[string]interface{}, previousManifestProperties map[interface{}]interface{}) int {
	if configuredMax, ok := arbitraryParams["maxclients"]; ok {
		return int(configuredMax.(float64))
	} else if previousManifestProperties != nil {
		return previousManifestProperties["maxclients"].(int)
	}
	return 10000
}

func (m *ManifestGenerator) persistenceForRedisServer(planProperties serviceadapter.Properties) (string, error) {
	persistenceConfig, found := planProperties[RedisServerPersistencePropertyKey]
	if !found {
		m.StderrLogger.Println(fmt.Sprintf("the plan property '%s' is missing", RedisServerPersistencePropertyKey))
		return "", errors.New("")
	}
	persistence := "no"
	if persistenceConfig.(bool) {
		persistence = "yes"
	}
	return persistence, nil
}

func oldGreaterThanNew(oldMajorVersion, oldMinorVersion, oldPatchVersion, newMajorVersion, newMinorVersion, newPatchVersion int) bool {
	if oldMajorVersion != newMajorVersion {
		return oldMajorVersion > newMajorVersion
	}
	if oldMinorVersion != newMinorVersion {
		return oldMinorVersion > newMinorVersion
	}
	return oldPatchVersion > newPatchVersion
}

func findOldManifestRedisRelease(redisReleaseName string, previousManifestReleases []bosh.Release) (bosh.Release, error) {
	for _, oldManifestRelease := range previousManifestReleases {
		if oldManifestRelease.Name == redisReleaseName {
			return oldManifestRelease, nil
		}
	}

	return bosh.Release{}, fmt.Errorf("no release with name %s found in previous manifest", redisReleaseName)
}

func (m ManifestGenerator) validUpgradePath(previousManifest bosh.BoshManifest, serviceReleases serviceadapter.ServiceReleases) error {
	newRedisRelease, err := findReleaseForJob(RedisServerJobName, serviceReleases)
	if err != nil {
		return err
	}

	oldRedisRelease, err := findOldManifestRedisRelease(newRedisRelease.Name, previousManifest.Releases)
	if err != nil {
		return err
	}

	// Allow upgrade to/from latest
	if newRedisRelease.Version == "latest" || oldRedisRelease.Version == "latest" {
		return nil
	}

	newMajorVersion, newMinorVersion, newPatchVersion, err := parseReleaseVersion(newRedisRelease.Version)
	if err != nil {
		return err
	}

	oldMajorVersion, oldMinorVersion, oldPatchVersion, err := parseReleaseVersion(oldRedisRelease.Version)
	if err != nil {
		return err
	}

	if oldGreaterThanNew(oldMajorVersion, oldMinorVersion, oldPatchVersion, newMajorVersion, newMinorVersion, newPatchVersion) {
		return fmt.Errorf(
			"error generating manifest: new release version %s is lower than existing release version %s",
			newRedisRelease.Version,
			oldRedisRelease.Version,
		)
	}

	return nil
}
