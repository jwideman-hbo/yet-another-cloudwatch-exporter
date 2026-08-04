package main

import (
	"testing"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

func TestBuildMetricStreamConfig(t *testing.T) {
	output := buildMetricStreamConfig(model.JobsConfig{
		DiscoveryJobs: []model.DiscoveryJob{{
			Type: "AWS/EC2",
			Metrics: []*model.MetricConfig{{
				Name:       "CPUUtilization",
				Statistics: []string{"Average", "p99", "p99.9"},
			}, {
				Name:       "NetworkIn",
				Statistics: []string{"Sum"},
			}, {
				Name:       "Quota",
				Expression: "SERVICE_QUOTA(m1)",
			}},
		}},
		StaticJobs: []model.StaticJob{{
			Namespace: "AWS/EC2",
			Metrics:   []*model.MetricConfig{{Name: "CPUUtilization", Statistics: []string{"Minimum"}}},
		}},
	})

	if len(output.IncludeFilters) != 1 || output.IncludeFilters[0].Namespace != "AWS/EC2" {
		t.Fatalf("unexpected filters: %+v", output.IncludeFilters)
	}
	if got := output.IncludeFilters[0].MetricNames; len(got) != 2 || got[0] != "CPUUtilization" || got[1] != "NetworkIn" {
		t.Fatalf("unexpected metric names: %+v", got)
	}
	if len(output.StatisticsConfigurations) != 1 {
		t.Fatalf("unexpected statistics configuration: %+v", output.StatisticsConfigurations)
	}
	stats := output.StatisticsConfigurations[0]
	if stats.Namespace != "AWS/EC2" || stats.MetricName != "CPUUtilization" || len(stats.AdditionalStatistics) != 2 || stats.AdditionalStatistics[0] != "p99" || stats.AdditionalStatistics[1] != "p99.9" {
		t.Fatalf("unexpected additional statistics: %+v", stats)
	}
}
