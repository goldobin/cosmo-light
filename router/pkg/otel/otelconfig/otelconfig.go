package otelconfig

import "os"

type Exporter string
type ExporterTemporality string

const (
	ExporterOLTPHTTP Exporter = "http"
	ExporterOLTPGRPC Exporter = "grpc"

	CloudDefaultTelemetryEndpoint = "https://cosmo-otel.wundergraph.com"
	DefaultMetricsPath            = "/v1/metrics"
	DefaultTracesPath             = "/v1/traces"

	DeltaTemporality       ExporterTemporality = "delta"
	CumulativeTemporality  ExporterTemporality = "cumulative"
	CustomCloudTemporality ExporterTemporality = "custom"
)

// DefaultEndpoint is the default endpoint used by subsystems that
// report OTEL data (e.g. metrics, traces, etc...)
func DefaultEndpoint() string {
	// Allow overriding this during development
	if ep := os.Getenv("DEFAULT_TELEMETRY_ENDPOINT"); ep != "" {
		return ep
	}
	return CloudDefaultTelemetryEndpoint
}
