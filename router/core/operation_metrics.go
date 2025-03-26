package core

import (
	"time"

	rotel "github.com/wundergraph/cosmo/router/pkg/otel"
	otelmetric "go.opentelemetry.io/otel/metric"

	"go.uber.org/zap"

	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"

	"go.opentelemetry.io/otel/attribute"
)

type OperationProtocol string

const (
	OperationProtocolHTTP = OperationProtocol("http")
	OperationProtocolWS   = OperationProtocol("ws")
)

func (p OperationProtocol) String() string {
	return string(p)
}

// OperationMetrics is a struct that holds the metrics for an operation. It should be created on the parent router request
// subgraph metrics are created in the transport or engine loader hooks.
type OperationMetrics struct {
	requestContentLength int64
	operationStartTime   time.Time
	inflightMetric       func()
	routerConfigVersion  string
	logger               *zap.Logger
	trackUsageInfo       bool
}

func (m *OperationMetrics) Finish(reqContext *requestContext, statusCode int) {
	m.inflightMetric()

	attrs := *reqContext.telemetry.AcquireAttributes()
	defer reqContext.telemetry.ReleaseAttributes(&attrs)

	attrs = append(attrs, semconv.HTTPStatusCode(statusCode))
	attrs = append(attrs, reqContext.telemetry.metricAttrs...)
	if reqContext.error != nil {
		attrs = append(attrs, rotel.WgRequestError.Bool(true))
	}
}

type OperationMetricsOptions struct {
	InFlightAddOption    otelmetric.AddOption
	SliceAttributes      []attribute.KeyValue
	RouterConfigVersion  string
	RequestContentLength int64
	Logger               *zap.Logger
	TrackUsageInfo       bool
}
