package promutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

func TestOwnerMetadataDoesNotChangeExportedMetrics(t *testing.T) {
	value := 42.0
	data := &model.CloudwatchData{
		MetricName:   "TaskInstanceFinished",
		ResourceName: "mwaa",
		Namespace:    "AmazonMWAA",
		Dimensions:   []model.Dimension{{Name: "Environment", Value: "example"}},
		GetMetricDataResult: &model.GetMetricDataResult{
			Statistic: "Sum", Datapoint: &value, Timestamp: time.Unix(100, 0),
		},
	}
	results := []model.CloudwatchMetricResult{{
		Context: &model.ScrapeContext{Region: "us-east-1", AccountID: "123456789012"},
		Data:    []*model.CloudwatchData{data},
	}}
	before, beforeLabels, err := BuildMetrics(results, false, logging.NewNopLogger())
	require.NoError(t, err)
	require.NotEmpty(t, before)
	data.OwnerTags = []model.Tag{
		{Key: "omd_business_service", Value: "commerce"},
		{Key: "omd_service", Value: "payments"},
		{Key: "omd_component", Value: "worker"},
	}
	after, afterLabels, err := BuildMetrics(results, false, logging.NewNopLogger())
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, beforeLabels, afterLabels)
}
