package job

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws/arn"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type customOwnerMapping struct {
	discoveryNamespace string
	dimension          string
	service            string
	resourcePrefix     string
}

func customNamespaceOwnerMapping(namespace string) (customOwnerMapping, bool) {
	switch namespace {
	case "AmazonMWAA", "AWS/MWAA":
		return customOwnerMapping{"AmazonMWAA", "Environment", "airflow", "environment/"}, true
	case "AWS/KinesisAnalytics":
		return customOwnerMapping{"AWS/KinesisAnalytics", "Application", "kinesisanalytics", "application/"}, true
	default:
		return customOwnerMapping{}, false
	}
}

func customOwnerDimension(dimensions []model.Dimension, name string) (string, bool) {
	value := ""
	found := false
	for _, dimension := range dimensions {
		if dimension.Name != name {
			continue
		}
		if found && dimension.Value != value {
			return "", false
		}
		value, found = dimension.Value, true
	}
	return value, found && value != ""
}

func enrichCustomNamespaceOwners(ctx context.Context, logger logging.Logger, namespace, accountID, region string, data []*model.CloudwatchData, client tagging.Client) {
	mapping, supported := customNamespaceOwnerMapping(namespace)
	if !supported || !config.FlagsFromCtx(ctx).IsFeatureEnabled(config.OwnerMetering) {
		return
	}

	requests := map[string][]*model.CloudwatchData{}
	for _, metric := range data {
		metric.OwnerTags = []model.Tag{}
		if metric.Namespace != namespace || metric.GetMetricDataProcessingParams == nil || metric.GetMetricDataProcessingParams.Expression != "" {
			continue
		}
		if name, ok := customOwnerDimension(metric.Dimensions, mapping.dimension); ok {
			requests[name] = append(requests[name], metric)
		}
	}
	if len(requests) == 0 || client == nil {
		return
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resources, err := client.GetResources(lookupCtx, model.DiscoveryJob{Type: mapping.discoveryNamespace}, region)
	if err != nil {
		logger.Error(err, "Failed to resolve custom namespace owners; retaining unallocated requests", "namespace", namespace, "account", accountID, "region", region)
		return
	}

	owners := map[string][]model.Tag{}
	seen := map[string]bool{}
	for _, resource := range resources {
		if resource == nil {
			continue
		}
		parsed, err := arn.Parse(resource.ARN)
		if err != nil || parsed.AccountID != accountID || parsed.Region != region || parsed.Service != mapping.service || !strings.HasPrefix(parsed.Resource, mapping.resourcePrefix) {
			continue
		}
		name := strings.TrimPrefix(parsed.Resource, mapping.resourcePrefix)
		if _, wanted := requests[name]; !wanted {
			continue
		}
		if seen[name] {
			owners[name] = []model.Tag{}
			continue
		}
		seen[name] = true
		owners[name] = []model.Tag{}
		for _, tag := range resource.Tags {
			switch tag.Key {
			case "omd_business_service", "omd_service", "omd_component":
				owners[name] = append(owners[name], tag)
			}
		}
	}
	for name, metrics := range requests {
		for _, metric := range metrics {
			metric.OwnerTags = append(metric.OwnerTags, owners[name]...)
		}
	}
}
