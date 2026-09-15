# Resource-owner GetMetricData metering

This opt-in instrumentation supports CEA-114135 analysis. It changes neither collection configuration nor exported AWS metric labels. Enable it with `--enable-feature=owner-metering`, alongside existing feature flags, only after a lower-environment pilot is approved.

## Signals

- `yace_cloudwatch_getmetricdata_owner_query_objects_total`: query objects submitted through each logical GetMetricData batch, including metric-math expressions.
- `yace_cloudwatch_getmetricdata_owner_estimated_metric_requests_total`: estimated metric-request units for direct MetricStat entries. Each group of up to five statistic objects for one metric identity and period in one logical batch contributes one unit. Different batches and polling cycles are counted separately.

Both counters carry `target_account_id`, `target_region`, `cloudwatch_namespace`, `cloudwatch_metric_name`, `owner_business_service`, `owner_service`, `owner_component`, `query_kind`, and `outcome`. Target account is resolved through the existing account client, not inferred from the exporter's hosting account. It still needs to be reconciled to the account actually billed in CUR for the deployed cross-account access model.

Only resource OMD tags on the request are used. Export the three OMD tags with `exportedTagsOnMetrics` for discovery jobs. Missing tag fields are `_unallocated`; conflicting owners for the same metric identity become wholly unallocated. Supported custom namespaces resolve request-only owner tags as described below; other custom namespace metrics without associated tags remain unallocated. Exporter/pod OMD must never be used as a customer fallback. Tags describe resource owners, not people querying the metrics.

These are aggregate owner/metric-family counters, not per-resource counters. They do not export ARNs, CloudWatch dimension values, query IDs, or expression text. Series cardinality still grows with owner tuples, metric names, targets, and outcome, so measure it before expanding deployment. Counter label combinations persist for the exporter process lifetime.

## Custom-namespace owner resolution

With `owner-metering` enabled, these custom namespaces resolve ownership before requests reach the batch processor:

| Metric namespace | Raw CloudWatch dimension | Resource lookup |
| --- | --- | --- |
| `AmazonMWAA` | `Environment` | `airflow` resources, exact `environment/<name>` ARN |
| `AWS/MWAA` | `Environment` | Same MWAA environment lookup |
| `AWS/KinesisAnalytics` | `Application` | `kinesisanalytics:application` resources, exact `application/<name>` ARN |

These dimensions appear as `dimension_Environment` and `dimension_Application` in exported Prometheus metrics. Resolution uses the existing tagging client, scoped to the same assumed role/account and region as collection. It performs one paginated resource lookup per eligible custom job invocation, not one call per statistic or resource. There is a ten-second lookup deadline; no stale cross-poll ownership cache is used. Confirm the runtime role's `tag:GetResources` permissions and pagination latency in the dev pilot. The existing tagging client supports both SDK paths; no new AWS SDK dependency or service-specific API is added.

The resolver requires exact account, region, ARN service/type and resource-name matches. It copies only the three OMD ownership fields. Partial tags remain partial; ambiguous resource results, conflicting dimensions, unmatched resources, lookup failures/timeouts and expressions remain unallocated. It does not infer owners from DAG filenames, account names, workload names, or prefixes. Cross-namespace metric-math entries are not assigned the job namespace's resource owner.

Resolved tags are stored in `CloudwatchData.OwnerTags`, separately from exported `Tags`. The request counters use these tags; the existing `aws_*` metric labels, names and values are unchanged. This deliberately avoids changing ServiceMonitor relabeling or workspace routing. Consequently the raw-series coverage dashboard can still report missing tags for these metrics even while the new request counters have owner attribution. Populating resource OMD on the ordinary exported series is a separate behavioral change and is not included here.

If enrichment fails, GMD collection continues and its requests are counted as unallocated. Unsupported namespaces perform no additional lookup. This does not repair resources genuinely missing `omd_component`, Athena tag-export configuration, unmatched discovery metrics or account-wide/shared metrics. It does not claim to close the entire observed metadata gap.

## Billing limitations

AWS describes the five-statistics grouping at https://aws.amazon.com/cloudwatch/pricing/ under "Retrieving Classic Metrics with Get Services". The estimate counts objects conservatively, including repeated statistics, and does not combine different periods. Reconcile these cases with actual CUR usage before using the estimate for chargeback.

This is not an AWS billing meter. It counts logical batches before downstream metric filtering, including requests that return no datapoint. `outcome="returned"` means the client returned a non-nil result, not that every metric had data or every response status succeeded. `failed_or_empty` means the client returned nil. Neither outcome proves whether AWS charged the request. Internal SDK retries and pagination are not counted again. GetMetricStatistics/static jobs are outside this GMD scope.

Expressions (including SEARCH, Metrics Insights, and DB_PERF_INSIGHTS) are exposed as query objects with `query_kind="expression"` but have no estimated-unit series. Their costs must remain unresolved rather than be priced as one ordinary metric. Base MetricStat entries in expression requests are still counted. Use the AWS SDK v2 path to retain the existing fork's metric-math behavior; the SDK v1 implementation does not implement those expressions.

No dollar conversion is built into the exporter. For each complete billed account/region/period, compare estimated units to CUR `CW:GMD-Metrics` usage amount and net cost. A scoped CUR unit rate can produce an explicitly estimated owner cost; unexplained differences stay unallocated. Do not force all CUR GetMetricData charges onto YACE: dashboards and other clients can incur charges in the same account. Do not normalize away missing telemetry, outages, or other callers by distributing their costs among the observed owners.

When the same exporter counter is stored in multiple workspaces or scraped by HA replicas, query one authoritative copy. Actual separate YACE pollers are separate CloudWatch activity and must not be deduplicated as scrape replicas.

## Pilot and rollout

1. Build an immutable image from the reviewed v0.60 fork commit. Do not change v0.58 or stock exporters to the fork as part of this pilot.
2. Run one existing fork workload in dev with `owner-metering` added to its current feature flags. Keep the current image available for rollback.
3. Confirm identical AWS metric output and request configuration, stable resource usage, bounded counter cardinality, and correct owner tags/target account/region.
4. Reconcile the owner query-object sum with the existing query-object counter over the same period. Validate estimated units against a complete CUR period, including multi-statistic and no-data examples.
5. Review partial/unallocated owners, expression requests, failed batches, scrape gaps and duplicate collection. Do not represent missing counters as zero usage.
6. Promote independently to int, stage, and prod only after lower-environment validation and required approvals. Image pin and flag changes belong in separate environment PRs; no image is built or deployed by this source PR.

Rollback is removing `owner-metering` and/or restoring the previous image. No existing metric names or job definitions need to change.
