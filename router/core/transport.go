package core

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"

	"github.com/wundergraph/cosmo/router/internal/retrytransport"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/httpclient"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/pool"
	"go.uber.org/zap"
)

type (
	TransportPreHandler  func(req *http.Request, ctx RequestContext) (*http.Request, *http.Response)
	TransportPostHandler func(resp *http.Response, ctx RequestContext) *http.Response
)

type CustomTransport struct {
	roundTripper http.RoundTripper
	preHandlers  []TransportPreHandler
	postHandlers []TransportPostHandler
	logger       *zap.Logger

	sf   map[uint64]*sfCacheItem
	sfMu *sync.RWMutex
}

type sfCacheItem struct {
	loaded   chan struct{}
	response *http.Response
	body     []byte
	err      error
}

func NewCustomTransport(
	logger *zap.Logger,
	roundTripper http.RoundTripper,
	retryOptions retrytransport.RetryOptions,
	enableSingleFlight bool,
) *CustomTransport {
	ct := &CustomTransport{}

	if retryOptions.Enabled {
		// The round trip method is almost always called via the http.Client RoundTripper interface
		// as a result we cannot pass in the request context logger directly, since this will break the interface
		// The RoundTripper is also not in the core package so it does not have access to the
		// getRequestContext function since its private to only the core package
		// As a workaround we pass in a function that can be used to get the logger from within the round tripper
		getRequestContextLogger := func(req *http.Request) *zap.Logger {
			reqContext := getRequestContext(req.Context())
			return reqContext.Logger()
		}
		ct.roundTripper = retrytransport.NewRetryHTTPTransport(roundTripper, retryOptions, getRequestContextLogger)
	} else {
		ct.roundTripper = roundTripper
	}
	if enableSingleFlight {
		ct.sf = make(map[uint64]*sfCacheItem)
		ct.sfMu = &sync.RWMutex{}
	}

	return ct
}

// RoundTrip of the engine upstream requests. The handler is called concurrently for each request.
// Be aware that multiple modules can be active at the same time. Must be concurrency safe.
func (ct *CustomTransport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	moduleContext := &moduleRequestContext{
		requestContext: getRequestContext(req.Context()),
		sendError:      nil,
	}

	if ct.preHandlers != nil {
		for _, preHandler := range ct.preHandlers {
			r, resp := preHandler(req, moduleContext)
			// Non nil response means the handler decided to skip sending the request
			if resp != nil {
				return resp, nil
			}
			req = r
		}
	}

	if !ct.allowSingleFlight(req) {
		resp, err = ct.roundTripper.RoundTrip(req)
	} else {
		resp, err = ct.roundTripSingleFlight(req)
	}

	// Set the error on the request context so that it can be checked by the post handlers
	if err != nil {
		moduleContext.sendError = err
	}

	if ct.postHandlers != nil {
		for _, postHandler := range ct.postHandlers {
			newResp := postHandler(resp, moduleContext)
			// Abort with the first handler that returns a non-nil response
			if newResp != nil {
				return newResp, nil
			}
		}
	}

	if err != nil {
		return nil, err
	}

	return resp, err
}

func (ct *CustomTransport) allowSingleFlight(req *http.Request) bool {
	if ct.sf == nil {
		// Single flight is disabled
		return false
	}

	if req.Header.Get("Upgrade") != "" {
		// Websocket requests are not idempotent
		return false
	}

	if req.Header.Get("Accept") == "text/event-stream" {
		// SSE requests are not idempotent
		return false
	}

	if resolve.SingleFlightDisallowed(req.Context()) {
		// Single flight is disallowed for this request (e.g. because it is a Mutation)
		return false
	}

	return true
}

func (ct *CustomTransport) roundTripSingleFlight(req *http.Request) (*http.Response, error) {
	key := ct.singleFlightKey(req)
	ct.sfMu.RLock()
	item, shared := ct.sf[key]
	ct.sfMu.RUnlock()

	sfStats := resolve.GetSingleFlightStats(req.Context())
	if sfStats != nil {
		sfStats.SingleFlightUsed = true
		sfStats.SingleFlightSharedResponse = shared
	}

	if shared {
		select {
		case <-item.loaded:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}

		// If the single flight item has an error, return it immediately
		// This happens e.g. on network errors
		if item.err != nil {
			return nil, item.err
		}

		res := &http.Response{}
		res.Status = item.response.Status
		res.StatusCode = item.response.StatusCode
		res.Header = item.response.Header
		res.Trailer = item.response.Trailer
		res.ContentLength = item.response.ContentLength
		res.TransferEncoding = item.response.TransferEncoding
		res.Close = item.response.Close
		res.Uncompressed = item.response.Uncompressed
		res.Request = req

		// Restore the body
		res.Body = io.NopCloser(bytes.NewReader(item.body))
		return res, item.err
	}

	if sfStats != nil {
		sfStats.SingleFlightUsed = true
		sfStats.SingleFlightSharedResponse = false
	}

	item = &sfCacheItem{
		loaded: make(chan struct{}),
	}
	ct.sfMu.Lock()
	ct.sf[key] = item
	ct.sfMu.Unlock()
	defer func() {
		close(item.loaded)
		ct.sfMu.Lock()
		delete(ct.sf, key)
		ct.sfMu.Unlock()
	}()

	res, err := ct.roundTripper.RoundTrip(req)
	if err != nil {
		item.err = err
		return nil, err
	}

	defer res.Body.Close()

	item.body, err = io.ReadAll(res.Body)
	if err != nil {
		item.err = err
		return nil, err
	}

	item.response = res

	// Restore the body
	res.Body = io.NopCloser(bytes.NewReader(item.body))

	return res, nil
}

func (ct *CustomTransport) singleFlightKey(req *http.Request) uint64 {
	keyGen := pool.Hash64.Get()
	defer pool.Hash64.Put(keyGen)

	if bodyHash, ok := httpclient.BodyHashFromContext(req.Context()); ok {
		_, _ = keyGen.WriteString(strconv.FormatUint(bodyHash, 10))
	}

	unsortedHeaders := make([]string, 0, len(req.Header))

	for key := range req.Header {
		value := req.Header.Get(key)
		unsortedHeaders = append(unsortedHeaders, key+value)
	}

	sort.Strings(unsortedHeaders)
	for i := range unsortedHeaders {
		_, _ = keyGen.WriteString(unsortedHeaders[i])
	}

	sum := keyGen.Sum64()
	return sum
}

type TransportFactory struct {
	preHandlers              []TransportPreHandler
	postHandlers             []TransportPostHandler
	subgraphTransportOptions *SubgraphTransportOptions
	retryOptions             retrytransport.RetryOptions
	logger                   *zap.Logger
}

var _ ApiTransportFactory = TransportFactory{}

type TransportOptions struct {
	PreHandlers              []TransportPreHandler
	PostHandlers             []TransportPostHandler
	SubgraphTransportOptions *SubgraphTransportOptions
	RetryOptions             retrytransport.RetryOptions
	Logger                   *zap.Logger
}

func NewTransport(opts *TransportOptions) *TransportFactory {
	return &TransportFactory{
		preHandlers:              opts.PreHandlers,
		postHandlers:             opts.PostHandlers,
		retryOptions:             opts.RetryOptions,
		subgraphTransportOptions: opts.SubgraphTransportOptions,
		logger:                   opts.Logger,
	}
}

func (t TransportFactory) RoundTripper(enableSingleFlight bool, baseTransport http.RoundTripper) http.RoundTripper {
	if t.subgraphTransportOptions != nil && t.subgraphTransportOptions.SubgraphMap != nil && len(t.subgraphTransportOptions.SubgraphMap) > 0 {
		baseTransport = NewSubgraphTransport(t.subgraphTransportOptions, baseTransport, t.logger)
	}

	tp := NewCustomTransport(
		t.logger,
		baseTransport,
		t.retryOptions,
		enableSingleFlight,
	)

	tp.preHandlers = t.preHandlers
	tp.postHandlers = t.postHandlers
	tp.logger = t.logger

	return tp
}

func (t TransportFactory) DefaultHTTPProxyURL() *url.URL {
	return nil
}

// SpanNameFormatter formats the span name based on the http request
func SpanNameFormatter(_ string, r *http.Request) string {
	requestContext := getRequestContext(r.Context())

	if requestContext != nil && requestContext.operation != nil {
		return GetSpanName(requestContext.operation.Name(), requestContext.operation.Type())
	}

	return fmt.Sprintf("%s %s", r.Method, r.URL.Path)
}

func GetSpanName(operationName string, operationType string) string {
	if operationName != "" {
		return fmt.Sprintf("%s %s", operationType, operationName)
	}
	return fmt.Sprintf("%s %s", operationType, "unnamed")
}
