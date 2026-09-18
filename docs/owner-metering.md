# Resource-owner GetMetricData metering

This opt-in instrumentation supports CEA-114135 analysis. It changes neither collection configuration nor exported AWS metric labels. Enable it with `--enable-feature=owner-metering`, alongside existing feature flags, only after a lower-environment pilot is approved.

## Signals

- `yace_cloudwatch_getmetricdata_owner_query_objects_total`: query objects submitted through each logical GetMetricData batch after owner policy filtering.
- `yace_cloudwatch_getmetricdata_owner_estimated_metric_requests_total`: estimated submitted metric-request units for direct MetricStat entries. Each group of up to five statistic objects for one metric identity and period in one logical batch contributes one unit. Different batches and polling cycles are counted separately.
- `yace_cloudwatch_getmetricdata_owner_pre_filter_estimated_metric_requests_total`: the same billing-unit estimate before owner policy filtering, using the configured query batch size and the original request order.

For example, requesting `Average` and `Maximum` for one metric identity increments the query-object counter by two but the estimated metric-request counter by one. The query-object signal audits YACE work; the grouped estimate is the signal that approximates `GMD-Metrics` billing.

The submitted counters carry `target_account_id`, `target_region`, `cloudwatch_namespace`, `cloudwatch_metric_name`, `owner_business_service`, `owner_service`, `owner_component`, and `outcome`. The pre-filter counter carries the same labels except `outcome`. Target account is resolved through the existing account client, not inferred from the exporter's hosting account. It still needs to be reconciled to the account actually billed in CUR for the deployed cross-account access model.

Only resource OMD tags on the request are used. Discovery metering reads raw tags from the already-fetched resource inventory, independently of `exportedTagsOnMetrics`; omitting OMD from exported labels no longer hides it from accounting. Missing tag fields are `_unallocated`; conflicting owners for the same metric identity become wholly unallocated. Supported custom namespaces resolve request-only owner tags as described below; other custom namespace metrics without associated tags remain unallocated. Exporter/pod OMD must never be used as a customer fallback. Tags describe resource owners, not people querying the metrics.

These are aggregate owner/metric-family counters, not per-resource counters. They do not export ARNs, CloudWatch dimension values, or query IDs. Series cardinality still grows with owner tuples, metric names, targets, and outcome, so measure it before expanding deployment. Counter label combinations persist for the exporter process lifetime.

## Resource-owner resolution

The [coverage sweep](owner-coverage-sweep.md) covers all configured discovery/custom job types and records the unresolved classes. With `owner-metering` enabled, discovery jobs reuse their existing resource inventory without additional tag calls. Exact associated ARNs use raw OMD tags, including tags excluded from exported labels. Previously unmatched requests can use the existing service registry's named resource-dimension patterns; the RDS `DbClusterIdentifier`/`DBClusterIdentifier` and instance variants are reconciled only for accounting. Conflicting variants remain unresolved.

Custom jobs use the same service registry where it supplies a resource association, plus these explicit mappings:

| Metric namespace | Required raw CloudWatch identity | Lookup |
| --- | --- | --- |
| `AmazonMWAA`, `AWS/MWAA` | `Environment` | MWAA environment |
| `AWS/KinesisAnalytics` | `Application` | Flink application |
| `AWS/DAX` | `ClusterId` | DAX cache |
| `AWS/EKS` | `ClusterName` | EKS cluster |
| `AWS/OSIS` | `PipelineName` | OpenSearch Ingestion pipeline |
| `CloudWatchSynthetics`, `CloudWatchSynthetics/Custom` | `CanaryName` | Synthetics canary |
| `AWS/EC2CapacityReservations` | `CapacityReservationId` | EC2 capacity reservation, not an instance-type aggregate |
| `AWS/Timestream` | `DatabaseName` AND `TableName` | Timestream table, no database-owner fallback |
| `AWS/Personalize` | `CampaignArn` or `RecommenderArn` | Personalize resource |
| `AWS/Glue` | `JobName` | Glue job via the existing `Glue` service mapping |
| `AWS/Usage` | `Service=Prometheus` AND `ResourceId=ws-...` | AMP workspace only; other usage stays unallocated |

Existing registry mappings also support identifiable API Gateway, Firehose, Redshift, SageMaker endpoint, Step Functions and WAFv2 requests. Certificate ARN, AMP workspace and DMS replication-instance identifiers are supported for previously unmatched discovery records. A mapping's existence does not guarantee its resource inventory contains usable OMD: for example, some Shield protected-resource entries only carry protection metadata rather than owner tags.

CloudWatch dimensions appear with a `dimension_` prefix in exported Prometheus metrics. Resolution uses the tagging client scoped to the same assumed role/account and region as collection. Eligible custom jobs perform one paginated inventory lookup per invocation, not one call per statistic or resource. Namespaces absent from the discovery registry use a resource-type-filtered `GetResourcesForOwner` reader in both SDK implementations. It rejects empty filters and discards partial inventories on error or repeated pagination tokens. Existing registry-specific discovery clients are reused for registered types.

There is a ten-second custom lookup deadline, including waiting for tagging-client concurrency. No stale cross-poll ownership cache is used. Validate permissions, latency, cardinality and polling behavior in the dev pilot. There is no new AWS SDK dependency; new owner-only inventories use the Resource Groups Tagging API already used by YACE.

The resolver validates account/region scope and matches exact resource identities through named dimension patterns. API Gateway ARNs legitimately omit account IDs; S3 ARNs omit account/region; CloudFront and Shield may omit region. Those are accepted only from the account-scoped lookup, not arbitrary cross-account inventories. Multiple matching resource ARNs, duplicate inventory records, conflicting dimensions, absent identifiers, and lookup failures remain unallocated. Only three OMD ownership fields are copied; no owner is inferred from account names, workload names, DAG filenames, or prefixes.

Resolved tags are stored in `CloudwatchData.OwnerTags`, separately from exported `Tags`. The request counters use these tags; the existing `aws_*` metric labels, names and values are unchanged. This deliberately avoids changing ServiceMonitor relabeling or workspace routing. Consequently the raw-series coverage dashboard can still report missing tags for these metrics even while the new request counters have owner attribution. Populating resource OMD on the ordinary exported series is a separate behavioral change and is not included here.

If enrichment fails, GMD collection continues and its requests are counted as unallocated. Unsupported namespaces and identifier-free aggregate requests perform no additional lookup. This does not repair resources genuinely missing `omd_component`, legacy-only tags, misspelled identities, raw Athena label-export configuration, or shared metrics without resource identity. It does not claim to close the entire observed metadata gap.

## Owner metric filtering

Owner policies can filter direct metric queries at the deployment, discovery/custom-namespace job, and metric levels. Actual filtering requires both flags:

```text
--enable-feature=owner-metering,owner-metric-filtering
```

Existing configurations need no policy. An omitted policy imposes no additional restriction, preserving collection for all owners. Existing metrics can add exact opt-outs with `allOwners`:

```yaml
ownerPolicy:
  mode: allOwners
  except:
    - businessService: media-supply-chain

discovery:
  jobs:
    - type: AWS/EC2
      ownerPolicy:
        mode: allOwners
        except:
          - businessService: playback-services
      metrics:
        - name: CPUUtilization
          statistics:
            - Average
          ownerPolicy:
            mode: allOwners
            except:
              - businessService: commerce
                service: checkout-platform
                component: payment-api
```

Specialized or newly requested metrics can use `selectedOwners` so only explicit consumers receive them:

```yaml
ownerPolicy:
  mode: selectedOwners
  owners:
    - businessService: observability
      service: metrics-as-a-service
    - businessService: commerce
      service: checkout-platform
```

Selectors use exact strings, not regular expressions, and preserve the OMD hierarchy. `businessService` is required. Adding `service` narrows the selector to that service and its components; adding `component` requires both ancestors and selects the complete owner tuple. A query must pass every configured policy, so a metric policy can narrow a job policy but cannot restore an owner denied by the job or deployment. Choose mode based on desired behavior for future owners, not only which representation is shorter: `allOwners` automatically includes them, while `selectedOwners` does not.

Filtering occurs after owner enrichment and before GMD batching. It applies to discovery and supported custom-namespace jobs. Static jobs are unchanged. Any request with a missing, empty, `_unknown`, `_unallocated`, or conflicting canonical OMD field fails open and remains collected. Policies never infer ownership from metric labels, resource names, legacy tags, job names, accounts, or exporter metadata.

`yace_cloudwatch_getmetricdata_owner_excluded_query_objects_total` records each filtered direct query object by target, namespace, raw CloudWatch metric name and resolved owner tuple. This is an audit count, not a cost-savings count: excluding five statistics for one metric identity can remove only one billed metric-request unit. Submitted owner counters do not include excluded objects. Existing exported AWS series backed by a filtered query stop updating and eventually become stale; no metric configuration or historical AMP data is deleted.

Estimate avoided billed units over a complete interval by subtracting submitted units, summed across `outcome`, from pre-filter units with the same remaining labels. Perform the authoritative comparison after summing all owners and metric names for a target account and region because removing entries can move later identities across 500-query batch boundaries. Per-owner or per-metric differences can include those packing effects.

```promql
sum without (cloudwatch_metric_name, owner_business_service, owner_service, owner_component) (
  increase(yace_cloudwatch_getmetricdata_owner_pre_filter_estimated_metric_requests_total[1d])
)
-
sum without (cloudwatch_metric_name, owner_business_service, owner_service, owner_component, outcome) (
  increase(yace_cloudwatch_getmetricdata_owner_estimated_metric_requests_total[1d])
)
```

Removing the `owner-metric-filtering` flag disables every policy without removing configuration. Removing `owner-metering` also prevents filtering because trusted resource-owner metadata is then unavailable. YACE cannot identify whether a metric was newly added; repositories that require new metrics to start in `selectedOwners` mode must enforce that transition in CI.

## Billing validation and limitations

AWS describes the five-statistics grouping at https://aws.amazon.com/cloudwatch/pricing/ under "Retrieving Classic Metrics with Get Services". The model was also validated against an existing five-minute Global Accelerator collector: 418 distinct metric identities produced 460 statistic query objects per poll and exactly 5,016 billed `GMD-Metrics` per normal hour. The observed value matched `418 identities * 12 polls`, not `460 objects * 12 polls`.

The estimate counts objects conservatively, including repeated statistics, and does not combine different periods. Reconcile these cases with actual CUR usage before using the estimate for chargeback.

This is not an AWS billing meter. It counts logical batches before downstream metric filtering, including requests that return no datapoint. `outcome="returned"` means the client returned a non-nil result, not that every metric had data or every response status succeeded. `failed_or_empty` means the client returned nil. Neither outcome proves whether AWS charged the request. Internal SDK retries and pagination are not counted again. GetMetricStatistics/static jobs are outside this GMD scope.

No dollar conversion is built into the exporter. For each complete billed account/region/period, compare estimated units to CUR `CW:GMD-Metrics` usage amount and net cost. A scoped CUR unit rate can produce an explicitly estimated owner cost; unexplained differences stay unallocated. Do not force all CUR GetMetricData charges onto YACE: dashboards and other clients can incur charges in the same account. Do not normalize away missing telemetry, outages, or other callers by distributing their costs among the observed owners.

When the same exporter counter is stored in multiple workspaces or scraped by HA replicas, query one authoritative copy. Actual separate YACE pollers are separate CloudWatch activity and must not be deduplicated as scrape replicas.

## Cost-reduction workflow

Prioritize estimated billing units rather than query-object counts. For each owner, report the target account and region, namespace, metric name, estimated units per poll, effective polling interval, estimated units per day, and downstream usage evidence. A metric attached to many resource dimension sets or polled every minute costs more than an equally unused metric with a small footprint or five-minute cadence.

Before filtering a metric, search dashboards, alert rules, recording rules, autoscaling dependencies, runbooks, and available query-usage telemetry, then obtain the owning team's confirmation. Use the narrowest exact owner hierarchy that reflects the decision. A business-service selector intentionally includes all descendant services and components; component selectors require the complete tuple.

Do not use statistic-level removal as the primary savings mechanism. Up to five statistics for one identity in one request form one billed metric request. The material levers are removing an entire unused metric identity, eliminating duplicate collectors, and matching the YACE polling interval to the source metric's publication frequency. CloudWatch standard resolution permits one-minute granularity but does not guarantee that every service publishes every metric each minute; for example, basic EC2 monitoring publishes at five-minute resolution. `period` controls CloudWatch aggregation and does not by itself reduce how often YACE calls AWS.

If one deployment mixes one-minute and five-minute publication cadences, changing its global `--scraping-interval` can reduce downstream freshness. Split cadence classes only when the resulting deployments have disjoint collection scopes. Treat request batching changes that preserve identity groups at 500-query boundaries as a separate behavioral change.

## Pilot and rollout

1. Build an immutable image from the reviewed commit based exactly on upstream v0.60.0. Do not change v0.58 exporters as part of this pilot.
2. Run one existing v0.60 workload in dev with `owner-metering` added only after the image-only gate passes. Keep the current image available for rollback.
3. Confirm identical AWS metric output and request configuration, stable resource usage, bounded counter cardinality, and correct owner tags/target account/region.
4. Reconcile the owner query-object sum with the existing query-object counter over the same period. Validate estimated units against a complete CUR period, including multi-statistic and no-data examples.
5. Review partial/unallocated owners, failed batches, scrape gaps, and duplicate collection. Do not represent missing counters as zero usage.
6. Promote independently to int, stage, and prod only after lower-environment validation and required approvals. Image pin and flag changes belong in separate environment PRs; no image is built or deployed by this source PR.

Rollback is removing `owner-metric-filtering`, removing `owner-metering`, and/or restoring the previous image. No existing metric names or job definitions need to change.
