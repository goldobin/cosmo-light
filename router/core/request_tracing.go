package core

import (
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

func (h *PreHandler) parseRequestTraceOptions() (options resolve.TraceOptions) {
	options.DisableAll()
	return
}
