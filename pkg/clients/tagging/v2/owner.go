package v2

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/promutil"
)

func (c client) GetResourcesForOwner(ctx context.Context, resourceTypes []string, region string) ([]*model.TaggedResource, error) {
	if len(resourceTypes) == 0 {
		return nil, errors.New("owner lookup requires resource types")
	}
	input := &resourcegroupstaggingapi.GetResourcesInput{ResourceTypeFilters: resourceTypes, ResourcesPerPage: aws.Int32(100)}
	var resources []*model.TaggedResource
	seen := map[string]bool{}
	for {
		promutil.ResourceGroupTaggingAPICounter.Inc()
		page, err := c.taggingAPI.GetResources(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, record := range page.ResourceTagMappingList {
			if aws.ToString(record.ResourceARN) == "" {
				continue
			}
			resource := &model.TaggedResource{ARN: *record.ResourceARN, Region: region}
			for _, tag := range record.Tags {
				resource.Tags = append(resource.Tags, model.Tag{Key: aws.ToString(tag.Key), Value: aws.ToString(tag.Value)})
			}
			resources = append(resources, resource)
		}
		token := aws.ToString(page.PaginationToken)
		if token == "" {
			return resources, nil
		}
		if seen[token] {
			return nil, errors.New("owner resource lookup repeated a pagination token")
		}
		seen[token] = true
		input.PaginationToken = aws.String(token)
	}
}
