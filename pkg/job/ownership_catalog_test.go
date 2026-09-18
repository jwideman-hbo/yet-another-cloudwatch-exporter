package job

import "testing"

func TestSweptCustomNamespaceCoverageIsExplicit(t *testing.T) {
	cases := map[string]bool{
		"/aws/sagemaker/Endpoints": true,
		"AWS/ApiGateway": true,
		"AWS/Bedrock": false,
		"AWS/Bedrock-AgentCore": false,
		"AWS/DAX": true,
		"AWS/DDoSProtection": true,
		"AWS/EC2/API": false,
		"AWS/EC2CapacityReservations": true,
		"AWS/EKS": true,
		"AWS/Firehose": true,
		"AWS/Glue": true,
		"AWS/KinesisAnalytics": true,
		"AWS/MWAA": true,
		"AWS/OSIS": true,
		"AWS/Personalize": true,
		"AWS/Redshift": true,
		"AWS/SageMaker": true,
		"AWS/States": true,
		"AWS/Timestream": true,
		"AWS/Usage": true,
		"AWS/WAFV2": true,
		"AmazonMWAA": true,
		"Blackbox/CrashDumpReceiver": false,
		"Blackbox/ErrorReceiver": false,
		"ClaudeCode": false,
		"CloudWatchSynthetics": true,
		"CloudWatchSynthetics/Custom": true,
		"CustomFieldProcessor": false,
		"HostedFarm": false,
		"Observability/ServiceCatalogMetering": false,
		"RDP/reportingDeliveryFeedback": false,
		"WBD/ADPLT/Reporting": false,
		"WBD/ADPLT/ReportingEventLoader": false,
		"WebAppCore": false,
		"continue-watching-worker": false,
		"databricks": false,
		"databricks-reformat": false,
		"gqa-o11y": false,
		"mux-qoe-observability": false,
		"providence-flink-autoscaler": false,
	}
	for namespace, expected := range cases {
		t.Run(namespace, func(t *testing.T) {
			_, supported := customNamespaceOwnerMapping(namespace)
			if supported != expected {
				t.Fatalf("supported=%v, want %v", supported, expected)
			}
		})
	}
}
