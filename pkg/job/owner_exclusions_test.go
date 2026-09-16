package job

import (
	"context"
	"testing"

	"github.com/grafana/regexp"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

type ownerExclusionFlags map[string]bool

func (f ownerExclusionFlags) IsFeatureEnabled(flag string) bool {
	return f[flag]
}

func exclusionRequest(namespace, metric string, tags []model.Tag) *model.CloudwatchData {
	return &model.CloudwatchData{
		Namespace:                     namespace,
		MetricName:                    metric,
		OwnerTags:                     tags,
		GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{},
	}
}

func ownerExclusion(namespace, metric, businessService, service, component string) model.OwnerMetricExclusion {
	exclusion := model.OwnerMetricExclusion{
		Namespace:  regexp.MustCompile(namespace),
		MetricName: regexp.MustCompile(metric),
	}
	if businessService != "" {
		exclusion.OwnerBusinessService = regexp.MustCompile(businessService)
	}
	if service != "" {
		exclusion.OwnerService = regexp.MustCompile(service)
	}
	if component != "" {
		exclusion.OwnerComponent = regexp.MustCompile(component)
	}
	return exclusion
}

func TestOwnerMetricExclusionsRequireBothFlags(t *testing.T) {
	request := exclusionRequest("AWS/EC2", "CPUUtilization", ownerTags())
	rules := []model.OwnerMetricExclusion{ownerExclusion(".*", ".*", "", "", ".*")}
	for _, flags := range []ownerExclusionFlags{
		{},
		{config.OwnerMetering: true},
		{config.OwnerMetricExclusions: true},
	} {
		ctx := config.CtxWithFlags(context.Background(), flags)
		if got := applyOwnerMetricExclusions(ctx, "123456789012", "us-east-1", rules, []*model.CloudwatchData{request}); len(got) != 1 {
			t.Fatal("one feature flag activated exclusions")
		}
	}
	ctx := config.CtxWithFlags(context.Background(), ownerExclusionFlags{config.OwnerMetering: true, config.OwnerMetricExclusions: true})
	if got := applyOwnerMetricExclusions(ctx, "123456789012", "us-east-1", rules, []*model.CloudwatchData{request}); len(got) != 0 {
		t.Fatal("both feature flags did not activate exclusions")
	}
}

func TestOwnerMetricExclusionsMatchRegexSubsets(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerExclusionFlags{config.OwnerMetering: true, config.OwnerMetricExclusions: true})
	matching := exclusionRequest("AWS/EC2", "CPUUtilization", []model.Tag{{Key: "omd_component", Value: "payments-worker"}})
	namespaceMiss := exclusionRequest("AWS/RDS", "CPUUtilization", ownerTags())
	metricMiss := exclusionRequest("AWS/EC2", "NetworkIn", ownerTags())
	ownerMiss := exclusionRequest("AWS/EC2", "CPUUtilization", []model.Tag{{Key: "omd_component", Value: "api"}})
	rules := []model.OwnerMetricExclusion{ownerExclusion(`^AWS/EC2$`, `^CPU.*$`, "", "", `^payments-.*$`)}
	got := applyOwnerMetricExclusions(ctx, "123456789012", "us-east-1", rules, []*model.CloudwatchData{matching, namespaceMiss, metricMiss, ownerMiss})
	if len(got) != 3 || got[0] != namespaceMiss || got[1] != metricMiss || got[2] != ownerMiss {
		t.Fatal("regex or subset matching removed the wrong requests")
	}
}

func TestOwnerMetricExclusionsNeverMatchMissingOrConflictingSelectedTags(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerExclusionFlags{config.OwnerMetering: true, config.OwnerMetricExclusions: true})
	rules := []model.OwnerMetricExclusion{ownerExclusion(`.*`, `.*`, "", "", `.*`)}
	missing := exclusionRequest("AWS/EC2", "CPUUtilization", nil)
	wrongSource := exclusionRequest("AWS/EC2", "CPUUtilization", nil)
	wrongSource.Tags = ownerTags()
	unknown := exclusionRequest("AWS/EC2", "CPUUtilization", []model.Tag{{Key: "omd_component", Value: "_unknown"}})
	unallocated := exclusionRequest("AWS/EC2", "CPUUtilization", []model.Tag{{Key: "omd_component", Value: "_unallocated"}})
	conflicting := exclusionRequest("AWS/EC2", "CPUUtilization", []model.Tag{
		{Key: "omd_component", Value: "worker"},
		{Key: "omd_component", Value: "api"},
		{Key: "omd_component", Value: "worker"},
	})
	got := applyOwnerMetricExclusions(ctx, "123456789012", "us-east-1", rules, []*model.CloudwatchData{missing, wrongSource, unknown, unallocated, conflicting})
	if len(got) != 5 {
		t.Fatal("missing, exported-only, or conflicting owner tags matched an exclusion")
	}
}

func TestOwnerMetricExclusionsUseDiscoveryResourceTags(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerExclusionFlags{config.OwnerMetering: true, config.OwnerMetricExclusions: true})
	request := exclusionRequest("AWS/Athena", "ProcessedBytes", nil)
	request.ResourceName = "arn:aws:athena:us-east-1:123456789012:workgroup/example"
	resources := []*model.TaggedResource{{ARN: request.ResourceName, Tags: ownerTags()}}
	enrichDiscoveryOwners(ctx, request.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{request}, resources)
	rules := []model.OwnerMetricExclusion{ownerExclusion(`^AWS/Athena$`, `^ProcessedBytes$`, "", "", `^worker$`)}
	if got := applyOwnerMetricExclusions(ctx, "123456789012", "us-east-1", rules, []*model.CloudwatchData{request}); len(got) != 0 {
		t.Fatal("raw discovery resource tags did not drive exclusion")
	}
}

func TestOwnerMetricExclusionsUseCustomNamespaceResourceTags(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerExclusionFlags{config.OwnerMetering: true, config.OwnerMetricExclusions: true})
	request := exclusionRequest("AmazonMWAA", "DAGDuration.Success", nil)
	request.Dimensions = []model.Dimension{{Name: "Environment", Value: "example"}}
	client := &ownerTagClient{resources: []*model.TaggedResource{{
		ARN: "arn:aws:airflow:us-east-1:123456789012:environment/example", Tags: ownerTags(),
	}}}
	enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), request.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{request}, client)
	rules := []model.OwnerMetricExclusion{ownerExclusion(`^AmazonMWAA$`, `^DAGDuration\.Success$`, "", "", `^worker$`)}
	if got := applyOwnerMetricExclusions(ctx, "123456789012", "us-east-1", rules, []*model.CloudwatchData{request}); len(got) != 0 {
		t.Fatal("custom namespace resource tags did not drive exclusion")
	}
}

func TestOwnerMetricExclusionCounter(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerExclusionFlags{config.OwnerMetering: true, config.OwnerMetricExclusions: true})
	request := exclusionRequest("Test/ExclusionCounter", "UniqueMetric", []model.Tag{{Key: "omd_component", Value: "unique-component"}})
	rules := []model.OwnerMetricExclusion{
		ownerExclusion(`^Test/ExclusionCounter$`, `^UniqueMetric$`, "", "", `^unique-component$`),
		ownerExclusion(`.*`, `.*`, "", "", `.*`),
	}
	counter := promutil.CloudwatchGetMetricDataOwnerExcludedQueryObjectsCounter.WithLabelValues(
		"000000000000", "test-region-1", "Test/ExclusionCounter", "UniqueMetric", "_unallocated", "_unallocated", "unique-component",
	)
	before := testutil.ToFloat64(counter)
	applyOwnerMetricExclusions(ctx, "000000000000", "test-region-1", rules, []*model.CloudwatchData{request})
	if after := testutil.ToFloat64(counter); after-before != 1 {
		t.Fatalf("counter delta=%v", after-before)
	}
}
