package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grafana/regexp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

func TestConfigureMetricStreamJobsSplitsStatistics(t *testing.T) {
	jobs := model.JobsConfig{DiscoveryJobs: []model.DiscoveryJob{{Metrics: []*model.MetricConfig{{
		Name:       "CPUUtilization",
		Statistics: []string{"Average", "p99"},
	}, {
		Name:       "RequestCount",
		Statistics: []string{"Average"},
	}, {
		Name:        "Quota",
		Expression:  "SERVICE_QUOTA(m1)",
		MetricStats: []model.MetricStat{{MetricName: "RequestCount"}},
	}}}}}
	configureMetricStreamJobs(&jobs)
	if len(jobs.DiscoveryJobs[0].Metrics) != 4 {
		t.Fatalf("expected split and API metrics, got %d", len(jobs.DiscoveryJobs[0].Metrics))
	}
	if jobs.DiscoveryJobs[0].Metrics[0].Source != model.MetricStreamSource || len(jobs.DiscoveryJobs[0].Metrics[0].Statistics) != 1 || jobs.DiscoveryJobs[0].Metrics[0].Statistics[0] != "Average" {
		t.Fatalf("unexpected stream metric: %+v", jobs.DiscoveryJobs[0].Metrics[0])
	}
	if jobs.DiscoveryJobs[0].Metrics[1].Source != "" || len(jobs.DiscoveryJobs[0].Metrics[1].Statistics) != 1 || jobs.DiscoveryJobs[0].Metrics[1].Statistics[0] != "p99" {
		t.Fatalf("unexpected API metric: %+v", jobs.DiscoveryJobs[0].Metrics[1])
	}
	if jobs.DiscoveryJobs[0].Metrics[2].Name != "RequestCount" || jobs.DiscoveryJobs[0].Metrics[2].Source != "" {
		t.Fatalf("metric math base must remain on API path: %+v", jobs.DiscoveryJobs[0].Metrics[2])
	}
	if jobs.DiscoveryJobs[0].Metrics[3].Name != "Quota" {
		t.Fatalf("unexpected metric math metric: %+v", jobs.DiscoveryJobs[0].Metrics[3])
	}
}

func TestAggregateMetricStreamValueHonorsWindow(t *testing.T) {
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	series := metricStreamValue{
		Statistic: "Average",
		Period:    5 * time.Minute,
		Length:    15 * time.Minute,
		Delay:     5 * time.Minute,
		Samples: map[int64]metricStreamSample{
			base.UnixMilli():                      {Value: 10, Count: 2},
			base.Add(4 * time.Minute).UnixMilli(): {Value: 20, Count: 1},
			base.Add(5 * time.Minute).UnixMilli(): {Value: 100, Count: 1},
		},
	}
	value, timestamp, ok := aggregateMetricStreamValue(series, base.Add(10*time.Minute))
	if !ok || value != 40.0/3.0 || !timestamp.Equal(base.Add(4*time.Minute)) {
		t.Fatalf("unexpected aggregate: value=%v timestamp=%v ok=%v", value, timestamp, ok)
	}
}

func TestMetricStreamHandlerUpdatesMetrics(t *testing.T) {
	logger = logging.NewLogger("logfmt", false, "test", true)
	collector := newMetricStreamCollector(model.JobsConfig{DiscoveryJobs: []model.DiscoveryJob{{
		Type:                  "AWS/EC2",
		Regions:               []string{"us-east-1"},
		ExportedTagsOnMetrics: []string{"Environment"},
		DimensionsRegexps: []model.DimensionsRegexp{{
			Regexp:          regexp.MustCompile(`arn:aws:ec2:[^:]+:[^:]+:instance/([^/]+)$`),
			DimensionsNames: []string{"InstanceId"},
		}},
		Metrics: []*model.MetricConfig{{Name: "CPUUtilization", Source: model.MetricStreamSource, Statistics: []string{"Average"}, Period: 60, Length: 300}},
	}}}, "secret")
	collector.UpdateTaggedResources([]model.TaggedResourceResult{{
		JobID:     "discovery-0",
		Region:    "us-east-1",
		AccountID: "123",
		DimensionsRegexps: []model.DimensionsRegexp{{
			Regexp:          regexp.MustCompile(`arn:aws:ec2:[^:]+:[^:]+:instance/([^/]+)$`),
			DimensionsNames: []string{"InstanceId"},
		}},
		Data: []*model.TaggedResource{{ARN: "arn:aws:ec2:us-east-1:123:instance/i-123", Tags: []model.Tag{{Key: "Environment", Value: "dev"}}}},
	}})
	timestamp := time.Now().Add(-time.Minute).UnixMilli()
	payload := []byte(fmt.Sprintf("{\"account_id\":\"123\",\"region\":\"us-east-1\",\"namespace\":\"AWS/EC2\",\"metric_name\":\"CPUUtilization\",\"dimensions\":{\"InstanceId\":\"i-123\"},\"timestamp\":%d,\"value\":{\"sum\":12,\"count\":2,\"min\":4,\"max\":8}}\n", timestamp))
	data := base64.StdEncoding.EncodeToString(payload)
	body, _ := json.Marshal(metricStreamRequest{RequestID: "request-1", Timestamp: 1700000001000, Records: []metricStreamEntry{{Data: data}}})

	req := httptest.NewRequest("POST", "/firehose", bytes.NewReader(body))
	req.Header.Set("X-Amz-Firehose-Access-Key", "secret")
	resp := httptest.NewRecorder()
	collector.Handler(resp, req)
	if resp.Code != 200 {
		t.Fatalf("unexpected status: %d", resp.Code)
	}
	var response metricStreamResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "request-1" || response.Timestamp == 0 {
		t.Fatalf("unexpected response: %+v", response)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || len(families[0].Metric) != 1 {
		t.Fatalf("unexpected gathered metrics: %+v", families)
	}
	if got := families[0].Metric[0].GetGauge().GetValue(); got != 6 {
		t.Fatalf("unexpected value: %v", got)
	}
	labels := families[0].Metric[0].GetLabel()
	if len(labels) != 5 || labels[0].GetName() == "" {
		t.Fatalf("unexpected labels: %+v", labels)
	}
	foundTag := false
	for _, label := range labels {
		if label.GetName() == "tag_Environment" && label.GetValue() == "dev" {
			foundTag = true
		}
	}
	if !foundTag {
		t.Fatalf("resource tag was not exported: %+v", labels)
	}
}

func TestMetricStreamHandlerRejectsInvalidAccessKey(t *testing.T) {
	collector := newMetricStreamCollector(model.JobsConfig{}, "secret")
	req := httptest.NewRequest("POST", "/firehose", bytes.NewReader([]byte(`{}`)))
	resp := httptest.NewRecorder()
	collector.Handler(resp, req)
	if resp.Code != 401 {
		t.Fatalf("unexpected status: %d", resp.Code)
	}
}

func TestMetricStreamTimestampFallback(t *testing.T) {
	got, ok := metricStreamTimestamp(nil)
	if ok || !got.IsZero() {
		t.Fatalf("unexpected fallback timestamp: %v", got)
	}
}
