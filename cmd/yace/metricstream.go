package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/job/maxdimassociator"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

type metricStreamRecord struct {
	AccountID       string            `json:"account_id"`
	Region          string            `json:"region"`
	Namespace       string            `json:"namespace"`
	MetricName      string            `json:"metric_name"`
	Dimensions      map[string]string `json:"dimensions"`
	Timestamp       json.RawMessage   `json:"timestamp"`
	Value           json.RawMessage   `json:"value"`
	StatisticValues struct {
		SampleCount float64 `json:"sample_count"`
		Sum         float64 `json:"sum"`
		Minimum     float64 `json:"minimum"`
		Maximum     float64 `json:"maximum"`
	} `json:"statistic_values"`
}

type metricStreamRequest struct {
	RequestID string              `json:"requestId"`
	Timestamp int64               `json:"timestamp"`
	Records   []metricStreamEntry `json:"records"`
}

type metricStreamEntry struct {
	Data string `json:"data"`
}

type metricStreamResponse struct {
	RequestID string `json:"requestId"`
	Timestamp int64  `json:"timestamp"`
}

type metricStreamTarget struct {
	JobID                     string
	Regions                   []string
	Namespace                 string
	Metric                    *model.MetricConfig
	CustomTags                []model.Tag
	ExportedTags              []string
	DimensionNameRequirements []string
	Name                      string
}

type metricStreamMapping struct {
	Region     string
	AccountID  string
	Associator maxdimassociator.Associator
}

type metricStreamValue struct {
	Name             string
	LabelNames       []string
	LabelValues      []string
	Value            float64
	IncludeTimestamp bool
	Timestamp        time.Time
}

type metricStreamCollector struct {
	mu       sync.RWMutex
	targets  []metricStreamTarget
	mappings map[string]metricStreamMapping
	values   map[string]metricStreamValue
	access   string
}

func newMetricStreamCollector(jobs model.JobsConfig, access string) *metricStreamCollector {
	collector := &metricStreamCollector{mappings: map[string]metricStreamMapping{}, values: map[string]metricStreamValue{}, access: access}
	collector.updateJobs(jobs)
	return collector
}

func (c *metricStreamCollector) updateJobs(jobs model.JobsConfig) {
	targets := make([]metricStreamTarget, 0)
	for jobIndex, job := range jobs.DiscoveryJobs {
		for _, metric := range job.Metrics {
			targets = append(targets, metricStreamTarget{
				JobID:                     fmt.Sprintf("discovery-%d", jobIndex),
				Regions:                   job.Regions,
				Namespace:                 job.Type,
				Metric:                    metric,
				CustomTags:                job.CustomTags,
				ExportedTags:              job.ExportedTagsOnMetrics,
				DimensionNameRequirements: job.DimensionNameRequirements,
				Name:                      "global",
			})
		}
	}
	for _, job := range jobs.StaticJobs {
		for _, metric := range job.Metrics {
			targets = append(targets, metricStreamTarget{Regions: job.Regions, Namespace: job.Namespace, Metric: metric, CustomTags: job.CustomTags, Name: job.Name})
		}
	}
	for _, job := range jobs.CustomNamespaceJobs {
		for _, metric := range job.Metrics {
			targets = append(targets, metricStreamTarget{Regions: job.Regions, Namespace: job.Namespace, Metric: metric, CustomTags: job.CustomTags, Name: job.Name})
		}
	}
	c.mu.Lock()
	c.targets = targets
	c.mappings = map[string]metricStreamMapping{}
	c.mu.Unlock()
}

func (c *metricStreamCollector) UpdateJobs(jobs model.JobsConfig) {
	c.updateJobs(jobs)
}

func (c *metricStreamCollector) UpdateTaggedResources(results []model.TaggedResourceResult) {
	mappings := make(map[string]metricStreamMapping, len(results))
	for _, result := range results {
		if result.JobID == "" || result.Region == "" || result.AccountID == "" {
			continue
		}
		mappings[metricStreamMappingKey(result.JobID, result.Region, result.AccountID)] = metricStreamMapping{
			Region:     result.Region,
			AccountID:  result.AccountID,
			Associator: maxdimassociator.NewAssociator(logger, result.DimensionsRegexps, result.Data),
		}
	}
	c.mu.Lock()
	c.mappings = mappings
	c.mu.Unlock()
}

func (c *metricStreamCollector) Enabled() bool {
	return c.access != ""
}

func (c *metricStreamCollector) Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Amz-Firehose-Access-Key") != c.access {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var request metricStreamRequest
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, entry := range request.Records {
		data, err := base64.StdEncoding.DecodeString(entry.Data)
		if err != nil {
			continue
		}
		data, err = decodeMetricStreamPayload(data)
		if err != nil {
			continue
		}
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var record metricStreamRecord
			if json.Unmarshal(line, &record) == nil {
				c.update(record)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metricStreamResponse{RequestID: request.RequestID, Timestamp: time.Now().UnixMilli()})
}

func (c *metricStreamCollector) update(record metricStreamRecord) {
	c.mu.RLock()
	targets := append([]metricStreamTarget(nil), c.targets...)
	mappings := make(map[string]metricStreamMapping, len(c.mappings))
	for key, mapping := range c.mappings {
		mappings[key] = mapping
	}
	c.mu.RUnlock()

	for _, target := range targets {
		if target.Metric.Source != model.MetricStreamSource || target.Metric.Expression != "" || target.Namespace != record.Namespace || target.Metric.Name != record.MetricName || !regionMatches(target.Regions, record.Region) || !dimensionsMatchNames(record.Dimensions, target.DimensionNameRequirements) {
			continue
		}

		name := target.Name
		var resourceTags []model.Tag
		if target.JobID != "" {
			mapping, ok := mappings[metricStreamMappingKey(target.JobID, record.Region, record.AccountID)]
			if ok {
				cwMetric := model.Metric{Namespace: record.Namespace, MetricName: record.MetricName}
				for dimension, value := range record.Dimensions {
					cwMetric.Dimensions = append(cwMetric.Dimensions, model.Dimension{Name: dimension, Value: value})
				}
				resource, skip := mapping.Associator.AssociateMetricToResource(&cwMetric)
				if skip {
					continue
				}
				if resource != nil {
					name = resource.ARN
					resourceTags = resource.MetricTags(target.ExportedTags)
				}
			}
		}

		labels := map[string]string{"account_id": record.AccountID, "name": name, "region": record.Region}
		for dimension, value := range record.Dimensions {
			if ok, label := promutil.PromStringTag(dimension, labelsSnakeCase); ok {
				labels["dimension_"+label] = value
			}
		}
		for _, tag := range resourceTags {
			if ok, label := promutil.PromStringTag(tag.Key, labelsSnakeCase); ok {
				labels["tag_"+label] = tag.Value
			}
		}
		for _, tag := range target.CustomTags {
			if ok, label := promutil.PromStringTag(tag.Key, labelsSnakeCase); ok {
				labels["custom_tag_"+label] = tag.Value
			}
		}
		labelNames := make([]string, 0, len(labels))
		labelValues := make([]string, 0, len(labels))
		for label := range labels {
			labelNames = append(labelNames, label)
		}
		sort.Strings(labelNames)
		for _, label := range labelNames {
			labelValues = append(labelValues, labels[label])
		}

		stats := metricStreamStats(record.Value, record.StatisticValues)
		timestamp, timestampOK := metricStreamTimestamp(record.Timestamp)
		for _, statistic := range target.Metric.Statistics {
			value, ok := stats[statistic]
			if !ok {
				continue
			}
			if !timestampOK {
				timestamp = time.Now()
			}
			metricName := promutil.BuildMetricName(record.Namespace, record.MetricName, statistic)
			key := metricName + "\x00" + strings.Join(labelValues, "\x00")
			c.mu.Lock()
			if current, exists := c.values[key]; exists && current.Timestamp.After(timestamp) {
				c.mu.Unlock()
				continue
			}
			c.values[key] = metricStreamValue{
				Name:             metricName,
				LabelNames:       append([]string(nil), labelNames...),
				LabelValues:      append([]string(nil), labelValues...),
				Value:            value,
				IncludeTimestamp: target.Metric.AddCloudwatchTimestamp,
				Timestamp:        timestamp,
			}
			c.mu.Unlock()
		}
	}
}

func (c *metricStreamCollector) Describe(_ chan<- *prometheus.Desc) {}

func (c *metricStreamCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, value := range c.values {
		desc := prometheus.NewDesc(value.Name, "Help is not implemented yet.", value.LabelNames, nil)
		metric := prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value.Value, value.LabelValues...)
		if value.IncludeTimestamp {
			ch <- prometheus.NewMetricWithTimestamp(value.Timestamp, metric)
		} else {
			ch <- metric
		}
	}
}

func metricStreamMappingKey(jobID, region, accountID string) string {
	return jobID + "\x00" + region + "\x00" + accountID
}

func regionMatches(regions []string, region string) bool {
	for _, candidate := range regions {
		if candidate == region || candidate == "*" {
			return true
		}
	}
	return false
}

func dimensionsMatchNames(dimensions map[string]string, requirements []string) bool {
	if len(requirements) == 0 {
		return true
	}
	if len(dimensions) != len(requirements) {
		return false
	}
	for name := range dimensions {
		found := false
		for _, requirement := range requirements {
			if name == requirement {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func metricStreamStats(value json.RawMessage, statisticValues struct {
	SampleCount float64 `json:"sample_count"`
	Sum         float64 `json:"sum"`
	Minimum     float64 `json:"minimum"`
	Maximum     float64 `json:"maximum"`
}) map[string]float64 {
	stats := map[string]float64{}
	var scalar float64
	if json.Unmarshal(value, &scalar) == nil {
		stats["Average"] = scalar
	}
	var aggregate struct {
		Count float64 `json:"count"`
		Sum   float64 `json:"sum"`
		Min   float64 `json:"min"`
		Max   float64 `json:"max"`
	}
	if json.Unmarshal(value, &aggregate) == nil && aggregate.Count > 0 {
		stats["Average"] = aggregate.Sum / aggregate.Count
		stats["Sum"] = aggregate.Sum
		stats["Minimum"] = aggregate.Min
		stats["Maximum"] = aggregate.Max
		stats["SampleCount"] = aggregate.Count
	}
	if statisticValues.SampleCount > 0 {
		stats["Average"] = statisticValues.Sum / statisticValues.SampleCount
		stats["Sum"] = statisticValues.Sum
		stats["Minimum"] = statisticValues.Minimum
		stats["Maximum"] = statisticValues.Maximum
		stats["SampleCount"] = statisticValues.SampleCount
	}
	return stats
}

func metricStreamTimestamp(value json.RawMessage) (time.Time, bool) {
	var milliseconds float64
	if json.Unmarshal(value, &milliseconds) == nil {
		return time.UnixMilli(int64(milliseconds)), true
	}
	var timestamp string
	if json.Unmarshal(value, &timestamp) == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func decodeMetricStreamPayload(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
