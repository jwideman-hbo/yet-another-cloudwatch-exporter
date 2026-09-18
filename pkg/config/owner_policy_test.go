package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

func TestOwnerPolicyValidation(t *testing.T) {
	owner := OwnerSelector{BusinessService: "observability", Service: "metrics-as-a-service", Component: "yace"}
	for _, tc := range []struct {
		name    string
		policy  *OwnerPolicy
		wantErr string
	}{
		{name: "omitted"},
		{name: "all owners", policy: &OwnerPolicy{Mode: model.OwnerPolicyModeAllOwners}},
		{
			name: "all owners with exceptions",
			policy: &OwnerPolicy{
				Mode:   model.OwnerPolicyModeAllOwners,
				Except: []OwnerSelector{{BusinessService: "commerce"}, owner},
			},
		},
		{
			name: "selected owners",
			policy: &OwnerPolicy{
				Mode:   model.OwnerPolicyModeSelectedOwners,
				Owners: []OwnerSelector{{BusinessService: "commerce", Service: "checkout"}, owner},
			},
		},
		{name: "unknown mode", policy: &OwnerPolicy{Mode: "unknown"}, wantErr: "mode must be"},
		{name: "selected owners empty", policy: &OwnerPolicy{Mode: model.OwnerPolicyModeSelectedOwners}, wantErr: "owners must not be empty"},
		{
			name: "owners in all owners mode",
			policy: &OwnerPolicy{
				Mode:   model.OwnerPolicyModeAllOwners,
				Owners: []OwnerSelector{owner},
			},
			wantErr: "owners cannot be set",
		},
		{
			name: "except in selected owners mode",
			policy: &OwnerPolicy{
				Mode:   model.OwnerPolicyModeSelectedOwners,
				Owners: []OwnerSelector{owner},
				Except: []OwnerSelector{{BusinessService: "commerce"}},
			},
			wantErr: "except cannot be set",
		},
		{
			name:    "missing business service",
			policy:  &OwnerPolicy{Mode: model.OwnerPolicyModeAllOwners, Except: []OwnerSelector{{Service: "checkout"}}},
			wantErr: "businessService must not be empty",
		},
		{
			name:    "component missing service",
			policy:  &OwnerPolicy{Mode: model.OwnerPolicyModeAllOwners, Except: []OwnerSelector{{BusinessService: "commerce", Component: "worker"}}},
			wantErr: "component requires service",
		},
		{
			name:    "reserved value",
			policy:  &OwnerPolicy{Mode: model.OwnerPolicyModeAllOwners, Except: []OwnerSelector{{BusinessService: "_unallocated"}}},
			wantErr: "cannot select _unallocated",
		},
		{
			name:    "surrounding whitespace",
			policy:  &OwnerPolicy{Mode: model.OwnerPolicyModeAllOwners, Except: []OwnerSelector{{BusinessService: " commerce"}}},
			wantErr: "surrounding whitespace",
		},
		{
			name:    "duplicate owner",
			policy:  &OwnerPolicy{Mode: model.OwnerPolicyModeSelectedOwners, Owners: []OwnerSelector{owner, owner}},
			wantErr: "duplicate owner selector",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.validate("test policy")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestOwnerPolicyModelConversion(t *testing.T) {
	policy := &OwnerPolicy{
		Mode: model.OwnerPolicyModeSelectedOwners,
		Owners: []OwnerSelector{
			{BusinessService: "commerce"},
			{BusinessService: "observability", Service: "metrics-as-a-service", Component: "yace"},
		},
	}
	want := &model.OwnerPolicy{
		Mode: model.OwnerPolicyModeSelectedOwners,
		Owners: []model.OwnerSelector{
			{BusinessService: "commerce"},
			{BusinessService: "observability", Service: "metrics-as-a-service", Component: "yace"},
		},
	}
	if got := toModelOwnerPolicy(policy); !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v, want=%#v", got, want)
	}
	if got := toModelOwnerPolicy(nil); got != nil {
		t.Fatalf("nil policy converted to %#v", got)
	}
}

func TestOwnerPoliciesPropagateAcrossConfigurationLevels(t *testing.T) {
	deploymentPolicy := &OwnerPolicy{
		Mode:   model.OwnerPolicyModeAllOwners,
		Except: []OwnerSelector{{BusinessService: "media-supply-chain"}},
	}
	jobPolicy := &OwnerPolicy{
		Mode:   model.OwnerPolicyModeAllOwners,
		Except: []OwnerSelector{{BusinessService: "playback-services"}},
	}
	metricPolicy := &OwnerPolicy{
		Mode:   model.OwnerPolicyModeSelectedOwners,
		Owners: []OwnerSelector{{BusinessService: "observability", Service: "metrics-as-a-service"}},
	}
	conf := ScrapeConf{
		APIVersion:  "v1alpha1",
		OwnerPolicy: deploymentPolicy,
		Discovery: Discovery{Jobs: []*Job{{
			Type:        "AWS/EC2",
			Regions:     []string{"us-east-1"},
			Roles:       []Role{{}},
			OwnerPolicy: jobPolicy,
			Metrics: []*Metric{{
				Name:        "CPUUtilization",
				Statistics:  []string{"Average"},
				OwnerPolicy: metricPolicy,
			}},
		}}},
	}

	got, err := conf.Validate(logging.NewNopLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.OwnerPolicy, toModelOwnerPolicy(deploymentPolicy)) ||
		!reflect.DeepEqual(got.DiscoveryJobs[0].OwnerPolicy, toModelOwnerPolicy(jobPolicy)) ||
		!reflect.DeepEqual(got.DiscoveryJobs[0].Metrics[0].OwnerPolicy, toModelOwnerPolicy(metricPolicy)) {
		t.Fatalf("policies did not propagate: %#v", got)
	}
}

func TestStaticMetricRejectsOwnerPolicy(t *testing.T) {
	job := Static{
		Name:      "static",
		Namespace: "AWS/EC2",
		Regions:   []string{"us-east-1"},
		Roles:     []Role{{}},
		Metrics: []*Metric{{
			Name:       "CPUUtilization",
			Statistics: []string{"Average"},
			OwnerPolicy: &OwnerPolicy{
				Mode:   model.OwnerPolicyModeSelectedOwners,
				Owners: []OwnerSelector{{BusinessService: "observability"}},
			},
		}},
	}
	if err := job.validateStaticJob(logging.NewNopLogger(), 0); err == nil || !strings.Contains(err.Error(), "ownerPolicy is not supported for static jobs") {
		t.Fatalf("unexpected error: %v", err)
	}
}
