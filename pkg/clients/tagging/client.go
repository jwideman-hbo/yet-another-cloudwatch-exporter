package tagging

import (
	"context"
	"errors"

	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
)

type Client interface {
	GetResources(ctx context.Context, job model.DiscoveryJob, region string) ([]*model.TaggedResource, error)
}

type OwnerResourceClient interface {
	GetResourcesForOwner(ctx context.Context, resourceTypes []string, region string) ([]*model.TaggedResource, error)
}

var ErrExpectedToFindResources = errors.New("expected to discover resources but none were found")

type limitedConcurrencyClient struct {
	client Client
	sem    chan struct{}
}

func NewLimitedConcurrencyClient(client Client, maxConcurrency int) Client {
	return &limitedConcurrencyClient{
		client: client,
		sem:    make(chan struct{}, maxConcurrency),
	}
}

func (c limitedConcurrencyClient) GetResources(ctx context.Context, job model.DiscoveryJob, region string) ([]*model.TaggedResource, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.client.GetResources(ctx, job, region)
}

func (c limitedConcurrencyClient) GetResourcesForOwner(ctx context.Context, resourceTypes []string, region string) ([]*model.TaggedResource, error) {
	client, ok := c.client.(OwnerResourceClient)
	if !ok {
		return nil, errors.New("tagging client does not support owner resource lookup")
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return client.GetResourcesForOwner(ctx, resourceTypes, region)
}
