package v2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
)

func TestOwnerResourcePages(t *testing.T) {
	for _, mode := range []string{"success", "second-page-error", "repeated-token"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var input struct {
					ResourceTypeFilters []string
					PaginationToken     string
					ResourcesPerPage    int
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				if len(input.ResourceTypeFilters) != 1 || input.ResourceTypeFilters[0] != "dax" || input.ResourcesPerPage != 100 {
					t.Error("unscoped resource lookup")
				}
				if calls > 1 && input.PaginationToken != "next" {
					t.Error("pagination token not passed")
				}
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")
				if mode == "second-page-error" && calls == 2 {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"denied"}`))
					return
				}
				token := ""
				if calls == 1 || mode == "repeated-token" {
					token = "next"
				}
				if err := json.NewEncoder(w).Encode(map[string]any{
					"PaginationToken": token,
					"ResourceTagMappingList": []any{map[string]any{
						"ResourceARN": "arn:aws:dax:us-east-1:123456789012:cache/example",
						"Tags":        []any{map[string]string{"Key": "omd_service", "Value": "payments"}},
					}},
				}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			api := resourcegroupstaggingapi.NewFromConfig(aws.Config{
				Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
				RetryMaxAttempts: 1,
			}, func(options *resourcegroupstaggingapi.Options) { options.BaseEndpoint = aws.String(server.URL) })
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
