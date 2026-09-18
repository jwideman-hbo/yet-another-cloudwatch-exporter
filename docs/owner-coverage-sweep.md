# YACE owner coverage sweep — CEA-114135

## Scope and reproducibility

This is a point-in-time analysis, not a chargeback ledger or proof that every AWS resource was individually checked.

- Configuration source: internal Flux `origin/main`, commit `51851972b6448e1b8aefd186cb42df77ce2f33d3`.
- All 224 cluster YACE value files were inspected with their matching environment-layer values: 131 enabled configurations, 93 disabled, no YAML parse failures.
- Enabled configuration contains 732 discovery-job definitions and 430 custom-namespace job definitions spanning 40 distinct custom namespaces. No static jobs were found in that inventory.
- Runtime snapshot: 2026-09-15 22:22:32 UTC, preceding 30 minutes. All 161 active AMP workspaces discovered in the five observability accounts' YACE deployment regions were queried successfully. Twenty-three contained matching YACE series.
- Runtime selection: `aws_*` metrics whose `job` begins with `yace`. Workloads using other naming conventions or workspaces outside the inventoried accounts/regions are outside this measurement.
- Counts are series observations per workspace. Copies stored in multiple workspaces are not deduplicated into a unique-resource count. They are neither CloudWatch billable requests nor AMP billed ingestion/storage units.
- Configuration is desired state; runtime data need not have reconciled the same commit yet. AWS tag checks validated representative resources for the identified failure classes, not every resource in every account.

The core runtime query, executed against each workspace rather than a merged datasource, was:

```promql
count by (
  job, family, account_id, region,
  tag_omd_business_service, tag_omd_service, tag_omd_component
) (
  label_replace(
    last_over_time({__name__=~"aws_.*", job=~"yace.*"}[30m]),
    "family", "$1", "__name__", "aws_([^_]+)_.*"
  )
)
```

Classify the returned OMD tuple as complete when all three fields are nonempty and not `_unknown`/`_unallocated`; partial when only some fields are usable; missing when none are usable. A complete tuple is not validated against the OMD catalog. The metric-family prefix is a triage heuristic, not a lossless CloudWatch namespace identifier.

To repeat the configuration audit, enumerate cluster `yace/*-values.yaml` files at a pinned Git ref, merge the matching environment file, check enablement, and parse the embedded config. Record all `discovery.jobs`, `customNamespace` and `static` entries, roles/regions, image version and exported-tag settings. To repeat the runtime audit, discover active AMP workspaces with each explicit environment profile in the deployment regions, then run the query with a common timestamp and retain response/error coverage. Use AWS resource tags to validate ownership; do not infer it from resource-name strings.

Detailed runtime/resource payloads and contact tags were deliberately not included in this fork. The local analysis artifacts are `cea114135-config-sweep.json` and `cea114135-runtime-sweep/{inventory,results,summary}.json`; retain/export these through the internal ticket rather than a public source-code repository.

## Observed coverage before deployment

| Environment | Workspaces queried | With YACE | Series observations | Complete OMD | Partial | Missing all three | Complete % |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| dev | 13 | 2 | 148,011 | 118,777 | 2,284 | 26,950 | 80.25% |
| int | 19 | 3 | 294,801 | 223,717 | 5,386 | 65,698 | 75.89% |
| nonprod | 2 | 1 | 468 | 281 | 7 | 180 | 60.04% |
| prod | 76 | 10 | 676,552 | 594,456 | 1,016 | 81,080 | 87.87% |
| stage | 51 | 7 | 398,491 | 321,204 | 29,370 | 47,917 | 80.61% |

The earlier approximately 90% number was one production workspace, not fleet coverage. The broader audit contains 259,888 incomplete observations out of 1,518,323 workspace-series observations. Neither percentage predicts how much GMD spend can be attributed after rollout.

Largest incomplete metric families across these workspace observations:

| Family | Incomplete observations |
| --- | ---: |
| EBS | 42,309 |
| Kinesis Analytics | 32,448 |
| RDS | 32,023 |
| EC2 | 30,705 |
| SageMaker | 19,211 |
| CWAgent | 16,384 |
| AmazonMWAA | 15,680 |
| DAX | 13,088 |
| OSIS | 10,570 |
| WAFv2 | 9,569 |
| AWS Usage | 9,221 |

## Failure classes and implemented response

### Resource tags exist but are excluded from exported labels

Twenty-nine discovery-job definitions lacked one or more required OMD export fields. This includes all 13 Athena definitions, several EC2/EBS/ES/Kinesis/Lambda/S3/SNS definitions, and resource-less Usage/CWAgent cases. A missing export configuration does not prove the source resource has the tags.

When discovery has actually associated a tagged resource, metering now reads its raw OMD tags independently of the exported-label allowlist. This fixes accounting for cases such as a fully tagged Athena workgroup without rewriting existing AWS metric labels. Missing source tags remain unallocated.

### Resource identity is present under a dimension-name variant

Live RDS cluster metrics were observed with `DbClusterIdentifier`, while the existing discovery matcher expects `DBClusterIdentifier`. Accounting resolves the supported cluster/instance case variants against the existing tagged-resource inventory. Conflicting variants stay unallocated. It does not change public metric dimensions or the original `name="global"` label.

Additional named-identifier associations cover certificates, AMP workspaces and DMS replication instances where the fetched inventory supplies a unique matching resource.

### Custom namespace bypasses tag association

Resource-backed custom namespaces now use the existing discovery registry where applicable, plus explicit mappings for MWAA, DAX, EKS, OSIS, Synthetics, capacity reservations, Personalize and Timestream tables. API Gateway, Firehose, Glue, Flink, Redshift, SageMaker endpoints, Step Functions and WAFv2 use named resource identities. AWS Usage is supported only for `Service=Prometheus` with a workspace `ResourceId`.

Twenty-one of the 40 configured custom namespaces have an identifier-mapping path. This is capability coverage, not a claim that every request in those namespaces is resolvable: tags can be absent, inventories incomplete, identities ambiguous, or metric dimensions aggregate-only. Shield uses existing protected-resource discovery, whose entries may lack OMD and therefore remain unallocated.

Live AWS tags were confirmed for representative MWAA/Flink resources, a DAX cache, an OSIS pipeline and a Synthetics canary. Mapper tests cover the additional supported identity shapes; production billing attribution still requires a dev pilot and reconciliation.

### Resource tags are missing, legacy-only or inconsistent

Representative Lambda resources genuinely lacked `omd_component` while retaining service/business-service/contact tags. A sampled Databricks EBS volume had `omd_microservice` but no canonical `omd_component`; its resource environment tag also differed from its account environment. These are owner/tag-taxonomy issues, not safe automatic enrichment candidates.

Do not silently equate a legacy component string with a current catalog identity, derive an owner from a resource name, or treat a nonempty spelling as catalog-valid. Preserve the known OMD fields and route missing/canonicalization work to the service owner. The source PR does not modify AWS resource tags.

### Aggregate, application-defined or unsupported metrics

Examples include metrics with only `Operation`, `InstanceType`, `ModelId`, query status or no dimensions; multi-resource S3 replication dimensions also do not establish a single owner. They cannot inherit an arbitrary account's or exporter's OMD.

The remaining 19 configured custom namespaces have no implemented AWS-resource owner path:

- `AWS/Bedrock`, `AWS/Bedrock-AgentCore`, `AWS/EC2/API`.
- `Blackbox/CrashDumpReceiver`, `Blackbox/ErrorReceiver`, `ClaudeCode`, `CustomFieldProcessor`, `HostedFarm`, `WebAppCore`.
- `Observability/ServiceCatalogMetering`, `providence-flink-autoscaler`.
- `RDP/reportingDeliveryFeedback`, `WBD/ADPLT/Reporting`, `WBD/ADPLT/ReportingEventLoader`.
- `continue-watching-worker`, `databricks`, `databricks-reformat`, `gqa-o11y`, `mux-qoe-observability`.

These need a verified additional resource mapping, emitted owner labels, or an explicitly approved job-level ownership declaration. A namespace name alone is not sufficient evidence. Aggregate requests within supported namespaces remain unallocated too.

## What the PR does not claim

- No request-weighted or dollar-weighted coverage improvement is asserted before the feature runs.
- Existing exported `aws_*` series remain unchanged; request counters can become attributable while raw-series OMD coverage remains unchanged.
- No claim that all missing tags, all customer costs, all GetMetricData callers or every application-defined metric are covered.
- No permanent deletion/collection decision follows from ownership or high volume. Dependency and owner review remain required.
- No collector image, Flux change or Backstage billing job is deployed by this source PR.
