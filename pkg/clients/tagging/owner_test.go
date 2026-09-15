package tagging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type ownerReaderStub struct{}

func (ownerReaderStub) GetResources(context.Context, model.DiscoveryJob, string) ([]*model.TaggedResource, error) {
	return nil, errors.New("unexpected lookup")
}

func (ownerReaderStub) GetResourcesForOwner(context.Context, []string, string) ([]*model.TaggedResource, error) {
	return nil, errors.New("unexpected lookup")
}

func TestLookupCancellationWhileWaitingForConcurrency(t *testing.T) {
	for _, owner := range []bool{false, true} {
		client := limitedConcurrencyClient{client: ownerReaderStub{}, sem: make(chan struct{}, 1)}
		client.sem <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)
		go func() {
			if owner {
				_, err := client.GetResourcesForOwner(ctx, []string{"dax"}, "us-east-1")
				done <- err
			} else {
				_, err := client.GetResources(ctx, model.DiscoveryJob{}, "us-east-1")
				done <- err
			}
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
		case <-time.After(time.Second):
			<-client.sem
			t.Fatal("lookup deadline must include semaphore wait")
		}
	}
}
