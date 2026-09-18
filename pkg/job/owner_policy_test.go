package job

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

type ownerPolicyFlags map[string]bool

func (f ownerPolicyFlags) IsFeatureEnabled(flag string) bool {
	return f[flag]
}

func policyRequest(namespace, metric string, tags []model.Tag) *model.CloudwatchData {
	return &model.CloudwatchData{
		Namespace:                     namespace,
		MetricName:                    metric,
		OwnerTags:                     tags,
		GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{},
	}
}

func allOwnersExcept(selectors ...model.OwnerSelector) *model.OwnerPolicy {
	return &model.OwnerPolicy{Mode: model.OwnerPolicyModeAllOwners, Except: selectors}
}

func selectedOwners(selectors ...model.OwnerSelector) *model.OwnerPolicy {
	return &model.OwnerPolicy{Mode: model.OwnerPolicyModeSelectedOwners, Owners: selectors}
}

func policyContext() context.Context {
	return config.CtxWithFlags(context.Background(), ownerPolicyFlags{
		config.OwnerMetering:        true,
		config.OwnerMetricFiltering: true,
	})
}

func TestOwnerPoliciesRequireBothFlags(t *testing.T) {
	request := policyRequest("AWS/EC2", "CPUUtilization", ownerTags())
	policy := allOwnersExcept(model.OwnerSelector{BusinessService: "commerce"})
	for _, flags := range []ownerPolicyFlags{
		{},
		{config.OwnerMetering: true},
		{config.OwnerMetricFiltering: true},
	} {
		ctx := config.CtxWithFlags(context.Background(), flags)
		if got := applyOwnerPolicies(ctx, "123456789012", "us-east-1", policy, nil, []*model.CloudwatchData{request}); len(got) != 1 {
			t.Fatal("one feature flag activated owner filtering")
		}
	}
	if got := applyOwnerPolicies(policyContext(), "123456789012", "us-east-1", policy, nil, []*model.CloudwatchData{request}); len(got) != 0 {
		t.Fatal("both feature flags did not activate owner filtering")
	}
}

func TestOwnerPoliciesUseExactHierarchicalSelectors(t *testing.T) {
	owner := newOwnerTagSet(ownerTags())
	for _, tc := range []struct {
		name    string
		policy  *model.OwnerPolicy
		allowed bool
	}{
		{
			name:    "business service exception",
			policy:  allOwnersExcept(model.OwnerSelector{BusinessService: "commerce"}),
			allowed: false,
		},
		{
			name:    "service exception",
			policy:  allOwnersExcept(model.OwnerSelector{BusinessService: "commerce", Service: "payments"}),
			allowed: false,
		},
		{
			name: "component selected",
			policy: selectedOwners(model.OwnerSelector{
				BusinessService: "commerce", Service: "payments", Component: "worker",
			}),
			allowed: true,
		},
		{
			name:    "same service under another business service",
			policy:  selectedOwners(model.OwnerSelector{BusinessService: "playback-services", Service: "payments"}),
			allowed: false,
		},
		{
			name:    "component mismatch",
			policy:  selectedOwners(model.OwnerSelector{BusinessService: "commerce", Service: "payments", Component: "api"}),
			allowed: false,
		},
		{
			name:    "regular expression characters are literal",
			policy:  selectedOwners(model.OwnerSelector{BusinessService: "commerce.*"}),
			allowed: false,
		},
		{
			name:    "invalid runtime mode fails open",
			policy:  &model.OwnerPolicy{Mode: "invalid"},
			allowed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerPolicyAllows(owner, tc.policy); got != tc.allowed {
				t.Fatalf("allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestOwnerPoliciesAreCumulative(t *testing.T) {
	owner := newOwnerTagSet(ownerTags())
	commerce := model.OwnerSelector{BusinessService: "commerce"}
	payments := model.OwnerSelector{BusinessService: "commerce", Service: "payments"}
	worker := model.OwnerSelector{BusinessService: "commerce", Service: "payments", Component: "worker"}
	observability := model.OwnerSelector{BusinessService: "observability"}

	for _, tc := range []struct {
		name     string
		policies []*model.OwnerPolicy
		allowed  bool
	}{
		{name: "omitted policies", policies: []*model.OwnerPolicy{nil, nil, nil}, allowed: true},
		{name: "passes every level", policies: []*model.OwnerPolicy{selectedOwners(commerce), selectedOwners(payments), selectedOwners(worker)}, allowed: true},
		{name: "deployment denies", policies: []*model.OwnerPolicy{allOwnersExcept(commerce), selectedOwners(payments), selectedOwners(worker)}},
		{name: "job denies", policies: []*model.OwnerPolicy{selectedOwners(commerce), selectedOwners(observability), selectedOwners(worker)}},
		{name: "metric denies", policies: []*model.OwnerPolicy{selectedOwners(commerce), selectedOwners(payments), allOwnersExcept(worker)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerPoliciesAllow(owner, tc.policies...); got != tc.allowed {
				t.Fatalf("allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestOwnerPoliciesFailOpenForIncompleteOrConflictingOwners(t *testing.T) {
	policy := selectedOwners(model.OwnerSelector{BusinessService: "observability"})
	requests := []*model.CloudwatchData{
		policyRequest("AWS/EC2", "Missing", nil),
		policyRequest("AWS/EC2", "Partial", []model.Tag{
			{Key: "omd_business_service", Value: "commerce"},
			{Key: "omd_service", Value: "payments"},
		}),
		policyRequest("AWS/EC2", "Unknown", []model.Tag{
			{Key: "omd_business_service", Value: "commerce"},
			{Key: "omd_service", Value: "payments"},
			{Key: "omd_component", Value: "_unknown"},
		}),
		policyRequest("AWS/EC2", "Conflicting", []model.Tag{
			{Key: "omd_business_service", Value: "commerce"},
			{Key: "omd_service", Value: "payments"},
			{Key: "omd_component", Value: "worker"},
			{Key: "omd_component", Value: "api"},
		}),
	}
	wrongSource := policyRequest("AWS/EC2", "ExportedTagsOnly", nil)
	wrongSource.Tags = ownerTags()
	requests = append(requests, wrongSource)

	got := applyOwnerPolicies(policyContext(), "123456789012", "us-east-1", policy, nil, requests)
	if len(got) != len(requests) {
		t.Fatalf("retained %d requests, want %d", len(got), len(requests))
	}
}

func TestOwnerPoliciesUseDiscoveryResourceTags(t *testing.T) {
	request := policyRequest("AWS/Athena", "ProcessedBytes", nil)
	request.ResourceName = "arn:aws:athena:us-east-1:123456789012:workgroup/example"
	resources := []*model.TaggedResource{{ARN: request.ResourceName, Tags: ownerTags()}}
	enrichDiscoveryOwners(policyContext(), request.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{request}, resources)
	policy := allOwnersExcept(model.OwnerSelector{BusinessService: "commerce", Service: "payments", Component: "worker"})
	if got := applyOwnerPolicies(policyContext(), "123456789012", "us-east-1", policy, nil, []*model.CloudwatchData{request}); len(got) != 0 {
		t.Fatal("raw discovery resource tags did not drive filtering")
	}
}

func TestOwnerPoliciesUseCustomNamespaceResourceTags(t *testing.T) {
	request := policyRequest("AmazonMWAA", "DAGDuration.Success", nil)
	request.Dimensions = []model.Dimension{{Name: "Environment", Value: "example"}}
	client := &ownerTagClient{resources: []*model.TaggedResource{{
		ARN: "arn:aws:airflow:us-east-1:123456789012:environment/example", Tags: ownerTags(),
	}}}
	enrichCustomNamespaceOwners(policyContext(), logging.NewNopLogger(), request.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{request}, client)
	policy := allOwnersExcept(model.OwnerSelector{BusinessService: "commerce", Service: "payments", Component: "worker"})
	if got := applyOwnerPolicies(policyContext(), "123456789012", "us-east-1", policy, nil, []*model.CloudwatchData{request}); len(got) != 0 {
		t.Fatal("custom namespace resource tags did not drive filtering")
	}
}

func TestMetricOwnerPolicyAttachedToRequests(t *testing.T) {
	policy := selectedOwners(model.OwnerSelector{BusinessService: "observability"})
	metric := &model.MetricConfig{
		Name: "TestMetric", Statistics: []string{"Average"}, Period: 60, Length: 60, OwnerPolicy: policy,
	}
	discoveryRequests := getFilteredMetricDatas(
		logging.NewNopLogger(),
		"AWS/EC2",
		nil,
		[]*model.Metric{{MetricName: "TestMetric", Namespace: "AWS/EC2"}},
		nil,
		metric,
		nopAssociator{},
	)
	if len(discoveryRequests) != 1 || discoveryRequests[0].OwnerPolicy != policy {
		t.Fatal("discovery request lost its metric owner policy")
	}

	customRequests := getMetricDataForQueriesForCustomNamespace(
		context.Background(),
		model.CustomNamespaceJob{Namespace: "AmazonMWAA", Metrics: []*model.MetricConfig{metric}},
		ownerCloudwatchClient{},
		logging.NewNopLogger(),
	)
	if len(customRequests) != 1 || customRequests[0].OwnerPolicy != policy {
		t.Fatal("custom namespace request lost its metric owner policy")
	}
}

func TestOwnerPolicyCounterCountsEachFilteredQueryOnce(t *testing.T) {
	request := policyRequest("Test/OwnerPolicyCounter", "UniqueMetric", ownerTags())
	request.OwnerPolicy = selectedOwners(model.OwnerSelector{BusinessService: "observability"})
	deploymentPolicy := allOwnersExcept(model.OwnerSelector{BusinessService: "commerce"})
	jobPolicy := selectedOwners(model.OwnerSelector{BusinessService: "observability"})
	counter := promutil.CloudwatchGetMetricDataOwnerExcludedQueryObjectsCounter.WithLabelValues(
		"000000000000", "test-region-1", request.Namespace, request.MetricName, "commerce", "payments", "worker",
	)
	before := testutil.ToFloat64(counter)
	applyOwnerPolicies(policyContext(), "000000000000", "test-region-1", deploymentPolicy, jobPolicy, []*model.CloudwatchData{request})
	if after := testutil.ToFloat64(counter); after-before != 1 {
		t.Fatalf("counter delta=%v", after-before)
	}
}
