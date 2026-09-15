package job

import (
	"context"
	"reflect"
	"testing"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

func TestSweptCustomNamespaceMappings(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		dimension string
		value     string
		resource  string
	}{
		{"AWS/DAX", "ClusterId", "example", "arn:aws:dax:us-east-1:123456789012:cache/example"},
		{"AWS/EKS", "ClusterName", "example", "arn:aws:eks:us-east-1:123456789012:cluster/example"},
		{"AWS/OSIS", "PipelineName", "example", "arn:aws:osis:us-east-1:123456789012:pipeline/example"},
		{"CloudWatchSynthetics", "CanaryName", "example", "arn:aws:synthetics:us-east-1:123456789012:canary:example"},
		{"CloudWatchSynthetics/Custom", "CanaryName", "example", "arn:aws:synthetics:us-east-1:123456789012:canary:example"},
		{"AWS/EC2CapacityReservations", "CapacityReservationId", "cr-example", "arn:aws:ec2:us-east-1:123456789012:capacity-reservation/cr-example"},
		{"AWS/Personalize", "CampaignArn", "arn:aws:personalize:us-east-1:123456789012:campaign/example", "arn:aws:personalize:us-east-1:123456789012:campaign/example"},
		{"AWS/Personalize", "RecommenderArn", "arn:aws:personalize:us-east-1:123456789012:recommender/example", "arn:aws:personalize:us-east-1:123456789012:recommender/example"},
		{"AWS/Glue", "JobName", "example", "arn:aws:glue:us-east-1:123456789012:job/example"},
		{"AWS/SageMaker", "EndpointName", "example", "arn:aws:sagemaker:us-east-1:123456789012:endpoint/example"},
		{"/aws/sagemaker/Endpoints", "EndpointName", "example", "arn:aws:sagemaker:us-east-1:123456789012:endpoint/example"},
		{"AWS/Firehose", "DeliveryStreamName", "example", "arn:aws:firehose:us-east-1:123456789012:deliverystream/example"},
		{"AWS/Redshift", "ClusterIdentifier", "example", "arn:aws:redshift:us-east-1:123456789012:cluster:example"},
		{"AWS/WAFV2", "WebACL", "example", "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/example/id"},
		{"AWS/ApiGateway", "ApiId", "example", "arn:aws:apigateway:us-east-1::/apis/example"},
		{"AWS/States", "StateMachineArn", "arn:aws:states:us-east-1:123456789012:stateMachine:example", "arn:aws:states:us-east-1:123456789012:stateMachine:example"},
	} {
		t.Run(tc.namespace+"/"+tc.dimension, func(t *testing.T) {
			data := ownerRequest(tc.namespace, tc.dimension, tc.value)
			client := &ownerTagClient{resources: []*model.TaggedResource{{ARN: tc.resource, Tags: ownerTags()}}}
			ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
			enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), tc.namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
			if !reflect.DeepEqual(data.OwnerTags, ownerTags()) || client.calls != 1 {
				t.Fatalf("owner mapping failed: %+v, calls=%d", data.OwnerTags, client.calls)
			}
		})
	}
}

func TestTimestreamRequiresCompleteTableIdentity(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
	data := ownerRequest("AWS/Timestream", "DatabaseName", "db")
	client := &ownerTagClient{resources: []*model.TaggedResource{
		{ARN: "arn:aws:timestream:us-east-1:123456789012:database/db", Tags: ownerTags()},
		{ARN: "arn:aws:timestream:us-east-1:123456789012:database/db/table/table", Tags: ownerTags()},
	}}
	enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
	if client.calls != 0 || len(data.OwnerTags) != 0 {
		t.Fatal("database-only aggregate must not inherit a table owner")
	}
	data.Dimensions = append(data.Dimensions, model.Dimension{Name: "TableName", Value: "table"})
	enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
	if !reflect.DeepEqual(data.OwnerTags, ownerTags()) {
		t.Fatal("table identity did not resolve")
	}
}

func TestUsageRequiresPrometheusWorkspaceIdentity(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
	client := &ownerTagClient{resources: []*model.TaggedResource{{ARN: "arn:aws:aps:us-east-1:123456789012:workspace/ws-example", Tags: ownerTags()}}}
	data := ownerRequest("AWS/Usage", "ResourceId", "ws-example")
	data.Dimensions = append(data.Dimensions, model.Dimension{Name: "Service", Value: "EC2"})
	enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
	if client.calls != 0 {
		t.Fatal("non-Prometheus usage must not resolve to a workspace")
	}
	data.Dimensions[1].Value = "Prometheus"
	enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
	if !reflect.DeepEqual(data.OwnerTags, ownerTags()) || client.jobType != "AWS/Prometheus" {
		t.Fatal("workspace owner was not resolved")
	}
}

func TestDiscoveryUsesRawTagsWithoutChangingExportedTags(t *testing.T) {
	data := ownerRequest("AWS/Athena", "WorkGroup", "example")
	data.ResourceName = "arn:aws:athena:us-east-1:123456789012:workgroup/example"
	data.Tags = nil
	resources := []*model.TaggedResource{{ARN: data.ResourceName, Tags: ownerTags()}}
	enrichDiscoveryOwners(context.Background(), data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, resources)
	if data.OwnerTags != nil {
		t.Fatal("disabled metering changed discovery ownership")
	}
	ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
	enrichDiscoveryOwners(ctx, data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, resources)
	if !reflect.DeepEqual(data.OwnerTags, ownerTags()) || data.Tags != nil {
		t.Fatal("raw tags must inform metering without changing exported labels")
	}
}

func TestRDSCaseAliasAndConflictingAliases(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
	data := ownerRequest("AWS/RDS", "DbClusterIdentifier", "example")
	data.ResourceName = "global"
	resources := []*model.TaggedResource{{ARN: "arn:aws:rds:us-east-1:123456789012:cluster:example", Tags: ownerTags()}}
	enrichDiscoveryOwners(ctx, data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, resources)
	if !reflect.DeepEqual(data.OwnerTags, ownerTags()) || data.Dimensions[0].Name != "DbClusterIdentifier" || data.ResourceName != "global" {
		t.Fatal("RDS metadata alias must not rewrite exported dimensions or resource name")
	}
	data.Dimensions = append(data.Dimensions, model.Dimension{Name: "DBClusterIdentifier", Value: "other"})
	enrichDiscoveryOwners(ctx, data.Namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, resources)
	if len(data.OwnerTags) != 0 {
		t.Fatal("conflicting aliases must stay unallocated")
	}
}

func TestMostSpecificResourceIdentityWins(t *testing.T) {
	mapping, _ := customNamespaceOwnerMapping("AWS/ApiGateway")
	apiTags := []model.Tag{{Key: "omd_service", Value: "api"}}
	stageTags := ownerTags()
	resources := []*model.TaggedResource{
		{ARN: "arn:aws:apigateway:us-east-1::/apis/example", Tags: apiTags},
		{ARN: "arn:aws:apigateway:us-east-1::/apis/example/stages/prod", Tags: stageTags},
	}
	index := newOwnerResourceIndex(mapping, resources, "123456789012", "us-east-1")
	data := ownerRequest("AWS/ApiGateway", "ApiId", "example")
	data.Dimensions = append(data.Dimensions, model.Dimension{Name: "Stage", Value: "prod"})
	if !reflect.DeepEqual(index.ownerTags(data, data.Namespace), stageTags) {
		t.Fatal("less-specific API resource made an exact stage identity ambiguous")
	}
}

func TestAmbiguousResourceNamesStayUnallocated(t *testing.T) {
	mapping, _ := customNamespaceOwnerMapping("AWS/WAFV2")
	resources := []*model.TaggedResource{
		{ARN: "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/example/id-1", Tags: ownerTags()},
		{ARN: "arn:aws:wafv2:us-east-1:123456789012:global/webacl/example/id-2", Tags: ownerTags()},
	}
	index := newOwnerResourceIndex(mapping, resources, "123456789012", "us-east-1")
	data := ownerRequest("AWS/WAFV2", "WebACL", "example")
	if len(index.ownerTags(data, data.Namespace)) != 0 {
		t.Fatal("ambiguous name-only match was attributed")
	}
}

func TestAggregateAndApplicationOnlyNamespacesRemainUnallocated(t *testing.T) {
	ctx := config.CtxWithFlags(context.Background(), ownerMeteringFlags{})
	for _, namespace := range []string{"AWS/DAX", "AWS/EC2CapacityReservations", "AWS/States", "AWS/Timestream", "AWS/Bedrock", "AWS/EC2/API", "Blackbox/ErrorReceiver", "mux-qoe-observability", "CWAgent"} {
		t.Run(namespace, func(t *testing.T) {
			data := ownerRequest(namespace, "Operation", "Read")
			client := &ownerTagClient{}
			enrichCustomNamespaceOwners(ctx, logging.NewNopLogger(), namespace, "123456789012", "us-east-1", []*model.CloudwatchData{data}, client)
			if client.calls != 0 || len(data.OwnerTags) != 0 {
				t.Fatal("aggregate/custom metric received guessed ownership")
			}
		})
	}
}
