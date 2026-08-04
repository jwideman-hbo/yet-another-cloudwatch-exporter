package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/grafana/regexp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

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
		Metrics: []*model.MetricConfig{{Name: "CPUUtilization", Source: model.MetricStreamSource, Statistics: []string{"Average"}}},
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
	payload := []byte("{\"account_id\":\"123\",\"region\":\"us-east-1\",\"namespace\":\"AWS/EC2\",\"metric_name\":\"CPUUtilization\",\"dimensions\":{\"InstanceId\":\"i-123\"},\"timestamp\":1700000000000,\"value\":{\"sum\":12,\"count\":2,\"min\":4,\"max\":8}}\n")
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
