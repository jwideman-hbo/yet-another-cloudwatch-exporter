package maxdimassociator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

var sagemakerEndpointHealthOne = &model.TaggedResource{
	ARN:       "arn:aws:sagemaker:us-west-2:123456789012:endpoint/example-endpoint-one",
	Namespace: "/aws/sagemaker/Endpoints",
}

var sagemakerEndpointHealthTwo = &model.TaggedResource{
	ARN:       "arn:aws:sagemaker:us-west-2:123456789012:endpoint/example-endpoint-two",
	Namespace: "/aws/sagemaker/Endpoints",
}

var sagemakerEndpointHealthUpper = &model.TaggedResource{
	ARN:       "arn:aws:sagemaker:us-west-2:123456789012:endpoint/example-endpoint-upper",
	Namespace: "/aws/sagemaker/Endpoints",
}

var sagemakerEndpointHealthMixed = &model.TaggedResource{
	ARN:       "arn:aws:sagemaker:us-west-2:123456789012:endpoint/Example-Endpoint-Mixed",
	Namespace: "/aws/sagemaker/Endpoints",
}

var sagemakerHealthResources = []*model.TaggedResource{
	sagemakerEndpointHealthOne,
	sagemakerEndpointHealthTwo,
	sagemakerEndpointHealthUpper,
	sagemakerEndpointHealthMixed,
}

func TestAssociatorSagemakerEndpoint(t *testing.T) {
	type args struct {
		dimensionRegexps []model.DimensionsRegexp
		resources        []*model.TaggedResource
		metric           *model.Metric
	}

	type testCase struct {
		name             string
		args             args
		expectedSkip     bool
		expectedResource *model.TaggedResource
	}

	testcases := []testCase{
		{
			name: "2 dimensions should match",
			args: args{
				dimensionRegexps: config.SupportedServices.GetService("/aws/sagemaker/Endpoints").ToModelDimensionsRegexp(),
				resources:        sagemakerHealthResources,
				metric: &model.Metric{
					MetricName: "MemoryUtilization",
					Namespace:  "/aws/sagemaker/Endpoints",
					Dimensions: []model.Dimension{
						{Name: "EndpointName", Value: "example-endpoint-two"},
						{Name: "VariantName", Value: "example-endpoint-two-variant-one"},
					},
				},
			},
			expectedSkip:     false,
			expectedResource: sagemakerEndpointHealthTwo,
		},
		{
			name: "2 dimensions should not match",
			args: args{
				dimensionRegexps: config.SupportedServices.GetService("/aws/sagemaker/Endpoints").ToModelDimensionsRegexp(),
				resources:        sagemakerHealthResources,
				metric: &model.Metric{
					MetricName: "MemoryUtilization",
					Namespace:  "/aws/sagemaker/Endpoints",
					Dimensions: []model.Dimension{
						{Name: "EndpointName", Value: "example-endpoint-three"},
						{Name: "VariantName", Value: "example-endpoint-three-variant-one"},
					},
				},
			},
			expectedSkip:     true,
			expectedResource: nil,
		},
		{
			name: "2 dimensions should match in Upper case",
			args: args{
				dimensionRegexps: config.SupportedServices.GetService("/aws/sagemaker/Endpoints").ToModelDimensionsRegexp(),
				resources:        sagemakerHealthResources,
				metric: &model.Metric{
					MetricName: "MemoryUtilization",
					Namespace:  "/aws/sagemaker/Endpoints",
					Dimensions: []model.Dimension{
						{Name: "EndpointName", Value: "Example-Endpoint-Upper"},
						{Name: "VariantName", Value: "AllTraffic"},
					},
				},
			},
			expectedSkip:     false,
			expectedResource: sagemakerEndpointHealthUpper,
		},
		{
			name: "2 dimensions should match in mixed case ARN",
			args: args{
				dimensionRegexps: config.SupportedServices.GetService("/aws/sagemaker/Endpoints").ToModelDimensionsRegexp(),
				resources:        sagemakerHealthResources,
				metric: &model.Metric{
					MetricName: "MemoryUtilization",
					Namespace:  "/aws/sagemaker/Endpoints",
					Dimensions: []model.Dimension{
						{Name: "EndpointName", Value: "Example-Endpoint-Mixed"},
						{Name: "VariantName", Value: "AllTraffic"},
					},
				},
			},
			expectedSkip:     false,
			expectedResource: sagemakerEndpointHealthMixed,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			associator := NewAssociator(logging.NewNopLogger(), tc.args.dimensionRegexps, tc.args.resources)
			res, skip := associator.AssociateMetricToResource(tc.args.metric)
			require.Equal(t, tc.expectedSkip, skip)
			require.Equal(t, tc.expectedResource, res)
		})
	}
}
