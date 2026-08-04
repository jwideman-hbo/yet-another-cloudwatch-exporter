package job

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/cloudwatch"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/job/maxdimassociator"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type resourceAssociator interface {
	AssociateMetricToResource(cwMetric *model.Metric) (*model.TaggedResource, bool)
}

type getMetricDataProcessor interface {
	Run(ctx context.Context, namespace string, jobMetricLength, jobMetricDelay int64, period *int64, requests []*model.CloudwatchData) ([]*model.CloudwatchData, error)
}

func runDiscoveryJob(
	ctx context.Context,
	logger logging.Logger,
	job model.DiscoveryJob,
	region string,
	clientTag tagging.Client,
	clientCloudwatch cloudwatch.Client,
	gmdProcessor getMetricDataProcessor,
) ([]*model.TaggedResource, []*model.CloudwatchData) {
	logger.Debug("Get tagged resources")

	resources, err := clientTag.GetResources(ctx, job, region)
	if err != nil {
		if errors.Is(err, tagging.ErrExpectedToFindResources) {
			logger.Error(err, "No tagged resources made it through filtering")
		} else {
			logger.Error(err, "Couldn't describe resources")
		}
		return nil, nil
	}

	if len(resources) == 0 {
		logger.Debug("No tagged resources", "region", region, "namespace", job.Type)
	}

	svc := config.SupportedServices.GetService(job.Type)
	getMetricDatas := getMetricDataForQueries(ctx, logger, job, svc, clientCloudwatch, resources)
	if len(getMetricDatas) == 0 {
		logger.Info("No metrics data found")
		return resources, nil
	}

	jobLength := getLargestLengthForMetrics(job.Metrics)
	getMetricDatas, err = gmdProcessor.Run(ctx, svc.Namespace, jobLength, job.Delay, job.RoundingPeriod, getMetricDatas)
	if err != nil {
		logger.Error(err, "Failed to get metric data")
		return nil, nil
	}

	return resources, getMetricDatas
}

func getLargestLengthForMetrics(metrics []*model.MetricConfig) int64 {
	var length int64
	for _, metric := range metrics {
		if metric.Length > length {
			length = metric.Length
		}
	}
	return length
}

func getMetricDataForQueries(
	ctx context.Context,
	logger logging.Logger,
	discoveryJob model.DiscoveryJob,
	svc *config.ServiceConfig,
	clientCloudwatch cloudwatch.Client,
	resources []*model.TaggedResource,
) []*model.CloudwatchData {
	mux := &sync.Mutex{}
	var getMetricDatas []*model.CloudwatchData

	var assoc resourceAssociator
	if len(svc.DimensionRegexps) > 0 && len(resources) > 0 {
		assoc = maxdimassociator.NewAssociator(logger, discoveryJob.DimensionsRegexps, resources)
	} else {
		// If we don't have dimension regex's and resources there's nothing to associate but metrics shouldn't be skipped
		assoc = nopAssociator{}
	}

	// Separate standard metrics from metric math expressions
	var standardMetrics []*model.MetricConfig
	var metricMathExpressions []*model.MetricConfig
	for _, metric := range discoveryJob.Metrics {
		if metric.Source == model.MetricStreamSource {
			continue
		}
		if metric.Expression != "" {
			metricMathExpressions = append(metricMathExpressions, metric)
		} else {
			standardMetrics = append(standardMetrics, metric)
		}
	}

	var wg sync.WaitGroup
	wg.Add(len(standardMetrics))

	// First, discover all standard metrics (including base metrics for expressions)
	for _, metric := range standardMetrics {
		go func(metric *model.MetricConfig) {
			defer wg.Done()

			err := clientCloudwatch.ListMetrics(ctx, svc.Namespace, metric, discoveryJob.RecentlyActiveOnly, func(page []*model.Metric) {
				data := getFilteredMetricDatas(logger, discoveryJob.Type, discoveryJob.ExportedTagsOnMetrics, page, discoveryJob.DimensionNameRequirements, metric, assoc)

				mux.Lock()
				getMetricDatas = append(getMetricDatas, data...)
				mux.Unlock()
			})
			if err != nil {
				logger.Error(err, "Failed to get full metric list", "metric_name", metric.Name, "namespace", svc.Namespace)
				return
			}
		}(metric)
	}

	wg.Wait()

	// Now, for each metric math expression, create entries for each discovered dimension combination
	if len(metricMathExpressions) > 0 {
		logger.Debug("Processing metric math expressions", "count", len(metricMathExpressions))

		for _, mathMetric := range metricMathExpressions {
			if len(mathMetric.MetricStats) == 0 {
				// Standalone expression (e.g., DB_PERF_INSIGHTS) - create single entry without dimensions
				logger.Debug("Creating standalone metric math expression", "metric", mathMetric.Name)
				mathData := getMetricMathData(logger, svc.Namespace, mathMetric)
				mux.Lock()
				getMetricDatas = append(getMetricDatas, mathData...)
				mux.Unlock()
				continue
			}

			// Expression with base metrics - discover dimensions from base metrics
			baseMetricName := mathMetric.MetricStats[0].MetricName
			logger.Debug("Creating metric math for discovered dimensions", "expression", mathMetric.Name, "base_metric", baseMetricName)

			// Find all discovered dimension combinations for the base metric
			// Filter to only dimensions that match the metricStat filters (e.g., Service=Prometheus)
			baseDimensions := findDiscoveredDimensionsMatching(getMetricDatas, baseMetricName, mathMetric.MetricStats[0].Dimensions)
			logger.Debug("Found discovered dimensions matching filters", "base_metric", baseMetricName, "count", len(baseDimensions))

			// Create expression entries for each dimension combination
			for _, dims := range baseDimensions {
				mathData := createMetricMathForDimensions(logger, svc.Namespace, mathMetric, dims, assoc)
				mux.Lock()
				getMetricDatas = append(getMetricDatas, mathData...)
				mux.Unlock()
			}
		}
	}

	// Deduplicate CloudwatchData entries by QueryID
	// This is necessary for metric math where multiple expressions may reference the same base metrics
	getMetricDatas = deduplicateByQueryID(getMetricDatas, logger)

	return getMetricDatas
}

// deduplicateByQueryID removes duplicate CloudwatchData entries based on QueryID
// This is needed for metric math expressions that may share base metrics
func deduplicateByQueryID(data []*model.CloudwatchData, logger logging.Logger) []*model.CloudwatchData {
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

// findDiscoveredDimensions extracts all unique dimension combinations for a given metric name
func findDiscoveredDimensions(allData []*model.CloudwatchData, metricName string) [][]model.Dimension {
	seen := make(map[string][]model.Dimension)

	for _, data := range allData {
		if data.MetricName == metricName {
			// Create a unique key from dimensions
			key := dimensionsToKey(data.Dimensions)
			if _, exists := seen[key]; !exists {
				// Make a copy of dimensions
				dimsCopy := make([]model.Dimension, len(data.Dimensions))
				copy(dimsCopy, data.Dimensions)
				seen[key] = dimsCopy
			}
		}
	}

	result := make([][]model.Dimension, 0, len(seen))
	for _, dims := range seen {
		result = append(result, dims)
	}
	return result
}

// findDiscoveredDimensionsMatching extracts unique dimension combinations for a metric
// that match the specified filter dimensions (e.g., Service=Prometheus)
func findDiscoveredDimensionsMatching(allData []*model.CloudwatchData, metricName string, filterDims []model.Dimension) [][]model.Dimension {
	seen := make(map[string][]model.Dimension)

	for _, data := range allData {
		if data.MetricName != metricName {
			continue
		}

		// Check if this metric's dimensions match the filter
		if !dimensionsMatchFilter(data.Dimensions, filterDims) {
			continue
		}

		// Create a unique key from dimensions
		key := dimensionsToKey(data.Dimensions)
		if _, exists := seen[key]; !exists {
			// Make a copy of dimensions
			dimsCopy := make([]model.Dimension, len(data.Dimensions))
			copy(dimsCopy, data.Dimensions)
			seen[key] = dimsCopy
		}
	}

	result := make([][]model.Dimension, 0, len(seen))
	for _, dims := range seen {
		result = append(result, dims)
	}
	return result
}

// dimensionsMatchFilter checks if discovered dimensions match the configured filter dimensions
// For example, if filter has {Service=Prometheus}, discovered dims must also have Service=Prometheus
func dimensionsMatchFilter(discovered []model.Dimension, filter []model.Dimension) bool {
	for _, filterDim := range filter {
		found := false
		for _, discDim := range discovered {
			if discDim.Name == filterDim.Name {
				if discDim.Value != filterDim.Value {
					// Dimension name matches but value doesn't - not a match
					return false
				}
				found = true
				break
			}
		}
		if !found {
			// Required filter dimension not found
			return false
		}
	}
	return true
}

// dimensionsToKey creates a unique string key from a set of dimensions
func dimensionsToKey(dims []model.Dimension) string {
	if len(dims) == 0 {
		return ""
	}
	parts := make([]string, len(dims))
	for i, dim := range dims {
		parts[i] = dim.Name + "=" + dim.Value
	}
	return strings.Join(parts, "|")
}

// createMetricMathForDimensions creates metric math CloudwatchData entries for a specific dimension combination
func createMetricMathForDimensions(logger logging.Logger, namespace string, metric *model.MetricConfig, dimensions []model.Dimension, assoc resourceAssociator) []*model.CloudwatchData {
	var cloudwatchData []*model.CloudwatchData

	// Generate a unique suffix from dimensions for QueryIDs
	dimSuffix := generateDimensionSuffix(dimensions)

	// Create CloudwatchData entries for each base metric referenced in the expression
	for _, metricStat := range metric.MetricStats {
		metricNamespace := metricStat.Namespace
		if metricNamespace == "" {
			metricNamespace = namespace
		}

		period := metricStat.Period
		if period == 0 {
			period = metric.Period
		}

		// Use discovered dimensions, overriding with any specified in metricStat
		finalDimensions := mergeDiscoveredWithConfiguredDimensions(dimensions, metricStat.Dimensions)

		// Try to associate with resource
		cwMetric := &model.Metric{
			Dimensions: finalDimensions,
			MetricName: metricStat.MetricName,
			Namespace:  metricNamespace,
		}
		resource, _ := assoc.AssociateMetricToResource(cwMetric)
		if resource == nil {
			resource = &model.TaggedResource{
				ARN:       "global",
				Namespace: namespace,
			}
		}

		cloudwatchData = append(cloudwatchData, &model.CloudwatchData{
			MetricName:   metricStat.MetricName,
			ResourceName: resource.ARN,
			Namespace:    metricNamespace,
			Dimensions:   finalDimensions,
			GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{
				QueryID:    metricStat.Id + dimSuffix,
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

	// Try to associate expression with resource
	cwMetric := &model.Metric{
		Dimensions: dimensions,
		MetricName: metric.Name,
		Namespace:  namespace,
	}
	resource, _ := assoc.AssociateMetricToResource(cwMetric)
	if resource == nil {
		resource = &model.TaggedResource{
			ARN:       "global",
			Namespace: namespace,
		}
	}

	// Sanitize metric name to lowercase for QueryID (CloudWatch requirement)
	sanitizedName := strings.ToLower(strings.ReplaceAll(metric.Name, "-", "_"))

	// Rewrite the expression to use suffixed IDs (e.g., "m1" -> "m1_ws_abc123")
	rewrittenExpression := rewriteExpressionIDs(metric.Expression, metric.MetricStats, dimSuffix)

	cloudwatchData = append(cloudwatchData, &model.CloudwatchData{
		MetricName:   metric.Name,
		ResourceName: resource.ARN,
		Namespace:    namespace,
		Dimensions:   dimensions,
		GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{
			QueryID:    "expr_" + sanitizedName + dimSuffix,
			Period:     metric.Period,
			Length:     metric.Length,
			Delay:      metric.Delay,
			Expression: rewrittenExpression,
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

	return cloudwatchData
}

// generateDimensionSuffix creates a short unique suffix from dimensions for QueryID uniqueness
func generateDimensionSuffix(dims []model.Dimension) string {
	if len(dims) == 0 {
		return ""
	}
	// Use a hash or simple concatenation - just need something unique
	// For simplicity, find ResourceId if it exists
	for _, dim := range dims {
		if dim.Name == "ResourceId" {
			// Sanitize to only alphanumeric and underscores (CloudWatch requirement)
			sanitized := sanitizeQueryID(dim.Value)
			return "_" + sanitized
		}
	}
	// Fallback: use simple hash of all dimensions
	key := dimensionsToKey(dims)
	hash := uint32(0)
	for i := 0; i < len(key); i++ {
		hash = hash*31 + uint32(key[i])
	}
	return fmt.Sprintf("_h%08x", hash) // Returns _h followed by 8 hex digits (10 chars total)
}

// sanitizeQueryID removes or replaces invalid characters for CloudWatch QueryIDs
// Pattern requirement: ^[a-z][a-zA-Z0-9_]*$
func sanitizeQueryID(s string) string {
	// Replace all non-alphanumeric characters (except underscores) with underscores
	re := regexp.MustCompile(`[^a-zA-Z0-9_]+`)
	return re.ReplaceAllString(s, "_")
}

// mergeDiscoveredWithConfiguredDimensions merges discovered dimensions with any overrides from config
func mergeDiscoveredWithConfiguredDimensions(discovered []model.Dimension, configured []model.Dimension) []model.Dimension {
	if len(configured) == 0 {
		return discovered
	}

	// Start with discovered dimensions
	result := make([]model.Dimension, len(discovered))
	copy(result, discovered)

	// Override with configured dimensions
	for _, configDim := range configured {
		found := false
		for i, dim := range result {
			if dim.Name == configDim.Name {
				result[i] = configDim
				found = true
				break
			}
		}
		if !found {
			result = append(result, configDim)
		}
	}

	return result
}

// rewriteExpressionIDs rewrites metric stat IDs in an expression with suffixed versions
// For example, "SERVICE_QUOTA(m1)" becomes "SERVICE_QUOTA(m1_ws_abc123)"
func rewriteExpressionIDs(expression string, metricStats []model.MetricStat, suffix string) string {
	if suffix == "" {
		return expression
	}

	result := expression
	for _, ms := range metricStats {
		// Use regex to replace only whole identifier matches
		// Match the ID as a complete identifier (not part of another identifier)
		// AWS IDs must match ^[a-z][a-zA-Z0-9_]*$, so we match word boundaries
		pattern := `\b` + regexp.QuoteMeta(ms.Id) + `\b`
		re := regexp.MustCompile(pattern)
		newID := ms.Id + suffix
		result = re.ReplaceAllString(result, newID)
	}
	return result
}

// getMetricMathData creates CloudwatchData entries for a metric math expression.
// It creates entries for both the base metrics (with ReturnData=false) and the expression itself (with ReturnData=true).
func getMetricMathData(logger logging.Logger, namespace string, metric *model.MetricConfig) []*model.CloudwatchData {
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
			ResourceName: "global", // Metric math expressions are not associated with specific resources
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
		ResourceName: "global",
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

	logger.Debug("Created metric math data", "metric", metric.Name, "expression", metric.Expression, "base_metrics_count", len(metric.MetricStats))

	return cloudwatchData
}

type nopAssociator struct{}

func (ns nopAssociator) AssociateMetricToResource(_ *model.Metric) (*model.TaggedResource, bool) {
	return nil, false
}

func getFilteredMetricDatas(
	logger logging.Logger,
	namespace string,
	tagsOnMetrics []string,
	metricsList []*model.Metric,
	dimensionNameList []string,
	m *model.MetricConfig,
	assoc resourceAssociator,
) []*model.CloudwatchData {
	getMetricsData := make([]*model.CloudwatchData, 0, len(metricsList))
	for _, cwMetric := range metricsList {
		if len(dimensionNameList) > 0 && !metricDimensionsMatchNames(cwMetric, dimensionNameList) {
			continue
		}

		matchedResource, skip := assoc.AssociateMetricToResource(cwMetric)
		if skip {
			if logger.IsDebugEnabled() {
				dimensions := make([]string, 0, len(cwMetric.Dimensions))
				for _, dim := range cwMetric.Dimensions {
					dimensions = append(dimensions, fmt.Sprintf("%s=%s", dim.Name, dim.Value))
				}
				logger.Debug("skipping metric unmatched by associator", "metric", m.Name, "dimensions", strings.Join(dimensions, ","))
			}
			continue
		}

		resource := matchedResource
		if resource == nil {
			resource = &model.TaggedResource{
				ARN:       "global",
				Namespace: namespace,
			}
		}

		metricTags := resource.MetricTags(tagsOnMetrics)
		for _, stat := range m.Statistics {
			getMetricsData = append(getMetricsData, &model.CloudwatchData{
				MetricName:   m.Name,
				ResourceName: resource.ARN,
				Namespace:    namespace,
				Dimensions:   cwMetric.Dimensions,
				GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{
					Period:     m.Period,
					Length:     m.Length,
					Delay:      m.Delay,
					Statistic:  stat,
					ReturnData: true, // Standard metrics always return data
				},
				MetricMigrationParams: model.MetricMigrationParams{
					NilToZero:              m.NilToZero,
					AddCloudwatchTimestamp: m.AddCloudwatchTimestamp,
				},
				Tags:                      metricTags,
				GetMetricDataResult:       nil,
				GetMetricStatisticsResult: nil,
			})
		}
	}
	return getMetricsData
}

func metricDimensionsMatchNames(metric *model.Metric, dimensionNameRequirements []string) bool {
	if len(dimensionNameRequirements) != len(metric.Dimensions) {
		return false
	}
	for _, dimension := range metric.Dimensions {
		foundMatch := false
		for _, dimensionName := range dimensionNameRequirements {
			if dimension.Name == dimensionName {
				foundMatch = true
				break
			}
		}
		if !foundMatch {
			return false
		}
	}
	return true
}
