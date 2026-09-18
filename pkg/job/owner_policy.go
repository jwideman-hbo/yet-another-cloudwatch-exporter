package job

import (
	"context"
	"strings"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

type ownerTagValue struct {
	value      string
	present    bool
	conflicted bool
}

type ownerTagSet struct {
	businessService ownerTagValue
	service         ownerTagValue
	component       ownerTagValue
}

func newOwnerTagSet(tags []model.Tag) ownerTagSet {
	var owner ownerTagSet
	for _, tag := range tags {
		var value *ownerTagValue
		switch tag.Key {
		case "omd_business_service":
			value = &owner.businessService
		case "omd_service":
			value = &owner.service
		case "omd_component":
			value = &owner.component
		default:
			continue
		}
		value.add(strings.TrimSpace(tag.Value))
	}
	return owner
}

func (v *ownerTagValue) add(value string) {
	if v.conflicted {
		return
	}
	if value == "" || value == "_unknown" || value == "_unallocated" || v.present && v.value != value {
		v.present = false
		v.conflicted = true
		return
	}
	v.value = value
	v.present = true
}

func (s ownerTagSet) complete() bool {
	return s.businessService.present && s.service.present && s.component.present
}

func (s ownerTagSet) matches(selector model.OwnerSelector) bool {
	return s.businessService.value == selector.BusinessService &&
		(selector.Service == "" || s.service.value == selector.Service) &&
		(selector.Component == "" || s.component.value == selector.Component)
}

func ownerPolicyAllows(owner ownerTagSet, policy *model.OwnerPolicy) bool {
	if policy == nil {
		return true
	}
	var selectors []model.OwnerSelector
	var allowMatch bool
	switch policy.Mode {
	case model.OwnerPolicyModeAllOwners:
		selectors = policy.Except
	case model.OwnerPolicyModeSelectedOwners:
		selectors = policy.Owners
		allowMatch = true
	default:
		return true
	}
	for _, selector := range selectors {
		if owner.matches(selector) {
			return allowMatch
		}
	}
	return !allowMatch
}

func ownerPoliciesAllow(owner ownerTagSet, policies ...*model.OwnerPolicy) bool {
	if !owner.complete() {
		return true
	}
	for _, policy := range policies {
		if !ownerPolicyAllows(owner, policy) {
			return false
		}
	}
	return true
}

func applyOwnerPolicies(
	ctx context.Context,
	accountID, region string,
	deploymentPolicy, jobPolicy *model.OwnerPolicy,
	data []*model.CloudwatchData,
) []*model.CloudwatchData {
	flags := config.FlagsFromCtx(ctx)
	if !flags.IsFeatureEnabled(config.OwnerMetering) || !flags.IsFeatureEnabled(config.OwnerMetricFiltering) {
		return data
	}

	filtered := data[:0]
	for _, metric := range data {
		owner := newOwnerTagSet(metric.OwnerTags)
		if metric.GetMetricDataProcessingParams == nil || ownerPoliciesAllow(owner, deploymentPolicy, jobPolicy, metric.OwnerPolicy) {
			filtered = append(filtered, metric)
			continue
		}
		promutil.CloudwatchGetMetricDataOwnerExcludedQueryObjectsCounter.WithLabelValues(
			accountID,
			region,
			metric.Namespace,
			metric.MetricName,
			owner.businessService.value,
			owner.service.value,
			owner.component.value,
		).Inc()
	}
	clear(data[len(filtered):])
	return filtered
}
