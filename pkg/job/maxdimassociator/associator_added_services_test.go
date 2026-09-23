package maxdimassociator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

func TestAssociatorAddedServices(t *testing.T) {
	testcases := []struct {
		namespace  string
		arn        string
		dimensions []model.Dimension
	}{
		{
			namespace: "AmazonMWAA",
			arn:       "arn:aws:airflow:us-east-1:123456789012:environment/example-env",
			dimensions: []model.Dimension{
				{Name: "Environment", Value: "example-env"},
				{Name: "Function", Value: "Scheduler"},
			},
		},
		{
			namespace: "AWS/MWAA",
			arn:       "arn:aws:airflow:us-east-1:123456789012:environment/example-env",
			dimensions: []model.Dimension{
				{Name: "Cluster", Value: "BaseWorker"},
				{Name: "Environment", Value: "example-env"},
			},
		},
		{
			namespace: "AWS/Bedrock-AgentCore",
			arn:       "arn:aws:bedrock-agentcore:us-east-1:123456789012:runtime/example_agent-AbCdEf1234",
			dimensions: []model.Dimension{
				{Name: "Name", Value: "example_agent::DEFAULT"},
				{Name: "Operation", Value: "InvokeAgentRuntime"},
				{Name: "Resource", Value: "arn:aws:bedrock-agentcore:us-east-1:123456789012:runtime/example_agent-AbCdEf1234"},
			},
		},
		{
			namespace: "AWS/DAX",
			arn:       "arn:aws:dax:us-east-1:123456789012:cache/example-cluster",
			dimensions: []model.Dimension{
				{Name: "ClusterId", Value: "example-cluster"},
				{Name: "NodeId", Value: "example-cluster-a"},
			},
		},
		{
			namespace: "AWS/EKS",
			arn:       "arn:aws:eks:us-east-1:123456789012:cluster/example-cluster",
			dimensions: []model.Dimension{
				{Name: "ClusterName", Value: "example-cluster"},
			},
		},
		{
			namespace: "AWS/Glue",
			arn:       "arn:aws:glue:us-east-1:123456789012:job/example-job",
			dimensions: []model.Dimension{
				{Name: "JobName", Value: "example-job"},
			},
		},
		{
			namespace: "AWS/OSIS",
			arn:       "arn:aws:osis:us-east-1:123456789012:pipeline/example-pipeline",
			dimensions: []model.Dimension{
				{Name: "PipelineName", Value: "example-pipeline"},
			},
		},
		{
			namespace: "CloudWatchSynthetics",
			arn:       "arn:aws:synthetics:us-east-1:123456789012:canary:example-canary",
			dimensions: []model.Dimension{
				{Name: "CanaryName", Value: "example-canary"},
				{Name: "StepName", Value: "example-step"},
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.namespace, func(t *testing.T) {
			resource := &model.TaggedResource{ARN: tc.arn, Namespace: tc.namespace}
			associator := NewAssociator(logging.NewNopLogger(), config.SupportedServices.GetService(tc.namespace).ToModelDimensionsRegexp(), []*model.TaggedResource{resource})
			res, skip := associator.AssociateMetricToResource(&model.Metric{Namespace: tc.namespace, Dimensions: tc.dimensions})
			require.False(t, skip)
			require.Equal(t, resource, res)
		})
	}
}
