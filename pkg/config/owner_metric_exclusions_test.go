package config

import "testing"

func TestOwnerMetricExclusionValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exclusion OwnerMetricExclusion
		valid     bool
	}{
		{
			name: "component subset",
			exclusion: OwnerMetricExclusion{
				Namespace: "^AWS/EC2$", MetricName: "^CPU.*$",
				Owner: OwnerSelector{Component: "^(api|worker)$"},
			},
			valid: true,
		},
		{
			name: "full owner",
			exclusion: OwnerMetricExclusion{
				Namespace: ".*", MetricName: ".*",
				Owner: OwnerSelector{BusinessService: ".*", Service: ".*", Component: ".*"},
			},
			valid: true,
		},
		{name: "missing namespace", exclusion: OwnerMetricExclusion{MetricName: ".*", Owner: OwnerSelector{Component: ".*"}}},
		{name: "missing metric", exclusion: OwnerMetricExclusion{Namespace: ".*", Owner: OwnerSelector{Component: ".*"}}},
		{name: "missing owner", exclusion: OwnerMetricExclusion{Namespace: ".*", MetricName: ".*"}},
		{name: "bad namespace", exclusion: OwnerMetricExclusion{Namespace: "[", MetricName: ".*", Owner: OwnerSelector{Component: ".*"}}},
		{name: "bad metric", exclusion: OwnerMetricExclusion{Namespace: ".*", MetricName: "[", Owner: OwnerSelector{Component: ".*"}}},
		{name: "bad owner", exclusion: OwnerMetricExclusion{Namespace: ".*", MetricName: ".*", Owner: OwnerSelector{Service: "["}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.exclusion.validate(0); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestOwnerMetricExclusionModelConversion(t *testing.T) {
	exclusions := toModelOwnerMetricExclusions([]OwnerMetricExclusion{{
		Namespace:  "^AWS/(EC2|EBS)$",
		MetricName: "^Volume.*$",
		Owner:      OwnerSelector{Component: "^worker$"},
	}})
	if len(exclusions) != 1 ||
		!exclusions[0].Namespace.MatchString("AWS/EBS") ||
		!exclusions[0].MetricName.MatchString("VolumeReadOps") ||
		exclusions[0].OwnerBusinessService != nil ||
		exclusions[0].OwnerService != nil ||
		!exclusions[0].OwnerComponent.MatchString("worker") {
		t.Fatal("exclusion was not converted to compiled model selectors")
	}
}
