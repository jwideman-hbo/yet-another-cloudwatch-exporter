package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi/resourcegroupstaggingapiiface"
	"github.com/aws/aws-sdk-go/service/shield"
	"github.com/aws/aws-sdk-go/service/shield/shieldiface"
	"github.com/grafana/regexp"
	"github.com/stretchr/testify/require"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type excludeTaggingAPI struct {
	resourcegroupstaggingapiiface.ResourceGroupsTaggingAPIAPI
}

func (excludeTaggingAPI) GetResourcesPagesWithContext(_ aws.Context, _ *resourcegroupstaggingapi.GetResourcesInput, fn func(*resourcegroupstaggingapi.GetResourcesOutput, bool) bool, _ ...request.Option) error {
	fn(&resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: []*resourcegroupstaggingapi.ResourceTagMapping{
		{ResourceARN: aws.String("arn:aws:ec2:us-east-1:123456789012:instance/i-keep"), Tags: []*resourcegroupstaggingapi.Tag{{Key: aws.String("team"), Value: aws.String("keep")}}},
		{ResourceARN: aws.String("arn:aws:ec2:us-east-1:123456789012:instance/i-drop"), Tags: []*resourcegroupstaggingapi.Tag{{Key: aws.String("team"), Value: aws.String("drop")}}},
	}}, true)
	return nil
}

func TestExcludeTagsGetResources(t *testing.T) {
	get := func(exclude []model.SearchTag) ([]*model.TaggedResource, error) {
		return (client{logger: logging.NewNopLogger(), taggingAPI: excludeTaggingAPI{}}).GetResources(context.Background(), model.DiscoveryJob{
			Type: "AWS/EC2", SearchTags: []model.SearchTag{{Key: "team", Value: regexp.MustCompile(".*")}}, ExcludeTags: exclude,
		}, "us-east-1")
	}

	resources, err := get([]model.SearchTag{{Key: "team", Value: regexp.MustCompile("^drop$")}})
	require.NoError(t, err)
	require.Len(t, resources, 1)
	require.Equal(t, "arn:aws:ec2:us-east-1:123456789012:instance/i-keep", resources[0].ARN)

	resources, err = get([]model.SearchTag{{Key: "missing", Value: regexp.MustCompile(".*")}})
	require.NoError(t, err)
	require.Len(t, resources, 2)

	resources, err = get([]model.SearchTag{{Key: "team", Value: regexp.MustCompile(".*")}})
	require.Nil(t, resources)
	require.ErrorIs(t, err, tagging.ErrExpectedToFindResources)
}

type excludeShieldAPI struct {
	shieldiface.ShieldAPI
	tags          []*shield.Tag
	tagErr        error
	tagCalls      int
	lastTaggedARN string
}

func (s *excludeShieldAPI) ListProtectionsPagesWithContext(_ aws.Context, _ *shield.ListProtectionsInput, fn func(*shield.ListProtectionsOutput, bool) bool, _ ...request.Option) error {
	fn(&shield.ListProtectionsOutput{Protections: []*shield.Protection{{
		ProtectionArn: aws.String("arn:aws:shield::123456789012:protection/abc"),
		ResourceArn:   aws.String("arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/test/abc"),
	}}}, true)
	return nil
}

func (s *excludeShieldAPI) ListTagsForResourceWithContext(_ aws.Context, input *shield.ListTagsForResourceInput, _ ...request.Option) (*shield.ListTagsForResourceOutput, error) {
	s.tagCalls++
	s.lastTaggedARN = aws.StringValue(input.ResourceARN)
	if s.tagErr != nil {
		return nil, s.tagErr
	}
	return &shield.ListTagsForResourceOutput{Tags: s.tags}, nil
}

func TestShieldProtectionTagsFilterSyntheticResources(t *testing.T) {
	for _, tc := range []struct {
		name         string
		search       []model.SearchTag
		exclude      []model.SearchTag
		wantIncluded bool
	}{
		{name: "no filters retain default without tag lookup", wantIncluded: true},
		{name: "search matches real protection tags", search: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("^drop$")}}, wantIncluded: true},
		{name: "exclusion overrides matching search", search: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("^drop$")}}, exclude: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("^drop$")}}},
		{name: "missing exclusion tag does not match", exclude: []model.SearchTag{{Key: "missing", Value: regexp.MustCompile(".*")}}, wantIncluded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &excludeShieldAPI{tags: []*shield.Tag{{Key: aws.String("team"), Value: aws.String("drop")}}}
			results, err := ServiceFilters["AWS/DDoSProtection"].ResourceFunc(context.Background(), client{shieldAPI: api}, model.DiscoveryJob{
				Type: "AWS/DDoSProtection", SearchTags: tc.search, ExcludeTags: tc.exclude,
			}, "us-east-1")
			require.NoError(t, err)
			if tc.wantIncluded {
				require.Len(t, results, 1)
				require.Equal(t, []model.Tag{{Key: "ProtectionArn", Value: "arn:aws:shield::123456789012:protection/abc"}}, results[0].Tags)
			} else {
				require.Empty(t, results)
			}
			if len(tc.search)+len(tc.exclude) > 0 {
				require.Equal(t, 1, api.tagCalls)
				require.Equal(t, "arn:aws:shield::123456789012:protection/abc", api.lastTaggedARN)
			} else {
				require.Zero(t, api.tagCalls)
			}
		})
	}

	api := &excludeShieldAPI{tagErr: errors.New("access denied")}
	results, err := ServiceFilters["AWS/DDoSProtection"].ResourceFunc(context.Background(), client{shieldAPI: api}, model.DiscoveryJob{
		Type: "AWS/DDoSProtection", ExcludeTags: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("drop")}},
	}, "us-east-1")
	require.Nil(t, results)
	require.ErrorContains(t, err, "access denied")
}
