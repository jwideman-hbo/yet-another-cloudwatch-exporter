package getmetricdata

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/cloudwatch"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

func meteringData(stat string) *model.CloudwatchData {
	return &model.CloudwatchData{
		MetricName: "CPUUtilization",
		Tags: []model.Tag{
			{Key: "omd_business_service", Value: "commerce"},
			{Key: "omd_service", Value: "payments"},
			{Key: "omd_component", Value: "processor"},
		},
		Dimensions:                    []model.Dimension{{Name: "InstanceId", Value: "i-1"}},
		GetMetricDataProcessingParams: &model.GetMetricDataProcessingParams{Statistic: stat, Period: 60},
	}
}

func TestMeteringStatisticBoundaries(t *testing.T) {
	for _, size := range []int{1, 2, 5, 6, 9, 10, 11} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			batch := make([]*model.CloudwatchData, size)
			for i := range batch {
				batch[i] = meteringData(fmt.Sprintf("p%d", i))
			}
			counts := countMetering(batch)
			key := meteringKey{owner{"commerce", "payments", "processor"}, "CPUUtilization", "metric"}
			if got := counts[key]; got != (meteringCount{size, (size + 4) / 5}) {
				t.Fatalf("unexpected count: %+v", got)
			}
		})
	}
}

func TestMeteringIdentityAndDimensionOrder(t *testing.T) {
	a, b, c, d := meteringData("Average"), meteringData("Maximum"), meteringData("Sum"), meteringData("Average")
	a.Dimensions = []model.Dimension{{Name: "Z", Value: "1"}, {Name: "A", Value: "2"}}
	b.Dimensions = []model.Dimension{{Name: "A", Value: "2"}, {Name: "Z", Value: "1"}}
	c.Dimensions = []model.Dimension{{Name: "InstanceId", Value: "i-2"}}
	d.GetMetricDataProcessingParams.Period = 300
	before := append([]model.Dimension(nil), a.Dimensions...)
	counts := countMetering([]*model.CloudwatchData{a, b, c, d})
	key := meteringKey{owner{"commerce", "payments", "processor"}, "CPUUtilization", "metric"}
	if counts[key] != (meteringCount{4, 3}) {
		t.Fatalf("unexpected count: %+v", counts)
	}
	if !reflect.DeepEqual(before, a.Dimensions) {
		t.Fatal("metering mutated request dimensions")
	}
}

func TestMeteringMissingAndConflictingOwners(t *testing.T) {
	tests := []struct {
		name string
		tags []model.Tag
		want owner
	}{
		{"missing", nil, owner{"_unallocated", "_unallocated", "_unallocated"}},
		{"unknown", []model.Tag{{Key: "omd_service", Value: "_unknown"}}, owner{"_unallocated", "_unallocated", "_unallocated"}},
		{"partial", []model.Tag{{Key: "omd_service", Value: "payments"}}, owner{"_unallocated", "payments", "_unallocated"}},
		{"blank", []model.Tag{{Key: "omd_component", Value: " "}}, owner{"_unallocated", "_unallocated", "_unallocated"}},
		{"conflict", []model.Tag{{Key: "omd_component", Value: "a"}, {Key: "omd_component", Value: "b"}, {Key: "omd_component", Value: "a"}}, owner{"_unallocated", "_unallocated", "_unallocated"}},
		{"legacy", []model.Tag{{Key: "omd_microservice", Value: "legacy"}}, owner{"_unallocated", "_unallocated", "_unallocated"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metricOwner(tt.tags); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	a, b := meteringData("Average"), meteringData("Maximum")
	b.Tags = nil
	counts := countMetering([]*model.CloudwatchData{a, b})
	key := meteringKey{owner{"_unallocated", "_unallocated", "_unallocated"}, "CPUUtilization", "metric"}
	if counts[key] != (meteringCount{2, 1}) {
		t.Fatalf("conflicting identity was attributed: %+v", counts)
	}
}

func TestMeteringUsesResolvedOwnerTagsWithoutExportedTagFallback(t *testing.T) {
	data := meteringData("Average")
	data.OwnerTags = []model.Tag{{Key: "omd_service", Value: "resolved-service"}}
	counts := countMetering([]*model.CloudwatchData{data})
	key := meteringKey{owner{"_unallocated", "resolved-service", "_unallocated"}, "CPUUtilization", "metric"}
	if counts[key] != (meteringCount{1, 1}) {
		t.Fatalf("resolved ownership was not used: %+v", counts)
	}
	data.OwnerTags = []model.Tag{}
	counts = countMetering([]*model.CloudwatchData{data})
	key.owner = owner{"_unallocated", "_unallocated", "_unallocated"}
	if counts[key] != (meteringCount{1, 1}) {
		t.Fatalf("unresolved ownership fell back to exported tags: %+v", counts)
	}
}

func TestMeteringExpressionsAreNotPriced(t *testing.T) {
	data := meteringData("")
	data.GetMetricDataProcessingParams.Expression = "SEARCH('...', 'Average', 60)"
	counts := countMetering([]*model.CloudwatchData{data})
	key := meteringKey{owner{"commerce", "payments", "processor"}, "CPUUtilization", "expression"}
	if counts[key] != (meteringCount{1, 0}) {
		t.Fatalf("expression received a pricing estimate: %+v", counts)
	}
}

type meteringTestClient struct {
	call func(context.Context, []*model.CloudwatchData, string, time.Time, time.Time) []cloudwatch.MetricDataResult
}

func (c meteringTestClient) GetMetricData(ctx context.Context, batch []*model.CloudwatchData, namespace string, start, end time.Time) []cloudwatch.MetricDataResult {
	return c.call(ctx, batch, namespace, start, end)
}

func TestMeteredClientPreservesCallsAndCountsNoData(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			promutil.CloudwatchGetMetricDataOwnerQueryObjectsCounter.Reset()
			promutil.CloudwatchGetMetricDataOwnerEstimatedUnitsCounter.Reset()
			data := meteringData("Average")
			batch := []*model.CloudwatchData{data}
			start, end := time.Unix(100, 0), time.Unix(200, 0)
			result := []cloudwatch.MetricDataResult{{ID: "id_0"}}
			outcome := "returned"
			if failed {
				result = nil
				outcome = "failed_or_empty"
			}
			calls := 0
			inner := meteringTestClient{call: func(_ context.Context, received []*model.CloudwatchData, namespace string, s, e time.Time) []cloudwatch.MetricDataResult {
				calls++
				if received[0] != data || namespace != "AWS/EC2" || s != start || e != end {
					t.Fatal("metering changed API input")
				}
				return result
			}}
			client := NewMeteredClient(inner, "123456789012", "us-east-1")
			got := client.GetMetricData(context.Background(), batch, "AWS/EC2", start, end)
			if !reflect.DeepEqual(got, result) || calls != 1 {
				t.Fatalf("API result/call count changed: %+v, %d", got, calls)
			}
			labels := []string{"123456789012", "us-east-1", "AWS/EC2", "CPUUtilization", "commerce", "payments", "processor", "metric", outcome}
			if value := testutil.ToFloat64(promutil.CloudwatchGetMetricDataOwnerQueryObjectsCounter.WithLabelValues(labels...)); value != 1 {
				t.Fatalf("query objects = %v", value)
			}
			if value := testutil.ToFloat64(promutil.CloudwatchGetMetricDataOwnerEstimatedUnitsCounter.WithLabelValues(labels...)); value != 1 {
				t.Fatalf("estimated units = %v", value)
			}
		})
	}
}

func TestMeteredClientConcurrentBatches(t *testing.T) {
	promutil.CloudwatchGetMetricDataOwnerQueryObjectsCounter.Reset()
	promutil.CloudwatchGetMetricDataOwnerEstimatedUnitsCounter.Reset()
	inner := meteringTestClient{call: func(context.Context, []*model.CloudwatchData, string, time.Time, time.Time) []cloudwatch.MetricDataResult {
		return []cloudwatch.MetricDataResult{{ID: "id_0"}}
	}}
	client := NewMeteredClient(inner, "123456789012", "us-east-1")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.GetMetricData(context.Background(), []*model.CloudwatchData{meteringData("Average")}, "AWS/EC2", time.Time{}, time.Time{})
		}()
	}
	wg.Wait()
	labels := []string{"123456789012", "us-east-1", "AWS/EC2", "CPUUtilization", "commerce", "payments", "processor", "metric", "returned"}
	if value := testutil.ToFloat64(promutil.CloudwatchGetMetricDataOwnerEstimatedUnitsCounter.WithLabelValues(labels...)); value != 20 {
		t.Fatalf("separate logical calls should not be deduplicated: %v", value)
	}
}
