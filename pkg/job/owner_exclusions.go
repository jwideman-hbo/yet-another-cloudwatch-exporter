package job

import (
	"context"
	"strings"

	"github.com/grafana/regexp"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

type ownerTagSet struct {
	values     map[string]string
	present    map[string]bool
	conflicted map[string]bool
}

func newOwnerTagSet(tags []model.Tag) ownerTagSet {
	set := ownerTagSet{values: map[string]string{}, present: map[string]bool{}, conflicted: map[string]bool{}}
	for _, tag := range tags {
		switch tag.Key {
		case "omd_business_service", "omd_service", "omd_component":
		default:
			continue
		}
		value := strings.TrimSpace(tag.Value)
		if value == "" || value == "_unknown" || value == "_unallocated" || set.conflicted[tag.Key] {
			continue
		}
		if previous, ok := set.values[tag.Key]; ok && previous != value {
			set.present[tag.Key] = false
			set.conflicted[tag.Key] = true
			continue
		}
		set.values[tag.Key] = value
		set.present[tag.Key] = true
	}
	return set
}

func (s ownerTagSet) matches(pattern *regexp.Regexp, key string) bool {
	return pattern == nil || s.present[key] && pattern.MatchString(s.values[key])
}

func (s ownerTagSet) label(key string) string {
	if s.present[key] {
		return s.values[key]
	}
	return "_unallocated"
}

func ownerMetricExcluded(data *model.CloudwatchData, owner ownerTagSet, exclusions []model.OwnerMetricExclusion) bool {
	params := data.GetMetricDataProcessingParams
	if params == nil || params.Expression != "" || !params.ReturnData {
		return false
	}
	for _, exclusion := range exclusions {
		if exclusion.Namespace.MatchString(data.Namespace) &&
			exclusion.MetricName.MatchString(data.MetricName) &&
			owner.matches(exclusion.OwnerBusinessService, "omd_business_service") &&
			owner.matches(exclusion.OwnerService, "omd_service") &&
			owner.matches(exclusion.OwnerComponent, "omd_component") {
			return true
		}
	}
	return false
}

func applyOwnerMetricExclusions(ctx context.Context, accountID, region string, exclusions []model.OwnerMetricExclusion, data []*model.CloudwatchData) []*model.CloudwatchData {
	flags := config.FlagsFromCtx(ctx)
	if len(exclusions) == 0 || !flags.IsFeatureEnabled(config.OwnerMetering) || !flags.IsFeatureEnabled(config.OwnerMetricExclusions) {
		return data
	}
	filtered := make([]*model.CloudwatchData, 0, len(data))
	for _, metric := range data {
		owner := newOwnerTagSet(metric.OwnerTags)
		if !ownerMetricExcluded(metric, owner, exclusions) {
			filtered = append(filtered, metric)
			continue
		}
		promutil.CloudwatchGetMetricDataOwnerExcludedQueryObjectsCounter.WithLabelValues(
			accountID,
			region,
			metric.Namespace,
			metric.MetricName,
			owner.label("omd_business_service"),
			owner.label("omd_service"),
			owner.label("omd_component"),
		).Inc()
	}
	return filtered
}
