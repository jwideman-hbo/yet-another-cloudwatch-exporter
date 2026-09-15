package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi/resourcegroupstaggingapiiface"
)

type ownerAPI struct {
	resourcegroupstaggingapiiface.ResourceGroupsTaggingAPIAPI
	call func(*resourcegroupstaggingapi.GetResourcesInput) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

func (a ownerAPI) GetResourcesWithContext(_ aws.Context, input *resourcegroupstaggingapi.GetResourcesInput, _ ...request.Option) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	return a.call(input)
}

func TestOwnerResourcePages(t *testing.T) {
	for _, mode := range []string{"success", "second-page-error", "repeated-token"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			api := ownerAPI{call: func(input *resourcegroupstaggingapi.GetResourcesInput) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
				calls++
				if len(input.ResourceTypeFilters) != 1 || *input.ResourceTypeFilters[0] != "dax" || *input.ResourcesPerPage != 100 {
					t.Fatal("unscoped resource lookup")
				}
				if calls > 1 && aws.StringValue(input.PaginationToken) != "next" {
					t.Fatal("pagination token not passed")
				}
				if mode == "second-page-error" && calls == 2 {
					return nil, errors.New("access denied")
				}
				token := ""
				if calls == 1 || mode == "repeated-token" {
					token = "next"
				}
				return &resourcegroupstaggingapi.GetResourcesOutput{
					PaginationToken: aws.String(token),
					ResourceTagMappingList: []*resourcegroupstaggingapi.ResourceTagMapping{{
						ResourceARN: aws.String("arn:aws:dax:us-east-1:123456789012:cache/example"),
						Tags:        []*resourcegroupstaggingapi.Tag{{Key: aws.String("omd_service"), Value: aws.String("payments")}},
					}},
				}, nil
			}}
			c := client{taggingAPI: api}
			resources, err := c.GetResourcesForOwner(context.Background(), []string{"dax"}, "us-east-1")
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			if mode != "success" {
				if err == nil || resources != nil {
					t.Fatal("partial resource inventory must be discarded")
				}
			} else if err != nil || len(resources) != 2 || resources[0].Tags[0].Value != "payments" {
				t.Fatalf("bad page result: %+v, %v", resources, err)
			}
		})
	}
}

func TestOwnerLookupRejectsEmptyResourceTypes(t *testing.T) {
	c := client{}
	_, err := c.GetResourcesForOwner(context.Background(), nil, "us-east-1")
	if err == nil {
		t.Fatal("empty resource types must not trigger account-wide inventory")
	}
}
