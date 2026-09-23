package job

import (
	"context"
	"testing"

	"github.com/grafana/regexp"
	"github.com/stretchr/testify/require"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/cloudwatch"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type discoveryTagClient struct {
	resources []*model.TaggedResource
}

func (c discoveryTagClient) GetResources(_ context.Context, job model.DiscoveryJob, _ string) ([]*model.TaggedResource, error) {
	var matched []*model.TaggedResource
	for _, resource := range c.resources {
		if resource.ShouldInclude(job.SearchTags, job.ExcludeTags) {
			matched = append(matched, resource)
		}
	}
	if len(matched) == 0 {
		return nil, tagging.ErrExpectedToFindResources
	}
	return matched, nil
}

type discoveryCWClient struct {
	cloudwatch.Client
	calls int
}

func (c *discoveryCWClient) ListMetrics(_ context.Context, namespace string, metric *model.MetricConfig, _ bool, fn func([]*model.Metric)) error {
	c.calls++
	fn([]*model.Metric{
		{Namespace: namespace, MetricName: metric.Name, Dimensions: []model.Dimension{{Name: "InstanceId", Value: "i-keep"}}},
		{Namespace: namespace, MetricName: metric.Name, Dimensions: []model.Dimension{{Name: "InstanceId", Value: "i-drop"}}},
	})
	return nil
}

type discoveryGMDProcessor struct {
	calls int
	data  []*model.CloudwatchData
}

func (p *discoveryGMDProcessor) Run(_ context.Context, _ string, _, _ int64, _ *int64, requests []*model.CloudwatchData) ([]*model.CloudwatchData, error) {
	p.calls++
	p.data = requests
	return requests, nil
}

func TestRunDiscoveryJobExcludesResourcesBeforeGetMetricData(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exclude   string
		wantCount int
		wantID    string
	}{
		{name: "excluded resource is not queried", exclude: "^drop$", wantCount: 1, wantID: "i-keep"},
		{name: "unmatched exclusion preserves both queries", exclude: "^other$", wantCount: 2},
		{name: "all resources excluded skips ListMetrics and GetMetricData", exclude: ".*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := model.DiscoveryJob{
				Type: "AWS/EC2", ExcludeTags: []model.SearchTag{{Key: "team", Value: regexp.MustCompile(tc.exclude)}},
				Metrics: []*model.MetricConfig{{Name: "CPUUtilization", Statistics: []string{"Average"}, Period: 60, Length: 300}},
			}
			job.DimensionsRegexps = []model.DimensionsRegexp{{Regexp: regexp.MustCompile("instance/(?P<InstanceId>i-[^/]+)$"), DimensionsNames: []string{"InstanceId"}}}
			resources := []*model.TaggedResource{
				{ARN: "arn:aws:ec2:us-east-1:123456789012:instance/i-keep", Tags: []model.Tag{{Key: "team", Value: "keep"}}},
				{ARN: "arn:aws:ec2:us-east-1:123456789012:instance/i-drop", Tags: []model.Tag{{Key: "team", Value: "drop"}}},
			}
			cw := &discoveryCWClient{}
			processor := &discoveryGMDProcessor{}
			_, data := runDiscoveryJob(context.Background(), logging.NewNopLogger(), job, "us-east-1", discoveryTagClient{resources: resources}, cw, processor)
			require.Len(t, data, tc.wantCount)
			if tc.wantCount == 0 {
				require.Zero(t, cw.calls)
				require.Zero(t, processor.calls)
				return
			}
			require.Equal(t, 1, cw.calls)
			require.Equal(t, 1, processor.calls)
			require.Len(t, processor.data, tc.wantCount)
			if tc.wantID != "" {
				require.Equal(t, tc.wantID, data[0].Dimensions[0].Value)
			}
		})
	}
}
