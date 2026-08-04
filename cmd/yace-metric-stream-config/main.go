package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

type includeFilter struct {
	Namespace   string   `json:"namespace"`
	MetricNames []string `json:"metric_names"`
}

type statisticsConfiguration struct {
	Namespace            string   `json:"namespace"`
	MetricName           string   `json:"metric_name"`
	AdditionalStatistics []string `json:"additional_statistics"`
}

type metricStreamConfig struct {
	IncludeFilters           []includeFilter           `json:"include_filters"`
	StatisticsConfigurations []statisticsConfiguration `json:"statistics_configurations"`
}

func main() {
	configFile := flag.String("config.file", "config.yml", "Path to YACE configuration")
	flag.Parse()

	logger := logging.NewLogger("logfmt", false, "component", "yace-metric-stream-config")
	scrapeConfig := config.ScrapeConf{}
	jobs, err := scrapeConfig.Load(*configFile, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	output := buildMetricStreamConfig(jobs)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintf(os.Stderr, "write config: %v\n", err)
		os.Exit(1)
	}
}

func buildMetricStreamConfig(jobs model.JobsConfig) metricStreamConfig {
	metricsByNamespace := map[string]map[string]struct{}{}
	additionalByMetric := map[string]map[string]map[string]struct{}{}

	addJob := func(namespace string, metrics []*model.MetricConfig) {
		if _, ok := metricsByNamespace[namespace]; !ok {
			metricsByNamespace[namespace] = map[string]struct{}{}
		}
		if _, ok := additionalByMetric[namespace]; !ok {
			additionalByMetric[namespace] = map[string]map[string]struct{}{}
		}
		for _, metric := range metrics {
			if metric.Expression != "" {
				continue
			}
			metricsByNamespace[namespace][metric.Name] = struct{}{}
			for _, statistic := range metric.Statistics {
				if !promutil.Percentile.MatchString(statistic) {
					continue
				}
				if _, ok := additionalByMetric[namespace][metric.Name]; !ok {
					additionalByMetric[namespace][metric.Name] = map[string]struct{}{}
				}
				additionalByMetric[namespace][metric.Name][statistic] = struct{}{}
			}
		}
	}

	for _, job := range jobs.DiscoveryJobs {
		addJob(job.Type, job.Metrics)
	}
	for _, job := range jobs.StaticJobs {
		addJob(job.Namespace, job.Metrics)
	}
	for _, job := range jobs.CustomNamespaceJobs {
		addJob(job.Namespace, job.Metrics)
	}

	config := metricStreamConfig{
		IncludeFilters:           make([]includeFilter, 0, len(metricsByNamespace)),
		StatisticsConfigurations: make([]statisticsConfiguration, 0),
	}
	namespaces := sortedKeys(metricsByNamespace)
	for _, namespace := range namespaces {
		config.IncludeFilters = append(config.IncludeFilters, includeFilter{
			Namespace:   namespace,
			MetricNames: sortedKeys(metricsByNamespace[namespace]),
		})
		for _, metricName := range sortedKeys(additionalByMetric[namespace]) {
			statistics := sortedKeys(additionalByMetric[namespace][metricName])
			if len(statistics) == 0 {
				continue
			}
			config.StatisticsConfigurations = append(config.StatisticsConfigurations, statisticsConfiguration{
				Namespace:            namespace,
				MetricName:           metricName,
				AdditionalStatistics: statistics,
			})
		}
	}
	return config
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
