package getmetricdata

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/cloudwatch"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

type meteredClient struct {
	Client
	accountID string
	region    string
}

func NewMeteredClient(client Client, accountID, region string) Client {
	return meteredClient{Client: client, accountID: accountID, region: region}
}

type owner struct {
	businessService string
	service         string
	component       string
}

type meteringKey struct {
	owner
	metric string
	kind   string
}

type meteringCount struct {
	objects int
	units   int
}

type metricIdentity struct {
	Name       string
	Dimensions []model.Dimension
	Period     int64
}

type identityCount struct {
	key     meteringKey
	objects int
}

func metricOwner(tags []model.Tag) owner {
	values := map[string]string{}
	for _, tag := range tags {
		value := strings.TrimSpace(tag.Value)
		if value == "" {
			continue
		}
		if previous, ok := values[tag.Key]; ok && previous != value {
			values[tag.Key] = "_unallocated"
		} else {
			values[tag.Key] = value
		}
	}
	value := func(key string) string {
		if v := values[key]; v != "" && v != "_unknown" {
			return v
		}
		return "_unallocated"
	}
	return owner{value("omd_business_service"), value("omd_service"), value("omd_component")}
}

func countMetering(batch []*model.CloudwatchData) map[meteringKey]meteringCount {
	counts := map[meteringKey]meteringCount{}
	identities := map[string]identityCount{}
	for _, data := range batch {
		key := meteringKey{owner: metricOwner(data.Tags), metric: data.MetricName, kind: "metric"}
		params := data.GetMetricDataProcessingParams
		if params.Expression != "" {
			key.kind = "expression"
			count := counts[key]
			count.objects++
			counts[key] = count
			continue
		}
		dimensions := append([]model.Dimension(nil), data.Dimensions...)
		sort.Slice(dimensions, func(i, j int) bool {
			if dimensions[i].Name == dimensions[j].Name {
				return dimensions[i].Value < dimensions[j].Value
			}
			return dimensions[i].Name < dimensions[j].Name
		})
		encoded, _ := json.Marshal(metricIdentity{Name: data.MetricName, Dimensions: dimensions, Period: params.Period})
		identity := string(encoded)
		entry, ok := identities[identity]
		if !ok {
			entry.key = key
		} else if entry.key.owner != key.owner {
			entry.key.owner = owner{"_unallocated", "_unallocated", "_unallocated"}
		}
		entry.objects++
		identities[identity] = entry
	}
	for _, entry := range identities {
		count := counts[entry.key]
		count.objects += entry.objects
		count.units += (entry.objects + 4) / 5
		counts[entry.key] = count
	}
	return counts
}

func (c meteredClient) GetMetricData(ctx context.Context, batch []*model.CloudwatchData, namespace string, startTime, endTime time.Time) []cloudwatch.MetricDataResult {
	counts := countMetering(batch)
	result := c.Client.GetMetricData(ctx, batch, namespace, startTime, endTime)
	outcome := "returned"
	if result == nil {
		outcome = "failed_or_empty"
	}
	for key, count := range counts {
		labels := []string{c.accountID, c.region, namespace, key.metric, key.businessService, key.service, key.component, key.kind, outcome}
		promutil.CloudwatchGetMetricDataOwnerQueryObjectsCounter.WithLabelValues(labels...).Add(float64(count.objects))
		if key.kind == "metric" {
			promutil.CloudwatchGetMetricDataOwnerEstimatedUnitsCounter.WithLabelValues(labels...).Add(float64(count.units))
		}
	}
	return result
}
