package v2

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/shield"
	"github.com/grafana/regexp"
	"github.com/stretchr/testify/require"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type tagRoundTrip func(*http.Request) (*http.Response, error)

func (fn tagRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func tagTestClient(fn tagRoundTrip) *http.Client {
	return &http.Client{Transport: fn}
}

func tagResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/x-amz-json-1.1"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestExcludeTagsGetResources(t *testing.T) {
	transport := tagRoundTrip(func(request *http.Request) (*http.Response, error) {
		require.Contains(t, request.Header.Get("X-Amz-Target"), "GetResources")
		return tagResponse(`{"ResourceTagMappingList":[{"ResourceARN":"arn:aws:ec2:us-east-1:123456789012:instance/i-keep","Tags":[{"Key":"team","Value":"keep"}]},{"ResourceARN":"arn:aws:ec2:us-east-1:123456789012:instance/i-drop","Tags":[{"Key":"team","Value":"drop"}]}]}`), nil
	})
	api := resourcegroupstaggingapi.New(resourcegroupstaggingapi.Options{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: tagTestClient(transport),
	})
	get := func(exclude []model.SearchTag) ([]*model.TaggedResource, error) {
		return (client{logger: logging.NewNopLogger(), taggingAPI: api}).GetResources(context.Background(), model.DiscoveryJob{
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

func TestShieldProtectionTagsFilterSyntheticResources(t *testing.T) {
	for _, tc := range []struct {
		name         string
		search       []model.SearchTag
		exclude      []model.SearchTag
		wantIncluded bool
		lookupFails  bool
	}{
		{name: "no filters retain default without tag lookup", wantIncluded: true},
		{name: "search matches real protection tags", search: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("^drop$")}}, wantIncluded: true},
		{name: "exclusion overrides matching search", search: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("^drop$")}}, exclude: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("^drop$")}}},
		{name: "missing exclusion tag does not match", exclude: []model.SearchTag{{Key: "missing", Value: regexp.MustCompile(".*")}}, wantIncluded: true},
		{name: "tag lookup failure prevents unfiltered output", exclude: []model.SearchTag{{Key: "team", Value: regexp.MustCompile("drop")}}, lookupFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tagCalls := 0
			transport := tagRoundTrip(func(request *http.Request) (*http.Response, error) {
				switch {
				case strings.Contains(request.Header.Get("X-Amz-Target"), "ListProtections"):
					return tagResponse(`{"Protections":[{"ProtectionArn":"arn:aws:shield::123456789012:protection/abc","ResourceArn":"arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/test/abc"}]}`), nil
				case strings.Contains(request.Header.Get("X-Amz-Target"), "ListTagsForResource"):
					tagCalls++
					payload, err := io.ReadAll(request.Body)
					require.NoError(t, err)
					require.Contains(t, string(payload), `"ResourceARN":"arn:aws:shield::123456789012:protection/abc"`)
					if tc.lookupFails {
						return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"__type":"AccessDeniedException","message":"denied"}`))}, nil
					}
					return tagResponse(`{"Tags":[{"Key":"team","Value":"drop"}]}`), nil
				default:
					t.Fatalf("unexpected Shield operation: %s", request.Header.Get("X-Amz-Target"))
					return nil, nil
				}
			})
			api := shield.New(shield.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: tagTestClient(transport)})
			results, err := ServiceFilters["AWS/DDoSProtection"].ResourceFunc(context.Background(), client{shieldAPI: api}, model.DiscoveryJob{
				Type: "AWS/DDoSProtection", SearchTags: tc.search, ExcludeTags: tc.exclude,
			}, "us-east-1")
			if tc.lookupFails {
				require.ErrorContains(t, err, "ListTagsForResource")
				require.Nil(t, results)
			} else {
				require.NoError(t, err)
				if tc.wantIncluded {
					require.Len(t, results, 1)
					require.Equal(t, []model.Tag{{Key: "ProtectionArn", Value: "arn:aws:shield::123456789012:protection/abc"}}, results[0].Tags)
				} else {
					require.Empty(t, results)
				}
			}
			if len(tc.search)+len(tc.exclude) > 0 {
				require.Equal(t, 1, tagCalls)
			} else {
				require.Zero(t, tagCalls)
			}
		})
	}
}
