package job

import (
	"context"
	"strings"
	"sync"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/cloudwatch"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

func runCustomNamespaceJob(
	ctx context.Context,
	logger logging.Logger,
	job model.CustomNamespaceJob,
	clientCloudwatch cloudwatch.Client,
	gmdProcessor getMetricDataProcessor,
) []*model.CloudwatchData {
	cloudwatchDatas := getMetricDataForQueriesForCustomNamespace(ctx, job, clientCloudwatch, logger)
	if len(cloudwatchDatas) == 0 {
		logger.Debug("No metrics data found")
		return nil
	}

	jobLength := getLargestLengthForMetrics(job.Metrics)
	var err error
	cloudwatchDatas, err = gmdProcessor.Run(ctx, job.Namespace, jobLength, job.Delay, job.RoundingPeriod, cloudwatchDatas)
	if err != nil {
		logger.Error(err, "Failed to get metric data")
		return nil
	}

	return cloudwatchDatas
}

func getMetricDataForQueriesForCustomNamespace(
	ctx context.Context,
	customNamespaceJob model.CustomNamespaceJob,
	clientCloudwatch cloudwatch.Client,
	logger logging.Logger,
) []*model.CloudwatchData {
	mux := &sync.Mutex{}
	var getMetricDatas []*model.CloudwatchData

	var wg sync.WaitGroup
	wg.Add(len(customNamespaceJob.Metrics))

	for _, metric := range customNamespaceJob.Metrics {
		// For every metric of the job get the full list of metrics.
		// This includes, for this metric the possible combinations
		// of dimensions and value of dimensions with data.

		go func(metric *model.MetricConfig) {
			defer wg.Done()

			// Custom handling for metric math expressions
			if metric.Expression != "" {
				// For metric math, we don't call ListMetrics since expressions are computed
				// Instead, we create CloudwatchData based on the metricStats configuration
				data := getMetricMathDataForCustomNamespace(logger, customNamespaceJob.Namespace, customNamespaceJob.Name, metric)
				mux.Lock()
				getMetricDatas = append(getMetricDatas, data...)
				mux.Unlock()
				return
			}

			err := clientCloudwatch.ListMetrics(ctx, customNamespaceJob.Namespace, metric, customNamespaceJob.RecentlyActiveOnly, func(page []*model.Metric) {
				var data []*model.CloudwatchData

				for _, cwMetric := range page {
					if len(customNamespaceJob.DimensionNameRequirements) > 0 && !metricDimensionsMatchNames(cwMetric, customNamespaceJob.DimensionNameRequirements) {
						continue
					}

					for _, stat := range metric.Statistics {
						data = append(data, &model.CloudwatchData{
							MetricName:   metric.Name,
							ResourceName: customNamespaceJob.Name,
							Namespace:    customNamespaceJob.Namespace,
							Dimensions:   cwMetric.Dimensions,
							GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{
								Period:     metric.Period,
								Length:     metric.Length,
								Delay:      metric.Delay,
								Statistic:  stat,
								ReturnData: true, // Standard metrics always return data
							},
							MetricMigrationParams: model.MetricMigrationParams{
								NilToZero:              metric.NilToZero,
								AddCloudwatchTimestamp: metric.AddCloudwatchTimestamp,
							},
							Tags:                      nil,
							GetMetricDataResult:       nil,
							GetMetricStatisticsResult: nil,
						})
					}
				}

				mux.Lock()
				getMetricDatas = append(getMetricDatas, data...)
				mux.Unlock()
			})
			if err != nil {
				logger.Error(err, "Failed to get full metric list", "metric_name", metric.Name, "namespace", customNamespaceJob.Namespace)
				return
			}
		}(metric)
	}

	wg.Wait()

	// This is necessary for metric math where multiple expressions may reference the same base metrics
	getMetricDatas = deduplicateByQueryIDCustom(getMetricDatas, logger)

	return getMetricDatas
}

// Removes duplicate CloudwatchData entries based on QueryID
func deduplicateByQueryIDCustom(data []*model.CloudwatchData, logger logging.Logger) []*model.CloudwatchData {
	seen := make(map[string]*model.CloudwatchData)
	var result []*model.CloudwatchData

	for _, d := range data {
		if d.GetMetricDataProcessingParams == nil {
			// No QueryID, keep as-is
			result = append(result, d)
			continue
		}

		queryID := d.GetMetricDataProcessingParams.QueryID
		if queryID == "" {
			// Empty QueryID, keep as-is
			result = append(result, d)
			continue
		}

		if existing, exists := seen[queryID]; exists {
			// Duplicate found - skip it but log for debugging
			logger.Debug("Deduplicated metric with QueryID", "query_id", queryID, "metric", d.MetricName)
			_ = existing // Keep the first occurrence
			continue
		}

		seen[queryID] = d
		result = append(result, d)
	}

	return result
}

// getMetricMathDataForCustomNamespace creates CloudwatchData entries for a metric math expression in custom namespace jobs.
// It creates entries for both the base metrics (with ReturnData=false) and the expression itself (with ReturnData=true).
func getMetricMathDataForCustomNamespace(logger logging.Logger, namespace string, resourceName string, metric *model.MetricConfig) []*model.CloudwatchData {
	var cloudwatchData []*model.CloudwatchData

	// Create CloudwatchData entries for each base metric referenced in the expression
	for _, metricStat := range metric.MetricStats {
		// Use the metric stat's namespace if specified, otherwise use the job namespace
		metricNamespace := metricStat.Namespace
		if metricNamespace == "" {
			metricNamespace = namespace
		}

		// Use the metric stat's period if specified, otherwise use the metric's period
		period := metricStat.Period
		if period == 0 {
			period = metric.Period
		}

		cloudwatchData = append(cloudwatchData, &model.CloudwatchData{
			MetricName:   metricStat.MetricName,
			ResourceName: resourceName,
			Namespace:    metricNamespace,
			Dimensions:   metricStat.Dimensions,
			GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{
				QueryID:    metricStat.Id,
				Period:     period,
				Length:     metric.Length,
				Delay:      metric.Delay,
				Statistic:  metricStat.Statistic,
				ReturnData: false, // Base metrics don't return data
			},
			MetricMigrationParams: model.MetricMigrationParams{
				NilToZero:              metric.NilToZero,
				AddCloudwatchTimestamp: metric.AddCloudwatchTimestamp,
				ExportAllDataPoints:    metric.ExportAllDataPoints,
			},
			Tags:                      nil,
			GetMetricDataResult:       nil,
			GetMetricStatisticsResult: nil,
		})
	}

	// Create CloudwatchData entry for the expression itself
	label := metric.Label
	if label == "" {
		label = metric.Name
	}

	// Sanitize metric name to lowercase for QueryID (CloudWatch requirement)
	sanitizedName := strings.ToLower(strings.ReplaceAll(metric.Name, "-", "_"))

	cloudwatchData = append(cloudwatchData, &model.CloudwatchData{
		MetricName:   metric.Name,
		ResourceName: resourceName,
		Namespace:    namespace,
		Dimensions:   nil, // Expressions don't have dimensions
		GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{
			QueryID:    "expr_" + sanitizedName, // Unique ID for the expression
			Period:     metric.Period,
			Length:     metric.Length,
			Delay:      metric.Delay,
			Expression: metric.Expression,
			Label:      label,
			ReturnData: true, // Expression returns data
		},
		MetricMigrationParams: model.MetricMigrationParams{
			NilToZero:              metric.NilToZero,
			AddCloudwatchTimestamp: metric.AddCloudwatchTimestamp,
			ExportAllDataPoints:    metric.ExportAllDataPoints,
		},
		Tags:                      nil,
		GetMetricDataResult:       nil,
		GetMetricStatisticsResult: nil,
	})

	logger.Debug("Created metric math data for custom namespace", "metric", metric.Name, "expression", metric.Expression, "base_metrics_count", len(metric.MetricStats))

	return cloudwatchData
}
