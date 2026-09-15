package job

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/cloudwatch"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type ownerMeteringFlags struct{}

func (ownerMeteringFlags) IsFeatureEnabled(flag string) bool {
	return flag == config.OwnerMetering
}

type ownerTagClient struct {
	resources []*model.TaggedResource
	err       error
	calls     int
	jobType   string
	region    string
	bounded   bool
}

func (c *ownerTagClient) GetResources(ctx context.Context, job model.DiscoveryJob, region string) ([]*model.TaggedResource, error) {
	c.calls++
	c.jobType, c.region = job.Type, region
	_, c.bounded = ctx.Deadline()
	return c.resources, c.err
}

func ownerRequest(namespace, dimension, name string) *model.CloudwatchData {
	return &model.CloudwatchData{
		MetricName:   "TestMetric",
		Namespace:    namespace,
		ResourceName: "existing-custom-job-name",
		Tags:         []model.Tag{{Key: "unchanged", Value: "exported-label"}},
		Dimensions:   []model.Dimension{{Name: dimension, Value: name}},
		GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{
			Period: 60, Statistic: "Average",
		},
	}
}

func ownerTags() []model.Tag {
	return []model.Tag{
		{Key: "omd_business_service", Value: "commerce"},
		{Key: "omd_service", Value: "payments"},
		{Key: "omd_component", Value: "worker"},
	}
}

func TestCustomNamespaceOwnerMappings(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		dimension string
		arn       string
		jobType   string
	}{
		{"AmazonMWAA", "Environment", "arn:aws:airflow:us-east-1:123456789012:environment/my-environment", "AmazonMWAA"},
		{"AWS/MWAA", "Environment", "arn:aws:airflow:us-east-1:123456789012:environment/my-environment", "AmazonMWAA"},
		{"AWS/KinesisAnalytics", "Application", "arn:aws:kinesisanalytics:us-east-1:123456789012:application/my-environment", "AWS/KinesisAnalytics"},
	} {
		t.Run(tc.namespace, func(t *testing.T) {
			ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
			a := ownerRequest(tc.namespace, tc.dimension, "my-environment")
			b := ownerRequest(tc.namespace, tc.dimension, "my-environment")
			b.GetMetricDataProcessingParams.Statistic = "Maximum"
			resourceTags := append(ownerTags(), model.Tag{Key: "sensitive-other-tag", Value: "not-exported"})
			client := &ownerTagClient{resources: []*model.TaggedResource{{ARN: tc.arn, Tags: resourceTags}}}
			original := *a
			enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), tc.namespace, "123456789012", "us-east-1", []*model.CloudwatchData{a, b}, client)
			if client.calls != 1 || client.jobType != tc.jobType || client.region != "us-east-1" || !client.bounded {
				t.Fatalf("unexpected lookup: %+v", client)
			}
			for _, data := range []*model.CloudwatchData{a, b} {
				if !reflect.DeepEqual(data.OwnerTags, ownerTags()) {
					t.Fatalf("owner tags = %+v", data.OwnerTags)
				}
			}
			copy := *a
			copy.OwnerTags = nil
			if !reflect.DeepEqual(copy, original) {
				t.Fatalf("enrichment changed exported fields or request parameters: %+v", copy)
			}
			if len(client.resources[0].Tags) != 4 {
				t.Fatal("modified resource tag inventory")
			}
		})
	}
}

func TestCustomOwnerRejectsWrongScopeAndAmbiguity(t *testing.T) {
	correctARN := "arn:aws:airflow:us-east-1:123456789012:environment/target"
	for _, tc := range []struct {
		name      string
		resources []*model.TaggedResource
	}{
		{"not found", nil},
		{"nil resource", []*model.TaggedResource{nil}},
		{"wrong account", []*model.TaggedResource{{ARN: "arn:aws:airflow:us-east-1:999999999999:environment/target", Tags: ownerTags()}}},
		{"wrong region", []*model.TaggedResource{{ARN: "arn:aws:airflow:us-west-2:123456789012:environment/target", Tags: ownerTags()}}},
		{"wrong service", []*model.TaggedResource{{ARN: "arn:aws:other:us-east-1:123456789012:environment/target", Tags: ownerTags()}}},
		{"wrong type", []*model.TaggedResource{{ARN: "arn:aws:airflow:us-east-1:123456789012:other/target", Tags: ownerTags()}}},
		{"prefix only", []*model.TaggedResource{{ARN: correctARN + "-other", Tags: ownerTags()}}},
		{"malformed ARN", []*model.TaggedResource{{ARN: "target", Tags: ownerTags()}}},
		{"ambiguous", []*model.TaggedResource{{ARN: correctARN, Tags: ownerTags()}, {ARN: correctARN, Tags: nil}, {ARN: correctARN, Tags: ownerTags()}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := ownerRequest("AmazonMWAA", "Environment", "target")
			client := &ownerTagClient{resources: tc.resources}
			ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
			enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), "AmazonMWAA", "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
			if data.OwnerTags == nil || len(data.OwnerTags) != 0 {
				t.Fatalf("must remain explicitly unallocated: %+v", data.OwnerTags)
			}
		})
	}
}

func TestCustomOwnerPartialAndDuplicateTagsArePreserved(t *testing.T) {
	for _, tags := range [][]model.Tag{
		{{Key: "omd_service", Value: "payments"}},
		{{Key: "omd_service", Value: "payments"}, {Key: "omd_service", Value: "conflict"}},
	} {
		data := ownerRequest("AmazonMWAA", "Environment", "target")
		client := &ownerTagClient{resources: []*model.TaggedResource{{ARN: "arn:aws:airflow:us-east-1:123456789012:environment/target", Tags: tags}}}
		ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
		enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), "AmazonMWAA", "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
		if !reflect.DeepEqual(data.OwnerTags, tags) {
			t.Fatalf("tag conflicts must reach metering validation, got %+v", data.OwnerTags)
		}
	}
}

func TestCustomOwnerDoesNotLookupWithoutEligibleRequests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		enabled   bool
		data      *model.CloudwatchData
	}{
		{"disabled", "AmazonMWAA", false, ownerRequest("AmazonMWAA", "Environment", "target")},
		{"unsupported", "AWS/Usage", true, ownerRequest("AWS/Usage", "Environment", "target")},
		{"missing dimension", "AmazonMWAA", true, ownerRequest("AmazonMWAA", "Other", "target")},
		{"empty dimension", "AmazonMWAA", true, ownerRequest("AmazonMWAA", "Environment", "")},
		{"cross namespace", "AmazonMWAA", true, ownerRequest("AWS/MWAA", "Environment", "target")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.enabled {
				ctx = config.CtxWithFlags(ctx, ownerMeteringFlags{})
			}
			client := &ownerTagClient{}
			enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), tc.namespace, "123456789012", "us-east-1", []*model.CloudwatchData{tc.data}, client)
			if client.calls != 0 {
				t.Fatal("unexpected resource lookup")
			}
			if !tc.enabled && tc.data.OwnerTags != nil {
				t.Fatal("disabled feature changed ownership")
			}
		})
	}
	ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
	data := ownerRequest("AmazonMWAA", "Environment", "target")
	data.GetMetricDataProcessingParams.Expression = "SEARCH('...', 'Average')"
	client := &ownerTagClient{}
	enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), "AmazonMWAA", "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
	if client.calls != 0 || len(data.OwnerTags) != 0 {
		t.Fatal("expression must not be assigned one resource's owner")
	}
	data.GetMetricDataProcessingParams.Expression = ""
	data.Dimensions = append(data.Dimensions, model.Dimension{Name: "Environment", Value: "different"})
	enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), "AmazonMWAA", "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
	if client.calls != 0 {
		t.Fatal("conflicting dimensions must not resolve")
	}
}

type ownerCloudwatchClient struct {
	cloudwatch.Client
}

func (ownerCloudwatchClient) ListMetrics(_ context.Context, namespace string, _ *model.MetricConfig, _ bool, fn func([]*model.Metric)) error {
	fn([]*model.Metric{{MetricName: "TestMetric", Namespace: namespace, Dimensions: []model.Dimension{{Name: "Environment", Value: "target"}}}})
	return nil
}

type ownerProcessor struct {
	run func([]*model.CloudwatchData) ([]*model.CloudwatchData, error)
}

func (p ownerProcessor) Run(_ context.Context, _ string, _, _ int64, _ *int64, data []*model.CloudwatchData) ([]*model.CloudwatchData, error) {
	return p.run(data)
}

func TestCustomOwnerResolutionRunsBeforeMeteringAndFailsOpen(t *testing.T) {
	for _, lookupFails := range []bool{false, true} {
		ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
		client := &ownerTagClient{resources: []*model.TaggedResource{{ARN: "arn:aws:airflow:us-east-1:123456789012:environment/target", Tags: ownerTags()}}}
		if lookupFails {
			client.err = errors.New("AccessDenied")
		}
		calls := 0
		processor := ownerProcessor{run: func(data []*model.CloudwatchData) ([]*model.CloudwatchData, error) {
			calls++
			if len(data) != 1 || data[0].ResourceName != "unchanged-name" || data[0].Tags != nil {
				t.Fatal("resource lookup changed collection output")
			}
			if lookupFails {
				if data[0].OwnerTags == nil || len(data[0].OwnerTags) != 0 {
					t.Fatal("failed lookup must not use partial resource results")
				}
			} else if !reflect.DeepEqual(data[0].OwnerTags, ownerTags()) {
				t.Fatal("owner tags were not attached before GMD processing")
			}
			return data, nil
		}}
		job := model.CustomNamespaceJob{Name: "unchanged-name", Namespace: "AmazonMWAA", Metrics: []*model.MetricConfig{{Name: "TestMetric", Statistics: []string{"Average"}, Period: 60, Length: 60}}}
		result := runCustomNamespaceJob(ctx, logging.NewNopLogger(), job, ownerCloudwatchClient{}, processor, client, "123456789012", "us-east-1")
		if len(result) != 1 || calls != 1 {
			t.Fatal("lookup failure suppressed metric collection")
		}
	}
}
