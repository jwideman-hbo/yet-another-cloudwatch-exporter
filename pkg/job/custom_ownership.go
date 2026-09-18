package job

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/grafana/regexp"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type customOwnerMapping struct {
	discoveryNamespace string
	resourceTypes      []string
	patterns           []*regexp.Regexp
	requiredDimensions map[string]string
}

func customNamespaceOwnerMapping(namespace string) (customOwnerMapping, bool) {
	mapping := customOwnerMapping{discoveryNamespace: namespace}
	switch namespace {
	case "AmazonMWAA", "AWS/MWAA":
		mapping.discoveryNamespace = "AmazonMWAA"
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^arn:[^:]+:airflow:[^:]+:[^:]+:environment/(?P<Environment>[^/]+)$`)}
	case "AWS/DAX":
		mapping.resourceTypes = []string{"dax"}
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^arn:[^:]+:dax:[^:]+:[^:]+:cache/(?P<ClusterId>[^/]+)$`)}
	case "AWS/EKS":
		mapping.resourceTypes = []string{"eks:cluster"}
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^arn:[^:]+:eks:[^:]+:[^:]+:cluster/(?P<ClusterName>[^/]+)$`)}
	case "AWS/OSIS":
		mapping.resourceTypes = []string{"osis"}
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^arn:[^:]+:osis:[^:]+:[^:]+:pipeline/(?P<PipelineName>[^/]+)$`)}
	case "CloudWatchSynthetics", "CloudWatchSynthetics/Custom":
		mapping.resourceTypes = []string{"synthetics"}
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^arn:[^:]+:synthetics:[^:]+:[^:]+:canary:(?P<CanaryName>[^:]+)$`)}
	case "AWS/EC2CapacityReservations":
		mapping.resourceTypes = []string{"ec2:capacity-reservation"}
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^arn:[^:]+:ec2:[^:]+:[^:]+:capacity-reservation/(?P<CapacityReservationId>[^/]+)$`)}
	case "AWS/Timestream":
		mapping.resourceTypes = []string{"timestream:table"}
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^arn:[^:]+:timestream:[^:]+:[^:]+:database/(?P<DatabaseName>[^/]+)/table/(?P<TableName>[^/]+)$`)}
	case "AWS/Personalize":
		mapping.resourceTypes = []string{"personalize:campaign", "personalize:recommender"}
		mapping.patterns = []*regexp.Regexp{
			regexp.MustCompile(`^(?P<CampaignArn>arn:[^:]+:personalize:[^:]+:[^:]+:campaign/[^/]+)$`),
			regexp.MustCompile(`^(?P<RecommenderArn>arn:[^:]+:personalize:[^:]+:[^:]+:recommender/[^/]+)$`),
		}
	case "AWS/Glue":
		mapping.discoveryNamespace = "Glue"
	case "AWS/CertificateManager":
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`^(?P<CertificateArn>arn:[^:]+:acm:[^:]+:[^:]+:certificate/[^/]+)$`)}
	case "AWS/Prometheus":
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`:workspace/(?P<Workspace>ws-[^/]+)$`)}
	case "AWS/Usage":
		mapping.discoveryNamespace = "AWS/Prometheus"
		mapping.requiredDimensions = map[string]string{"Service": "Prometheus"}
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`:workspace/(?P<ResourceId>ws-[^/]+)$`)}
	case "AWS/DMS":
		mapping.patterns = []*regexp.Regexp{regexp.MustCompile(`:rep:(?P<ReplicationInstanceExternalResourceId>[^/]+)(?:/[^/]+)?$`)}
	}
	if svc := config.SupportedServices.GetService(mapping.discoveryNamespace); svc != nil {
		mapping.patterns = append(mapping.patterns, svc.DimensionRegexps...)
	}
	return mapping, len(mapping.patterns) != 0
}

func customOwnerDimension(dimensions []model.Dimension, name string) (string, bool) {
	value := ""
	found := false
	for _, dimension := range dimensions {
		key := dimension.Name
		if key == "DbClusterIdentifier" {
			key = "DBClusterIdentifier"
		}
		if key == "DbInstanceIdentifier" {
			key = "DBInstanceIdentifier"
		}
		if key != name {
			continue
		}
		if found && dimension.Value != value {
			return "", false
		}
		value, found = dimension.Value, true
	}
	return value, found && value != ""
}

func ownerDimensionKey(pattern *regexp.Regexp, dimensions []model.Dimension) (string, bool) {
	var values []string
	for _, name := range pattern.SubexpNames()[1:] {
		if name == "" {
			continue
		}
		value, ok := customOwnerDimension(dimensions, strings.ReplaceAll(name, "_", " "))
		if !ok {
			return "", false
		}
		values = append(values, value)
	}
	encoded, _ := json.Marshal(values)
	return string(encoded), len(values) != 0
}

func (m customOwnerMapping) eligible(data *model.CloudwatchData, namespace string) bool {
	if data.Namespace != namespace || data.GetMetricDataProcessingParams == nil {
		return false
	}
	for name, expected := range m.requiredDimensions {
		if value, ok := customOwnerDimension(data.Dimensions, name); !ok || value != expected {
			return false
		}
	}
	for _, pattern := range m.patterns {
		if _, ok := ownerDimensionKey(pattern, data.Dimensions); ok {
			return true
		}
	}
	return false
}

type ownerResourceIndex struct {
	mapping    customOwnerMapping
	byARN      map[string][]model.Tag
	dimensions []map[string][]string
}

func newOwnerResourceIndex(mapping customOwnerMapping, resources []*model.TaggedResource, accountID, region string) ownerResourceIndex {
	index := ownerResourceIndex{mapping: mapping, byARN: map[string][]model.Tag{}, dimensions: make([]map[string][]string, len(mapping.patterns))}
	for i := range index.dimensions {
		index.dimensions[i] = map[string][]string{}
	}
	for _, resource := range resources {
		if resource == nil {
			continue
		}
		parsed, err := arn.Parse(resource.ARN)
		if err != nil {
			continue
		}
		accountMatches := parsed.AccountID == accountID || parsed.AccountID == "" && (parsed.Service == "apigateway" || parsed.Service == "s3")
		regionMatches := parsed.Region == region || parsed.Region == "" && (parsed.Service == "s3" || parsed.Service == "cloudfront" || parsed.Service == "shield")
		if !accountMatches || !regionMatches {
			continue
		}
		if _, duplicate := index.byARN[resource.ARN]; duplicate {
			index.byARN[resource.ARN] = []model.Tag{}
			continue
		}
		index.byARN[resource.ARN] = []model.Tag{}
		for _, tag := range resource.Tags {
			switch tag.Key {
			case "omd_business_service", "omd_service", "omd_component":
				index.byARN[resource.ARN] = append(index.byARN[resource.ARN], tag)
			}
		}
		for i, pattern := range mapping.patterns {
			matches := pattern.FindStringSubmatch(resource.ARN)
			if matches == nil {
				continue
			}
			var values []string
			for j, name := range pattern.SubexpNames() {
				if j > 0 && name != "" {
					values = append(values, matches[j])
				}
			}
			encoded, _ := json.Marshal(values)
			key := string(encoded)
			index.dimensions[i][key] = append(index.dimensions[i][key], resource.ARN)
		}
	}
	return index
}

func (i ownerResourceIndex) ownerTags(data *model.CloudwatchData, namespace string) []model.Tag {
	if data.Namespace != namespace || data.GetMetricDataProcessingParams == nil {
		return []model.Tag{}
	}
	if strings.HasPrefix(data.ResourceName, "arn:") {
		return append([]model.Tag{}, i.byARN[data.ResourceName]...)
	}
	if !i.mapping.eligible(data, namespace) {
		return []model.Tag{}
	}
	candidate := ""
	maxDimensions := 0
	for patternIndex, pattern := range i.mapping.patterns {
		key, ok := ownerDimensionKey(pattern, data.Dimensions)
		if !ok {
			continue
		}
		dimensionCount := 0
		for _, name := range pattern.SubexpNames()[1:] {
			if name != "" {
				dimensionCount++
			}
		}
		resources := i.dimensions[patternIndex][key]
		if len(resources) == 0 || dimensionCount < maxDimensions {
			continue
		}
		if dimensionCount > maxDimensions {
			candidate = ""
			maxDimensions = dimensionCount
		}
		for _, resourceARN := range resources {
			if candidate != "" && candidate != resourceARN {
				return []model.Tag{}
			}
			candidate = resourceARN
		}
	}
	return append([]model.Tag{}, i.byARN[candidate]...)
}

func enrichCustomNamespaceOwners(ctx context.Context, logger logging.Logger, namespace, accountID, region string, data []*model.CloudwatchData, client tagging.Client) {
	mapping, supported := customNamespaceOwnerMapping(namespace)
	if !supported || !config.FlagsFromCtx(ctx).IsFeatureEnabled(config.OwnerMetering) {
		return
	}
	eligible := false
	for _, metric := range data {
		metric.OwnerTags = []model.Tag{}
		eligible = eligible || mapping.eligible(metric, namespace)
	}
	if !eligible || client == nil {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var resources []*model.TaggedResource
	var err error
	if len(mapping.resourceTypes) != 0 {
		if ownerClient, ok := client.(tagging.OwnerResourceClient); ok {
			resources, err = ownerClient.GetResourcesForOwner(lookupCtx, mapping.resourceTypes, region)
		} else {
			return
		}
	} else {
		resources, err = client.GetResources(lookupCtx, model.DiscoveryJob{Type: mapping.discoveryNamespace}, region)
	}
	if err != nil {
		logger.Error(err, "Failed to resolve custom namespace owners; retaining unallocated requests", "namespace", namespace, "account", accountID, "region", region)
		return
	}
	index := newOwnerResourceIndex(mapping, resources, accountID, region)
	for _, metric := range data {
		metric.OwnerTags = index.ownerTags(metric, namespace)
	}
}

func enrichDiscoveryOwners(ctx context.Context, namespace, accountID, region string, data []*model.CloudwatchData, resources []*model.TaggedResource) {
	if !config.FlagsFromCtx(ctx).IsFeatureEnabled(config.OwnerMetering) {
		return
	}
	mapping, _ := customNamespaceOwnerMapping(namespace)
	index := newOwnerResourceIndex(mapping, resources, accountID, region)
	for _, metric := range data {
		metric.OwnerTags = index.ownerTags(metric, namespace)
	}
}
