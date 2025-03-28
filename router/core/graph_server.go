package core

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"github.com/cloudflare/backoff"
	"github.com/dgraph-io/ristretto/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/klauspost/compress/gzhttp"
	"github.com/klauspost/compress/gzip"
	"github.com/wundergraph/cosmo/router/internal/expr"
	"github.com/wundergraph/cosmo/router/internal/rconf"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"net/http"
	"net/url"
	"sync"
	"time"

	rmiddleware "github.com/wundergraph/cosmo/router/internal/middleware"
	"github.com/wundergraph/cosmo/router/internal/recoveryhandler"
	"github.com/wundergraph/cosmo/router/internal/requestlogger"
	"github.com/wundergraph/cosmo/router/internal/retrytransport"
	"github.com/wundergraph/cosmo/router/pkg/execution_config"
	"github.com/wundergraph/cosmo/router/pkg/health"
	"github.com/wundergraph/cosmo/router/pkg/logging"
	"github.com/wundergraph/cosmo/router/pkg/statistics"
)

type (
	// Server is the public interface of the server.
	Server interface {
		HttpServer() *http.Server
		HealthChecks() health.Checker
	}

	// graphServer is the swappable implementation of a Graph instance which is an HTTP mux with middlewares.
	// Everytime a schema is updated, the old graph server is shutdown and a new graph server is created.
	// For feature flags, a graphql server has multiple mux and is dynamically switched based on the feature flag header or cookie.
	// All fields are shared between all feature muxes. On shutdown, all graph instances are shutdown.
	graphServer struct {
		*Config
		context                 context.Context
		cancelFunc              context.CancelFunc
		engineStats             statistics.EngineStatistics
		playgroundHandler       func(http.Handler) http.Handler
		publicKey               *ecdsa.PublicKey
		executionTransport      *http.Transport
		baseRouterConfigVersion string
		mux                     *chi.Mux
		// inFlightRequests is used to track the number of requests currently being processed
		// does not include websocket (hijacked) connections
		inFlightRequests *atomic.Uint64
		graphMuxList     []*graphMux
		graphMuxListLock sync.Mutex
		hostName         string
		routerListenAddr string
	}
)

// newGraphServer creates a new server instance.
func newGraphServer(ctx context.Context, r *Router, routerConfig *rconf.RouterConfig) (*graphServer, error) {
	/* Older versions of composition will not populate a compatibility version.
	 * Currently, all "old" router execution configurations are compatible as there have been no breaking
	 * changes.
	 * Upon the first breaking change to the execution config, an unpopulated compatibility version will
	 * also be unsupported (and the logic for IsRouterCompatibleWithExecutionConfig will need to be updated).
	 */
	if !execution_config.IsRouterCompatibleWithExecutionConfig(r.logger, routerConfig.CompatibilityVersion) {
		return nil, fmt.Errorf(`the compatibility version "%s" is not compatible with this router version`, routerConfig.CompatibilityVersion)
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &graphServer{
		context:                 ctx,
		cancelFunc:              cancel,
		Config:                  &r.Config,
		engineStats:             r.EngineStats,
		executionTransport:      newHTTPTransport(r.subgraphTransportOptions.TransportRequestOptions),
		playgroundHandler:       r.playgroundHandler,
		baseRouterConfigVersion: routerConfig.Version,
		inFlightRequests:        &atomic.Uint64{},
		graphMuxList:            make([]*graphMux, 0, 1),
		routerListenAddr:        r.listenAddr,
		hostName:                r.hostName,
	}

	httpRouter := chi.NewRouter()

	/**
	* Middlewares
	 */

	// This recovery handler is used for everything before the graph mux to ensure that
	// we can recover from panics and log them properly.
	httpRouter.Use(recoveryhandler.New(recoveryhandler.WithLogHandler(func(w http.ResponseWriter, r *http.Request, err any) {
		s.logger.Error("[Recovery from panic]",
			zap.Any("error", err),
		)
	})))

	// Request traffic shaping related middlewares
	httpRouter.Use(rmiddleware.RequestSize(int64(s.routerTrafficConfig.MaxRequestBodyBytes)))
	if s.routerTrafficConfig.DecompressionEnabled {
		httpRouter.Use(rmiddleware.HandleCompression(s.logger))
	}

	httpRouter.Use(middleware.RequestID)
	httpRouter.Use(middleware.RealIP)

	gm, err := s.buildGraphMux(ctx, "", s.baseRouterConfigVersion, routerConfig.EngineConfig, routerConfig.Subgraphs)
	if err != nil {
		return nil, fmt.Errorf("failed to build base mux: %w", err)
	}

	multiGraphHandler := gm.mux

	wrapper, err := gzhttp.NewWrapper(
		gzhttp.MinSize(1024*4), // 4KB
		gzhttp.CompressionLevel(gzip.DefaultCompression),
		gzhttp.ContentTypes(CompressibleContentTypes),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip wrapper: %w", err)
	}

	/**
	* A group where we can selectively apply middlewares to the graphql endpoint
	 */
	httpRouter.Group(func(cr chi.Router) {
		// We are applying it conditionally because compressing 3MB playground is still slow even with stdlib gzip
		cr.Use(func(h http.Handler) http.Handler {
			return wrapper(h)
		})

		// Mount the feature flag handler. It calls the base mux if no feature flag is set.
		cr.Handle(r.graphqlPath, multiGraphHandler)

		if r.webSocketConfiguration != nil && r.webSocketConfiguration.Enabled && r.webSocketConfiguration.AbsintheProtocol.Enabled {
			// Mount the Absinthe protocol handler for WebSockets
			httpRouter.Handle(r.webSocketConfiguration.AbsintheProtocol.HandlerPath, multiGraphHandler)
		}
	})

	/**
	* Routes
	 */

	// We mount the playground once here when we don't have a conflict with the websocket handler
	// If we have a conflict, we mount the playground during building the individual muxes
	if s.playgroundHandler != nil && s.graphqlPath != s.playgroundConfig.Path {
		httpRouter.Get(r.playgroundConfig.Path, s.playgroundHandler(nil).ServeHTTP)
	}

	httpRouter.Get(s.healthCheckPath, r.healthcheck.Liveness())
	httpRouter.Get(s.livenessCheckPath, r.healthcheck.Liveness())
	httpRouter.Get(s.readinessCheckPath, r.healthcheck.Readiness())

	s.mux = httpRouter

	return s, nil
}

type graphMux struct {
	mux                        *chi.Mux
	planCache                  *ristretto.Cache[uint64, *planWithMetaData]
	normalizationCache         *ristretto.Cache[uint64, NormalizationCacheEntry]
	complexityCalculationCache *ristretto.Cache[uint64, ComplexityCacheEntry]
	validationCache            *ristretto.Cache[uint64, bool]
	operationHashCache         *ristretto.Cache[uint64, string]
	accessLogsFileLogger       *logging.BufferedLogger
}

// buildOperationCaches creates the caches for the graph mux.
// The caches are created based on the engine configuration.
func (s *graphMux) buildOperationCaches(srv *graphServer) (err error) {
	// We create a new execution plan cache for each operation planner which is coupled to
	// the specific engine configuration. This is necessary because otherwise we would return invalid plans.
	//
	// when an execution plan was generated, which can be quite expensive, we want to cache it
	// this means that we can hash the input and cache the generated plan
	// the next time we get the same input, we can just return the cached plan
	// the engine is smart enough to first do normalization and then hash the input
	// this means that we can cache the normalized input and don't have to worry about
	// different inputs that would generate the same execution plan

	if srv.engineExecutionConfiguration.ExecutionPlanCacheSize > 0 {
		planCacheConfig := &ristretto.Config[uint64, *planWithMetaData]{
			Metrics:            false,
			MaxCost:            srv.engineExecutionConfiguration.ExecutionPlanCacheSize,
			NumCounters:        srv.engineExecutionConfiguration.ExecutionPlanCacheSize * 10,
			IgnoreInternalCost: true,
			BufferItems:        64,
		}
		s.planCache, err = ristretto.NewCache[uint64, *planWithMetaData](planCacheConfig)
		if err != nil {
			return fmt.Errorf("failed to create planner cache: %w", err)
		}
	}

	if srv.engineExecutionConfiguration.EnableNormalizationCache && srv.engineExecutionConfiguration.NormalizationCacheSize > 0 {
		normalizationCacheConfig := &ristretto.Config[uint64, NormalizationCacheEntry]{
			Metrics:            false,
			MaxCost:            srv.engineExecutionConfiguration.NormalizationCacheSize,
			NumCounters:        srv.engineExecutionConfiguration.NormalizationCacheSize * 10,
			IgnoreInternalCost: true,
			BufferItems:        64,
		}
		s.normalizationCache, err = ristretto.NewCache[uint64, NormalizationCacheEntry](normalizationCacheConfig)
		if err != nil {
			return fmt.Errorf("failed to create normalization cache: %w", err)
		}
	}

	if srv.engineExecutionConfiguration.EnableValidationCache && srv.engineExecutionConfiguration.ValidationCacheSize > 0 {
		validationCacheConfig := &ristretto.Config[uint64, bool]{
			Metrics:            false,
			MaxCost:            srv.engineExecutionConfiguration.ValidationCacheSize,
			NumCounters:        srv.engineExecutionConfiguration.ValidationCacheSize * 10,
			IgnoreInternalCost: true,
			BufferItems:        64,
		}
		s.validationCache, err = ristretto.NewCache[uint64, bool](validationCacheConfig)
		if err != nil {
			return fmt.Errorf("failed to create validation cache: %w", err)
		}
	}

	return nil
}

func (s *graphMux) Shutdown() error {
	var err error

	if s.planCache != nil {
		s.planCache.Close()
	}

	if s.normalizationCache != nil {
		s.normalizationCache.Close()
	}

	if s.validationCache != nil {
		s.validationCache.Close()
	}

	if s.complexityCalculationCache != nil {
		s.complexityCalculationCache.Close()
	}

	if s.accessLogsFileLogger != nil {
		if aErr := s.accessLogsFileLogger.Close(); aErr != nil {
			err = errors.Join(err, aErr)
		}
	}

	return err
}

// buildGraphMux creates a new graph mux with the given feature flags and engine configuration.
// It also creates a new execution plan cache for the mux. The mux is not mounted on the server.
// The mux is appended internally to the graph server's list of muxes to clean up later when the server is swapped.
func (s *graphServer) buildGraphMux(ctx context.Context,
	featureFlagName string,
	routerConfigVersion string,
	engineConfig *rconf.EngineConfiguration,
	configSubgraphs []*rconf.Subgraph,
) (*graphMux, error) {
	gm := &graphMux{}
	httpRouter := chi.NewRouter()
	exprManager := expr.CreateNewExprManager()
	subgraphs, err := configureSubgraphs(configSubgraphs)
	if err != nil {
		return nil, err
	}

	if err := gm.buildOperationCaches(s); err != nil {
		return nil, err
	}

	baseLogFields := []zapcore.Field{
		zap.String("config_version", routerConfigVersion),
	}

	if featureFlagName != "" {
		baseLogFields = append(baseLogFields, zap.String("feature_flag", featureFlagName))
	}

	// Enrich the request context with the subgraph information which is required for custom modules and tracing
	subgraphResolver := NewSubgraphResolver(subgraphs)
	httpRouter.Use(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestLogger := s.logger.With(logging.WithRequestID(middleware.GetReqID(r.Context())))
			r = r.WithContext(withSubgraphResolver(r.Context(), subgraphResolver))

			reqContext := buildRequestContext(requestContextOptions{
				operationContext: nil,
				requestLogger:    requestLogger,
				w:                w,
				r:                r,
			})

			r = r.WithContext(withRequestContext(r.Context(), reqContext))

			// For debugging purposes, we can validate from what version of the config the request is coming from
			if s.setConfigVersionHeader {
				w.Header().Set("X-Router-Config-Version", routerConfigVersion)
			}

			h.ServeHTTP(w, r)
		})
	})

	var recoverOpts []recoveryhandler.Option

	// If we have no access logger configured, we log the panic in the recovery handler to avoid losing the panic information
	if s.accessLogsConfig == nil {
		recoverOpts = append(recoverOpts, recoveryhandler.WithLogHandler(func(w http.ResponseWriter, r *http.Request, err any) {
			reqContext := getRequestContext(r.Context())
			if reqContext != nil {
				reqContext.logger.Error("[Recovery from panic]",
					zap.Any("error", err),
				)
			}
		}))
	}

	recoveryHandler := recoveryhandler.New(recoverOpts...)

	httpRouter.Use(recoveryHandler)

	// Setup any router on request middlewares so that they can be used to manipulate
	// other downstream internal middlewares such as tracing or authentication
	httpRouter.Use(s.routerOnRequestHandlers...)

	httpRouter.Use(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r)
		})
	})

	var subgraphAccessLogger *requestlogger.SubgraphAccessLogger
	if s.accessLogsConfig != nil && s.accessLogsConfig.Logger != nil {
		exprAttributes, err := requestlogger.GetAccessLogConfigExpressions(s.accessLogsConfig.Attributes, exprManager)
		if err != nil {
			return nil, fmt.Errorf("failed building router access log expressions: %w", err)
		}

		s.accessLogsConfig.Attributes = requestlogger.CleanupExpressionAttributes(s.accessLogsConfig.Attributes)

		requestLoggerOpts := []requestlogger.Option{
			requestlogger.WithDefaultOptions(),
			requestlogger.WithNoTimeField(),
			requestlogger.WithFields(baseLogFields...),
			requestlogger.WithAttributes(s.accessLogsConfig.Attributes),
			requestlogger.WithExprAttributes(exprAttributes),
			requestlogger.WithFieldsHandler(RouterAccessLogsFieldHandler),
		}

		requestLogger := requestlogger.New(
			s.accessLogsConfig.Logger,
			requestLoggerOpts...,
		)
		httpRouter.Use(requestLogger)

		if s.accessLogsConfig.SubgraphEnabled {
			s.accessLogsConfig.SubgraphAttributes = requestlogger.CleanupExpressionAttributes(s.accessLogsConfig.SubgraphAttributes)

			subgraphAccessLogger = requestlogger.NewSubgraphAccessLogger(
				s.accessLogsConfig.Logger,
				requestlogger.SubgraphOptions{
					FieldsHandler: SubgraphAccessLogsFieldHandler,
					Fields:        baseLogFields,
					Attributes:    s.accessLogsConfig.SubgraphAttributes,
				})
		}
	}

	routerEngineConfig := &RouterEngineConfiguration{
		Execution:                s.engineExecutionConfiguration,
		Headers:                  s.headerRules,
		SubgraphErrorPropagation: s.subgraphErrorPropagation,
	}

	ecb := &ExecutorConfigurationBuilder{
		introspection: s.introspection,
		baseURL:       s.baseURL,
		transport:     s.executionTransport,
		logger:        s.logger,
		transportOptions: &TransportOptions{
			SubgraphTransportOptions: s.subgraphTransportOptions,
			PreHandlers:              s.preOriginHandlers,
			PostHandlers:             s.postOriginHandlers,
			RetryOptions: retrytransport.RetryOptions{
				Enabled:       s.retryOptions.Enabled,
				MaxRetryCount: s.retryOptions.MaxRetryCount,
				MaxDuration:   s.retryOptions.MaxDuration,
				Interval:      s.retryOptions.Interval,
				ShouldRetry: func(err error, req *http.Request, resp *http.Response) bool {
					return retrytransport.IsRetryableError(err, resp) && !isMutationRequest(req.Context())
				},
			},
			Logger: s.logger,
		},
	}

	executor, err := ecb.Build(
		ctx,
		&ExecutorBuildOptions{
			EngineConfig:                   engineConfig,
			Subgraphs:                      configSubgraphs,
			RouterEngineConfig:             routerEngineConfig,
			Reporter:                       s.engineStats,
			ApolloCompatibilityFlags:       s.apolloCompatibilityFlags,
			ApolloRouterCompatibilityFlags: s.apolloRouterCompatibilityFlags,
			HeartbeatInterval:              s.multipartHeartbeatInterval,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build plan configuration: %w", err)
	}

	operationProcessor := NewOperationProcessor(OperationProcessorOptions{
		Executor:                       executor,
		MaxOperationSizeInBytes:        int64(s.routerTrafficConfig.MaxRequestBodyBytes),
		NormalizationCache:             gm.normalizationCache,
		ValidationCache:                gm.validationCache,
		QueryDepthCache:                gm.complexityCalculationCache,
		OperationHashCache:             gm.operationHashCache,
		ParseKitPoolSize:               s.engineExecutionConfiguration.ParseKitPoolSize,
		IntrospectionEnabled:           s.Config.introspection,
		ApolloCompatibilityFlags:       s.apolloCompatibilityFlags,
		ApolloRouterCompatibilityFlags: s.apolloRouterCompatibilityFlags,
	})
	operationPlanner := NewOperationPlanner(executor, gm.planCache)

	authorizerOptions := &CosmoAuthorizerOptions{
		FieldConfigurations:           engineConfig.FieldConfigurations,
		RejectOperationIfUnauthorized: false,
	}

	if s.Config.authorization != nil {
		authorizerOptions.RejectOperationIfUnauthorized = s.authorization.RejectOperationIfUnauthorized
	}

	handlerOpts := HandlerOptions{
		Executor:                               executor,
		Log:                                    s.logger,
		EnableExecutionPlanCacheResponseHeader: s.engineExecutionConfiguration.EnableExecutionPlanCacheResponseHeader,
		EnablePersistedOperationCacheResponseHeader: s.engineExecutionConfiguration.Debug.EnablePersistedOperationsCacheResponseHeader,
		EnableNormalizationCacheResponseHeader:      s.engineExecutionConfiguration.Debug.EnableNormalizationCacheResponseHeader,
		EnableResponseHeaderPropagation:             s.headerRules != nil,
		EngineStats:                                 s.engineStats,
		Authorizer:                                  NewCosmoAuthorizer(authorizerOptions),
		SubgraphErrorPropagation:                    s.subgraphErrorPropagation,
		EngineLoaderHooks:                           NewEngineRequestHooks(subgraphAccessLogger),
	}

	if s.apolloCompatibilityFlags.SubscriptionMultipartPrintBoundary.Enabled {
		handlerOpts.ApolloSubscriptionMultipartPrintBoundary = s.apolloCompatibilityFlags.SubscriptionMultipartPrintBoundary.Enabled
	}

	graphqlHandler := NewGraphQLHandler(handlerOpts)
	executor.Resolver.SetAsyncErrorWriter(graphqlHandler)

	graphqlPreHandler := NewPreHandler(&PreHandlerOptions{
		Logger:                    s.logger,
		Executor:                  executor,
		OperationProcessor:        operationProcessor,
		Planner:                   operationPlanner,
		AccessController:          s.accessController,
		RouterPublicKey:           s.publicKey,
		DevelopmentMode:           s.developmentMode,
		ComplexityLimits:          nil,
		AlwaysIncludeQueryPlan:    s.engineExecutionConfiguration.Debug.AlwaysIncludeQueryPlan,
		AlwaysSkipLoader:          s.engineExecutionConfiguration.Debug.AlwaysSkipLoader,
		QueryPlansEnabled:         s.Config.queryPlansEnabled,
		QueryPlansLoggingEnabled:  s.engineExecutionConfiguration.Debug.PrintQueryPlans,
		ClientHeader:              s.clientHeader,
		ApolloCompatibilityFlags:  &s.apolloCompatibilityFlags,
		DisableVariablesRemapping: s.engineExecutionConfiguration.DisableVariablesRemapping,
		ExprManager:               exprManager,
	})

	if s.webSocketConfiguration != nil && s.webSocketConfiguration.Enabled {
		wsMiddleware := NewWebsocketMiddleware(ctx, WebsocketMiddlewareOptions{
			OperationProcessor:        operationProcessor,
			Planner:                   operationPlanner,
			GraphQLHandler:            graphqlHandler,
			PreHandler:                graphqlPreHandler,
			AccessController:          s.accessController,
			Logger:                    s.logger,
			Stats:                     s.engineStats,
			ReadTimeout:               s.engineExecutionConfiguration.WebSocketClientReadTimeout,
			EnableNetPoll:             s.engineExecutionConfiguration.EnableNetPoll,
			NetPollTimeout:            s.engineExecutionConfiguration.WebSocketClientPollTimeout,
			NetPollConnBufferSize:     s.engineExecutionConfiguration.WebSocketClientConnBufferSize,
			WebSocketConfiguration:    s.webSocketConfiguration,
			ClientHeader:              s.clientHeader,
			DisableVariablesRemapping: s.engineExecutionConfiguration.DisableVariablesRemapping,
			ApolloCompatibilityFlags:  s.apolloCompatibilityFlags,
		})

		// When the playground path is equal to the graphql path, we need to handle
		// ws upgrades and html requests on the same route.
		if s.playgroundConfig.Enabled && s.graphqlPath == s.playgroundConfig.Path {
			httpRouter.Use(s.playgroundHandler, wsMiddleware)
		} else {
			httpRouter.Use(wsMiddleware)
		}
	}

	httpRouter.Use(
		// Responsible for handling regular GraphQL requests over HTTP not WebSockets
		graphqlPreHandler.Handler,
		// Must be mounted after the websocket middleware to ensure that we only count non-hijacked requests like WebSockets
		func(handler http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestContext := getRequestContext(r.Context())

				// We don't want to count any type of subscriptions e.g. SSE as in-flight requests because they are long-lived
				if requestContext != nil && requestContext.operation != nil && requestContext.operation.opType != OperationTypeSubscription {
					s.inFlightRequests.Add(1)

					// Counting like this is safe because according to the go http.ServeHTTP documentation
					// the requests is guaranteed to be finished when ServeHTTP returns
					defer s.inFlightRequests.Sub(1)
				}

				handler.ServeHTTP(w, r)
			})
		})

	// Mount built global and custom modules
	// Needs to be mounted after the pre-handler to ensure that the request was parsed and authorized
	httpRouter.Use(s.routerMiddlewares...)

	// GraphQL over POST
	httpRouter.Post(s.graphqlPath, graphqlHandler.ServeHTTP)
	// GraphQL over GET
	httpRouter.Get(s.graphqlPath, graphqlHandler.ServeHTTP)

	gm.mux = httpRouter

	s.graphMuxListLock.Lock()
	defer s.graphMuxListLock.Unlock()
	s.graphMuxList = append(s.graphMuxList, gm)

	return gm, nil
}

// wait waits for all in-flight requests to finish. Similar to http.Server.Shutdown we wait in intervals + jitter
// to make the shutdown process more efficient.
func (s *graphServer) wait(ctx context.Context) error {
	b := backoff.New(500*time.Millisecond, time.Millisecond)
	defer b.Reset()

	timer := time.NewTimer(b.Duration())
	defer timer.Stop()

	for {
		if s.inFlightRequests.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			timer.Reset(b.Duration())
		}
	}
}

// Shutdown gracefully shutdown the server and waits for all in-flight requests to finish.
// After all requests are done, it will shut down the metric store and runtime metrics.
// Shutdown does cancel the context after all non-hijacked requests such as WebSockets has been handled.
func (s *graphServer) Shutdown(ctx context.Context) error {
	// Cancel the context after the graceful shutdown is done
	// to clean up resources like websocket connections, pools, etc.
	defer s.cancelFunc()

	s.logger.Debug("Shutdown of graph server initiated. Waiting for in-flight requests to finish.",
		zap.String("config_version", s.baseRouterConfigVersion),
	)

	var finalErr error

	// Wait for all in-flight requests to finish.
	// In the worst case, we wait until the context is done or all requests has timed out.
	if err := s.wait(ctx); err != nil {
		s.logger.Error("Failed to wait for in-flight requests to finish", zap.Error(err))
		finalErr = errors.Join(finalErr, err)
	}

	s.logger.Debug("Shutdown of graph server resources",
		zap.String("grace_period", s.routerGracePeriod.String()),
		zap.String("config_version", s.baseRouterConfigVersion),
	)

	// Ensure that we don't wait indefinitely for shutdown
	if s.routerGracePeriod > 0 {
		newCtx, cancel := context.WithTimeout(ctx, s.routerGracePeriod)
		defer cancel()

		ctx = newCtx
	}

	// Shutdown all graphs muxes to release resources
	// e.g. planner cache
	s.graphMuxListLock.Lock()
	defer s.graphMuxListLock.Unlock()
	for _, mux := range s.graphMuxList {
		if err := mux.Shutdown(); err != nil {
			s.logger.Error("Failed to shutdown graph mux", zap.Error(err))
			finalErr = errors.Join(finalErr, err)
		}
	}

	return finalErr
}

func configureSubgraphs(
	configSubgraphs []*rconf.Subgraph,
) ([]Subgraph, error) {
	var err error
	subgraphs := make([]Subgraph, 0, len(configSubgraphs))
	for _, sg := range configSubgraphs {
		subgraph := Subgraph{
			Id:   sg.Id,
			Name: sg.Name,
		}

		// Validate subgraph url. Note that it can be empty if the subgraph is virtual
		subgraph.Url, err = url.Parse(sg.RoutingUrl)
		if err != nil {
			return nil, fmt.Errorf("failed to parse subgraph url '%s': %w", sg.RoutingUrl, err)
		}
		subgraph.UrlString = subgraph.Url.String()
		subgraphs = append(subgraphs, subgraph)
	}

	return subgraphs, nil
}
