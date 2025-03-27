package core

import (
	"time"

	otelmetric "go.opentelemetry.io/otel/metric"

	"go.uber.org/zap"

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

func (m *OperationMetrics) Finish() {
	m.inflightMetric()
}

type OperationMetricsOptions struct {
	InFlightAddOption    otelmetric.AddOption
	SliceAttributes      []attribute.KeyValue
	RouterConfigVersion  string
	RequestContentLength int64
	Logger               *zap.Logger
	TrackUsageInfo       bool
}
