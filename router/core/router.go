package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/nats-io/nuid"
	"go.uber.org/atomic"
	"go.uber.org/zap"

	nodev1 "github.com/wundergraph/cosmo/router/gen/proto/wg/cosmo/node/v1"
	"github.com/wundergraph/cosmo/router/internal/debug"
	"github.com/wundergraph/cosmo/router/internal/graphiql"
	"github.com/wundergraph/cosmo/router/internal/retrytransport"
	"github.com/wundergraph/cosmo/router/pkg/config"
	"github.com/wundergraph/cosmo/router/pkg/execution_config"
	"github.com/wundergraph/cosmo/router/pkg/health"
	"github.com/wundergraph/cosmo/router/pkg/statistics"
	"github.com/wundergraph/cosmo/router/pkg/watcher"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/netpoll"
)

var CompressibleContentTypes = []string{
	"text/html",
	"text/css",
	"text/plain",
	"text/javascript",
	"application/javascript",
	"application/x-javascript",
	"application/json",
	"application/atom+xml",
	"application/rss+xml",
	"image/svg+xml",
	"application/graphql",
	"application/graphql-response+json",
	"application/graphql+json",
}

type (
	// Router is the main application instance.
	Router struct {
		Config
		httpServer        *server
		modules           []Module
		EngineStats       statistics.EngineStatistics
		playgroundHandler func(http.Handler) http.Handler
	}

	TransportRequestOptions struct {
		RequestTimeout         time.Duration
		ResponseHeaderTimeout  time.Duration
		ExpectContinueTimeout  time.Duration
		KeepAliveIdleTimeout   time.Duration
		DialTimeout            time.Duration
		TLSHandshakeTimeout    time.Duration
		KeepAliveProbeInterval time.Duration

		MaxConnsPerHost     int
		MaxIdleConns        int
		MaxIdleConnsPerHost int
	}

	SubgraphTransportOptions struct {
		*TransportRequestOptions
		SubgraphMap map[string]*TransportRequestOptions
	}

	TlsClientAuthConfig struct {
		Required bool
		CertFile string
	}

	TlsConfig struct {
		Enabled  bool
		CertFile string
		KeyFile  string

		ClientAuth *TlsClientAuthConfig
	}

	ExecutionConfig struct {
		Watch         bool
		WatchInterval time.Duration
		Path          string
	}

	AccessLogsConfig struct {
		Attributes         []config.CustomAttribute
		Logger             *zap.Logger
		SubgraphEnabled    bool
		SubgraphAttributes []config.CustomAttribute
	}

	// Config defines the configuration options for the Router.
	Config struct {
		clusterName                    string
		instanceID                     string
		logger                         *zap.Logger
		setConfigVersionHeader         bool
		routerGracePeriod              time.Duration
		staticExecutionConfig          *nodev1.RouterConfig
		shutdown                       atomic.Bool
		bootstrapped                   atomic.Bool
		listenAddr                     string
		baseURL                        string
		graphqlWebURL                  string
		playgroundPath                 string
		graphqlPath                    string
		playground                     bool
		introspection                  bool
		queryPlansEnabled              bool
		healthCheckPath                string
		readinessCheckPath             string
		livenessCheckPath              string
		playgroundConfig               config.PlaygroundConfig
		cacheControlPolicy             config.CacheControlPolicy
		apolloCompatibilityFlags       config.ApolloCompatibilityFlags
		apolloRouterCompatibilityFlags config.ApolloRouterCompatibilityFlags
		modulesConfig                  map[string]interface{}
		executionConfig                *ExecutionConfig
		routerOnRequestHandlers        []func(http.Handler) http.Handler
		routerMiddlewares              []func(http.Handler) http.Handler
		preOriginHandlers              []TransportPreHandler
		postOriginHandlers             []TransportPostHandler
		headerRules                    *config.HeaderRules
		subgraphTransportOptions       *SubgraphTransportOptions
		routerTrafficConfig            *config.RouterTrafficConfiguration
		accessController               *AccessController
		retryOptions                   retrytransport.RetryOptions
		processStartTime               time.Time
		developmentMode                bool
		healthcheck                    health.Checker
		accessLogsConfig               *AccessLogsConfig
		tlsServerConfig                *tls.Config
		tlsConfig                      *TlsConfig
		customModules                  []Module
		engineExecutionConfiguration   config.EngineExecutionConfiguration
		// should be removed once the users have migrated to the new overrides config
		overrideRoutingURLConfiguration config.OverrideRoutingURLConfiguration
		// the new overrides config
		overrides                  config.OverridesConfiguration
		authorization              *config.AuthorizationConfiguration
		webSocketConfiguration     *config.WebSocketConfiguration
		subgraphErrorPropagation   config.SubgraphErrorPropagationConfiguration
		clientHeader               config.ClientHeader
		multipartHeartbeatInterval time.Duration
		hostName                   string
	}
	// Option defines the method to customize server.
	Option func(svr *Router)
)

// NewRouter creates a new Router instance. Router.Start() must be called to start the server.
// Alternatively, use Router.NewServer() to create a new server instance without starting it.
func NewRouter(opts ...Option) (*Router, error) {
	r := &Router{
		EngineStats: statistics.NewNoopEngineStats(),
	}

	for _, opt := range opts {
		opt(r)
	}

	if r.logger == nil {
		r.logger = zap.NewNop()
	}

	// Default value for graphql path
	if r.graphqlPath == "" {
		r.graphqlPath = "/graphql"
	}

	if r.graphqlWebURL == "" {
		r.graphqlWebURL = r.graphqlPath
	}

	// this is set via the deprecated method
	if !r.playground {
		r.playgroundConfig.Enabled = r.playground
		r.logger.Warn("The playground_enabled option is deprecated. Use the playground.enabled option in the config instead.")
	}
	if r.playgroundPath != "" && r.playgroundPath != "/" {
		r.playgroundConfig.Path = r.playgroundPath
		r.logger.Warn("The playground_path option is deprecated. Use the playground.path option in the config instead.")
	}

	if r.playgroundConfig.Path == "" {
		r.playgroundConfig.Path = "/"
	}

	if r.instanceID == "" {
		r.instanceID = nuid.Next()
	}

	r.processStartTime = time.Now()

	if r.subgraphTransportOptions == nil {
		r.subgraphTransportOptions = DefaultSubgraphTransportOptions()
	}

	if r.routerTrafficConfig == nil {
		r.routerTrafficConfig = DefaultRouterTrafficConfig()
	}

	if r.accessController != nil {
		if len(r.accessController.authenticators) == 0 && r.accessController.authenticationRequired {
			r.logger.Warn("authentication is required but no authenticators are configured")
		}
	}

	// Default values for health check paths

	if r.healthCheckPath == "" {
		r.healthCheckPath = "/health"
	}
	if r.readinessCheckPath == "" {
		r.readinessCheckPath = "/health/ready"
	}
	if r.livenessCheckPath == "" {
		r.livenessCheckPath = "/health/live"
	}

	r.headerRules = AddCacheControlPolicyToRules(r.headerRules, r.cacheControlPolicy)
	hr, err := NewHeaderPropagation(r.headerRules)
	if err != nil {
		return nil, err
	}

	if hr.HasRequestRules() {
		r.preOriginHandlers = append(r.preOriginHandlers, hr.OnOriginRequest)
	}

	r.preOriginHandlers = append(r.preOriginHandlers, func(req *http.Request, ctx RequestContext) (*http.Request, *http.Response) {
		return req, nil
	})

	if hr.HasResponseRules() {
		r.postOriginHandlers = append(r.postOriginHandlers, hr.OnOriginResponse)
	}

	defaultHeaders := []string{
		// Common headers
		"authorization",
		"origin",
		"content-length",
		"content-type",
		// Semi standard client info headers
		"graphql-client-name",
		"graphql-client-version",
		// Apollo client info headers
		"apollographql-client-name",
		"apollographql-client-version",
		// Required for WunderGraph ART
		"x-wg-trace",
		"x-wg-disable-tracing",
		"x-wg-token",
		"x-wg-skip-loader",
		"x-wg-include-query-plan",
		// Required for Trace Context propagation
		"traceparent",
		"tracestate",
		// Required for feature flags
		"x-feature-flag",
	}

	if r.clientHeader.Name != "" {
		defaultHeaders = append(defaultHeaders, r.clientHeader.Name)
	}
	if r.clientHeader.Version != "" {
		defaultHeaders = append(defaultHeaders, r.clientHeader.Version)
	}

	if r.tlsConfig != nil && r.tlsConfig.Enabled {
		r.baseURL = fmt.Sprintf("https://%s", r.listenAddr)
	} else {
		r.baseURL = fmt.Sprintf("http://%s", r.listenAddr)
	}

	if r.tlsConfig != nil && r.tlsConfig.Enabled {
		if r.tlsConfig.CertFile == "" {
			return nil, errors.New("tls cert file not provided")
		}

		if r.tlsConfig.KeyFile == "" {
			return nil, errors.New("tls key file not provided")
		}

		var caCertPool *x509.CertPool
		clientAuthMode := tls.NoClientCert

		if r.tlsConfig.ClientAuth != nil && r.tlsConfig.ClientAuth.CertFile != "" {
			caCert, err := os.ReadFile(r.tlsConfig.ClientAuth.CertFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read cert file: %w", err)
			}

			// Create a CA an empty cert pool and add the CA cert to it to serve as authority to validate client certs
			caPool := x509.NewCertPool()
			if ok := caPool.AppendCertsFromPEM(caCert); !ok {
				return nil, errors.New("failed to append cert to pool")
			}
			caCertPool = caPool

			if r.tlsConfig.ClientAuth.Required {
				clientAuthMode = tls.RequireAndVerifyClientCert
			} else {
				clientAuthMode = tls.VerifyClientCertIfGiven
			}

			r.logger.Debug("Client auth enabled", zap.String("mode", clientAuthMode.String()))
		}

		// Load the server cert and private key
		cer, err := tls.LoadX509KeyPair(r.tlsConfig.CertFile, r.tlsConfig.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load tls cert and key: %w", err)
		}

		r.tlsServerConfig = &tls.Config{
			ClientCAs:    caCertPool,
			Certificates: []tls.Certificate{cer},
			ClientAuth:   clientAuthMode,
		}
	}

	if r.developmentMode {
		r.logger.Warn("Development mode enabled. This should only be used for testing purposes")
	}

	if r.healthcheck == nil {
		r.healthcheck = health.New(&health.Options{
			Logger: r.logger,
		})
	}

	if !r.engineExecutionConfiguration.EnableNetPoll {
		r.logger.Warn("Net poller is disabled by configuration. Falling back to less efficient connection handling method.")
	} else if err := netpoll.Supported(); err != nil {
		// Disable netPoll if it's not supported. This flag is used everywhere to decide whether to use netPoll or not.
		r.engineExecutionConfiguration.EnableNetPoll = false
		if errors.Is(err, netpoll.ErrUnsupported) {
			r.logger.Warn(
				"Net poller is only available on Linux and MacOS. Falling back to less efficient connection handling method.",
				zap.Error(err),
			)
		} else {
			r.logger.Warn(
				"Net poller is not functional by the environment. Ensure that the system supports epoll/kqueue and that necessary syscall permissions are granted. Falling back to less efficient connection handling method.",
				zap.Error(err),
			)
		}
	}

	if r.hostName == "" {
		r.hostName, err = os.Hostname()
		if err != nil {
			r.logger.Warn("Failed to get hostname", zap.Error(err))
		}
	}

	return r, nil
}

// newGraphServer creates a new server.
func (r *Router) newServer(ctx context.Context, cfg *nodev1.RouterConfig) error {
	server, err := newGraphServer(ctx, r, cfg)
	if err != nil {
		r.logger.Error("Failed to create graph server. Keeping the old server", zap.Error(err))
		return err
	}

	r.httpServer.SwapGraphServer(ctx, server)

	return nil
}

func (r *Router) listenAndServe() error {
	go func() {
		// Mark the server as not ready when the server is stopped
		defer r.httpServer.healthcheck.SetReady(false)

		// This is a blocking call
		if err := r.httpServer.listenAndServe(); err != nil {
			r.logger.Error("Failed to start new server", zap.Error(err))
		}
	}()

	return nil
}

func (r *Router) initModules(ctx context.Context) error {
	moduleList := make([]ModuleInfo, 0, len(modules)+len(r.customModules))

	for _, module := range modules {
		moduleList = append(moduleList, module)
	}

	for _, module := range r.customModules {
		moduleList = append(moduleList, module.Module())
	}

	moduleList = sortModules(moduleList)

	for _, moduleInfo := range moduleList {
		now := time.Now()

		moduleInstance := moduleInfo.New()

		mc := &ModuleContext{
			Context: ctx,
			Module:  moduleInstance,
			Logger:  r.logger.With(zap.String("module", string(moduleInfo.ID))),
		}

		moduleConfig, ok := r.modulesConfig[string(moduleInfo.ID)]
		if ok {
			if err := mapstructure.Decode(moduleConfig, &moduleInstance); err != nil {
				return fmt.Errorf("failed to decode module config from module %s: %w", moduleInfo.ID, err)
			}
		} else {
			r.logger.Debug("No config found for module", zap.String("id", string(moduleInfo.ID)))
		}

		if fn, ok := moduleInstance.(Provisioner); ok {
			if err := fn.Provision(mc); err != nil {
				return fmt.Errorf("failed to provision module '%s': %w", moduleInfo.ID, err)
			}
		}

		if fn, ok := moduleInstance.(RouterMiddlewareHandler); ok {
			r.routerMiddlewares = append(r.routerMiddlewares, func(handler http.Handler) http.Handler {
				return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					reqContext := getRequestContext(request.Context())
					// Ensure we work with latest request in the chain to work with the right context
					reqContext.request = request
					fn.Middleware(reqContext, handler)
				})
			})
		}

		if fn, ok := moduleInstance.(RouterOnRequestHandler); ok {
			r.routerOnRequestHandlers = append(r.routerOnRequestHandlers, func(handler http.Handler) http.Handler {
				return http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
					reqContext := getRequestContext(request.Context())
					// Ensure we work with latest request in the chain to work with the right context
					reqContext.request = request
					fn.RouterOnRequest(reqContext, handler)
				})
			})
		}

		if handler, ok := moduleInstance.(EnginePreOriginHandler); ok {
			r.preOriginHandlers = append(r.preOriginHandlers, handler.OnOriginRequest)
		}

		if handler, ok := moduleInstance.(EnginePostOriginHandler); ok {
			r.postOriginHandlers = append(r.postOriginHandlers, handler.OnOriginResponse)
		}

		r.modules = append(r.modules, moduleInstance)

		r.logger.Info("Module registered",
			zap.String("id", string(moduleInfo.ID)),
			zap.String("duration", time.Since(now).String()),
		)
	}

	return nil
}

func (r *Router) BaseURL() string {
	return r.baseURL
}

// NewServer prepares a new server instance but does not start it. The method should only be used when you want to bootstrap
// the server manually otherwise you can use Router.Start(). You're responsible for setting health checks status to ready with Server.HealthChecks().
// The server can be shutdown with Router.Shutdown(). Use core.WithExecutionConfig to pass the initial config otherwise the Router will
// try to fetch the config from the control plane. You can swap the router config by using Router.newGraphServer().
func (r *Router) NewServer(ctx context.Context) (Server, error) {
	if r.shutdown.Load() {
		return nil, fmt.Errorf("router is shutdown. Create a new instance with router.NewRouter()")
	}

	if err := r.bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("failed to bootstrap application: %w", err)
	}

	r.httpServer = newServer(&httpServerOptions{
		addr:               r.listenAddr,
		logger:             r.logger,
		tlsConfig:          r.tlsConfig,
		tlsServerConfig:    r.tlsServerConfig,
		healthcheck:        r.healthcheck,
		baseURL:            r.baseURL,
		maxHeaderBytes:     int(r.routerTrafficConfig.MaxHeaderBytes.Uint64()),
		livenessCheckPath:  r.livenessCheckPath,
		readinessCheckPath: r.readinessCheckPath,
		healthCheckPath:    r.healthCheckPath,
	})

	if r.staticExecutionConfig == nil {
		return nil, fmt.Errorf("server only works with static execution configuration")
	}

	return r.httpServer, r.newServer(ctx, r.staticExecutionConfig)
}

// bootstrap initializes the Router. It is called by Start() and NewServer().
// It should only be called once for a Router instance.
func (r *Router) bootstrap(ctx context.Context) error {
	if !r.bootstrapped.CompareAndSwap(false, true) {
		return fmt.Errorf("router is already bootstrapped")
	}

	if r.engineExecutionConfiguration.Debug.ReportMemoryUsage {
		debug.ReportMemoryUsage(ctx, r.logger)
	}

	if r.playgroundConfig.Enabled {
		playgroundUrl, err := url.JoinPath(r.baseURL, r.playgroundConfig.Path)
		if err != nil {
			return fmt.Errorf("failed to join playground url: %w", err)
		}
		r.logger.Info("Serving GraphQL playground", zap.String("url", playgroundUrl))
		r.playgroundHandler = graphiql.NewPlayground(&graphiql.PlaygroundOptions{
			Html:             graphiql.PlaygroundHTML(),
			GraphqlURL:       r.graphqlWebURL,
			PlaygroundPath:   r.playgroundPath,
			ConcurrencyLimit: int64(r.playgroundConfig.ConcurrencyLimit),
		})
	}

	if r.executionConfig != nil && r.executionConfig.Path != "" {
		executionConfig, err := execution_config.FromFile(r.executionConfig.Path)
		if err != nil {
			return fmt.Errorf("failed to read execution config: %w", err)
		}
		r.staticExecutionConfig = executionConfig
	}

	// Modules are only initialized once and not on every config change
	if err := r.initModules(ctx); err != nil {
		return fmt.Errorf("failed to init user modules: %w", err)
	}

	return nil
}

// Start starts the router. It does block until the router has been initialized. After that the server is listening
// on a separate goroutine. The server can be shutdown with Router.Shutdown(). Not safe for concurrent use.
// During initialization, the router will register itself with the control plane and poll the config from the CDN
// if the user opted in to connect to Cosmo Cloud.
func (r *Router) Start(ctx context.Context) error {
	if r.shutdown.Load() {
		return fmt.Errorf("router is shutdown. Create a new instance with router.NewRouter()")
	}

	if err := r.bootstrap(ctx); err != nil {
		return fmt.Errorf("failed to bootstrap router: %w", err)
	}

	r.httpServer = newServer(&httpServerOptions{
		addr:               r.listenAddr,
		logger:             r.logger,
		tlsConfig:          r.tlsConfig,
		tlsServerConfig:    r.tlsServerConfig,
		healthcheck:        r.healthcheck,
		baseURL:            r.baseURL,
		maxHeaderBytes:     int(r.routerTrafficConfig.MaxHeaderBytes.Uint64()),
		livenessCheckPath:  r.livenessCheckPath,
		readinessCheckPath: r.readinessCheckPath,
		healthCheckPath:    r.healthCheckPath,
	})

	// Start the server with the static config without polling
	if r.staticExecutionConfig == nil {
		r.logger.Error("Server only works with static execution configuration")
	}
	if err := r.listenAndServe(); err != nil {
		return err
	}

	if err := r.newServer(ctx, r.staticExecutionConfig); err != nil {
		return err
	}

	defer func() {
		r.httpServer.healthcheck.SetReady(true)

		r.logger.Info("Server initialized and ready to serve requests",
			zap.String("listen_addr", r.listenAddr),
			zap.Bool("playground", r.playgroundConfig.Enabled),
			zap.Bool("introspection", r.introspection),
			zap.String("config_version", r.staticExecutionConfig.Version),
		)
	}()

	if r.executionConfig != nil && r.executionConfig.Watch {
		w, err := watcher.New(watcher.Options{
			Logger:   r.logger.With(zap.String("watcher_label", "execution_config")),
			Path:     r.executionConfig.Path,
			Interval: r.executionConfig.WatchInterval,
			Callback: func() {
				if r.shutdown.Load() {
					r.logger.Warn("Router is in shutdown state. Skipping config update")
					return
				}

				data, err := os.ReadFile(r.executionConfig.Path)
				if err != nil {
					r.logger.Error("Failed to read config file", zap.Error(err))
					return
				}

				r.logger.Info("Config file changed. Updating server with new config", zap.String("path", r.executionConfig.Path))

				cfg, err := execution_config.UnmarshalConfig(data)
				if err != nil {
					r.logger.Error("Failed to unmarshal config file", zap.Error(err))
					return
				}

				if err := r.newServer(ctx, cfg); err != nil {
					r.logger.Error("Failed to update server with new config", zap.Error(err))
					return
				}
			},
		})

		if err != nil {
			return fmt.Errorf("failed to create watcher: %w", err)
		}

		go func() {
			if err := w(ctx); err != nil {
				r.logger.Error("Error watching execution config", zap.Error(err))
				return
			}
		}()

		r.logger.Info("Watching config file for changes. Router will hot-reload automatically without downtime",
			zap.String("path", r.executionConfig.Path),
		)

		return nil
	}

	return nil
}

// Shutdown gracefully shuts down the router. It blocks until the server is shutdown.
// If the router is already shutdown, the method returns immediately without error.
func (r *Router) Shutdown(ctx context.Context) (err error) {
	if !r.shutdown.CompareAndSwap(false, true) {
		return nil
	}

	// Respect grace period
	if r.routerGracePeriod > 0 {
		ctxWithTimer, cancel := context.WithTimeout(ctx, r.routerGracePeriod)
		defer cancel()

		ctx = ctxWithTimer
	}

	if r.httpServer != nil {
		if subErr := r.httpServer.Shutdown(ctx); subErr != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				r.logger.Warn(
					"Shutdown deadline exceeded. Router took too long to shutdown. Consider increasing the grace period",
					zap.Duration("grace_period", r.routerGracePeriod),
				)
			}
			err = errors.Join(err, fmt.Errorf("failed to shutdown router: %w", subErr))
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		for _, module := range r.modules {
			if cleaner, ok := module.(Cleaner); ok {
				if subErr := cleaner.Cleanup(); subErr != nil {
					err = errors.Join(err, fmt.Errorf("failed to clean module %s: %w", module.Module().ID, subErr))
				}
			}
		}
	}()

	wg.Wait()

	return err
}

func WithListenerAddr(addr string) Option {
	return func(r *Router) {
		r.listenAddr = addr
	}
}

func WithLogger(logger *zap.Logger) Option {
	return func(r *Router) {
		r.logger = logger
	}
}

func WithPlayground(enable bool) Option {
	return func(r *Router) {
		r.playground = enable
	}
}

func WithIntrospection(enable bool) Option {
	return func(r *Router) {
		r.introspection = enable
	}
}

func WithQueryPlans(enabled bool) Option {
	return func(r *Router) {
		r.queryPlansEnabled = enabled
	}
}

// WithMultipartHeartbeatInterval sets the interval for the engine to send heartbeats for multipart subscriptions.
func WithMultipartHeartbeatInterval(interval time.Duration) Option {
	return func(r *Router) {
		r.multipartHeartbeatInterval = interval
	}
}

// WithGraphQLPath sets the path where the GraphQL endpoint is served.
func WithGraphQLPath(p string) Option {
	return func(r *Router) {
		r.graphqlPath = p
	}
}

// WithPlaygroundPath sets the path where the GraphQL Playground is served.
func WithPlaygroundPath(p string) Option {
	return func(r *Router) {
		r.playgroundPath = p
	}
}

// WithPlaygroundConfig sets the path where the GraphQL Playground is served.
func WithPlaygroundConfig(c config.PlaygroundConfig) Option {
	return func(r *Router) {
		r.playgroundConfig = c
	}
}

// WithGracePeriod sets the grace period for the router to shutdown.
func WithGracePeriod(timeout time.Duration) Option {
	return func(r *Router) {
		r.routerGracePeriod = timeout
	}
}

func WithModulesConfig(config map[string]interface{}) Option {
	return func(r *Router) {
		r.modulesConfig = config
	}
}

func WithExecutionConfig(cfg *ExecutionConfig) Option {
	return func(r *Router) {
		r.executionConfig = cfg
	}
}

// WithStaticExecutionConfig sets the static execution config. This disables polling and file watching.
func WithStaticExecutionConfig(cfg *nodev1.RouterConfig) Option {
	return func(r *Router) {
		r.staticExecutionConfig = cfg
	}
}

func WithHealthCheckPath(path string) Option {
	return func(r *Router) {
		r.healthCheckPath = path
	}
}

func WithReadinessCheckPath(path string) Option {
	return func(r *Router) {
		r.readinessCheckPath = path
	}
}

func WithLivenessCheckPath(path string) Option {
	return func(r *Router) {
		r.livenessCheckPath = path
	}
}

func WithHeaderRules(headers config.HeaderRules) Option {
	return func(r *Router) {
		r.headerRules = &headers
	}
}

func WithCacheControlPolicy(cfg config.CacheControlPolicy) Option {
	return func(r *Router) {
		r.cacheControlPolicy = cfg
	}
}

func WithOverrideRoutingURL(overrideRoutingURL config.OverrideRoutingURLConfiguration) Option {
	return func(r *Router) {
		r.overrideRoutingURLConfiguration = overrideRoutingURL
	}
}

func WithOverrides(overrides config.OverridesConfiguration) Option {
	return func(r *Router) {
		r.overrides = overrides
	}
}

func WithEngineExecutionConfig(cfg config.EngineExecutionConfiguration) Option {
	return func(r *Router) {
		r.engineExecutionConfiguration = cfg
	}
}

func WithCustomModules(modules ...Module) Option {
	return func(r *Router) {
		r.customModules = modules
	}
}

func WithSubgraphTransportOptions(opts *SubgraphTransportOptions) Option {
	return func(r *Router) {
		r.subgraphTransportOptions = opts
	}
}

func WithSubgraphRetryOptions(enabled bool, maxRetryCount int, retryMaxDuration, retryInterval time.Duration) Option {
	return func(r *Router) {
		r.retryOptions = retrytransport.RetryOptions{
			Enabled:       enabled,
			MaxRetryCount: maxRetryCount,
			MaxDuration:   retryMaxDuration,
			Interval:      retryInterval,
		}
	}
}

func WithRouterTrafficConfig(cfg *config.RouterTrafficConfiguration) Option {
	return func(r *Router) {
		r.routerTrafficConfig = cfg
	}
}

func WithAccessController(controller *AccessController) Option {
	return func(r *Router) {
		r.accessController = controller
	}
}

func WithAuthorizationConfig(cfg *config.AuthorizationConfiguration) Option {
	return func(r *Router) {
		r.Config.authorization = cfg
	}
}

func DefaultRouterTrafficConfig() *config.RouterTrafficConfiguration {
	return &config.RouterTrafficConfiguration{
		MaxRequestBodyBytes: 1000 * 1000 * 5, // 5 MB
	}
}

func NewTransportRequestOptions(cfg config.GlobalSubgraphRequestRule) *TransportRequestOptions {
	defaults := DefaultTransportRequestOptions()

	return &TransportRequestOptions{
		RequestTimeout:         or(cfg.RequestTimeout, defaults.RequestTimeout),
		TLSHandshakeTimeout:    or(cfg.TLSHandshakeTimeout, defaults.TLSHandshakeTimeout),
		ResponseHeaderTimeout:  or(cfg.ResponseHeaderTimeout, defaults.ResponseHeaderTimeout),
		ExpectContinueTimeout:  or(cfg.ExpectContinueTimeout, defaults.ExpectContinueTimeout),
		KeepAliveProbeInterval: or(cfg.KeepAliveProbeInterval, defaults.KeepAliveProbeInterval),
		KeepAliveIdleTimeout:   or(cfg.KeepAliveIdleTimeout, defaults.KeepAliveIdleTimeout),
		DialTimeout:            or(cfg.DialTimeout, defaults.DialTimeout),
		MaxConnsPerHost:        or(cfg.MaxConnsPerHost, defaults.MaxConnsPerHost),
		MaxIdleConns:           or(cfg.MaxIdleConns, defaults.MaxIdleConns),
		MaxIdleConnsPerHost:    or(cfg.MaxIdleConnsPerHost, defaults.MaxIdleConnsPerHost),
	}
}

func DefaultTransportRequestOptions() *TransportRequestOptions {
	return &TransportRequestOptions{
		RequestTimeout:         60 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  0 * time.Second,
		ExpectContinueTimeout:  0 * time.Second,
		KeepAliveProbeInterval: 30 * time.Second,
		KeepAliveIdleTimeout:   0 * time.Second,
		DialTimeout:            30 * time.Second,

		MaxConnsPerHost:     100,
		MaxIdleConns:        1024,
		MaxIdleConnsPerHost: 20,
	}
}

func NewSubgraphTransportOptions(cfg config.TrafficShapingRules) *SubgraphTransportOptions {
	base := &SubgraphTransportOptions{
		TransportRequestOptions: NewTransportRequestOptions(cfg.All),
		SubgraphMap:             map[string]*TransportRequestOptions{},
	}

	for k, v := range cfg.Subgraphs {
		base.SubgraphMap[k] = NewTransportRequestOptions(*v)
	}

	return base
}

func DefaultSubgraphTransportOptions() *SubgraphTransportOptions {
	return &SubgraphTransportOptions{
		TransportRequestOptions: DefaultTransportRequestOptions(),
		SubgraphMap:             map[string]*TransportRequestOptions{},
	}
}

// WithDevelopmentMode enables development mode. This should only be used for testing purposes.
// Development mode allows e.g. to use ART (Advanced Request Tracing) without request signing.
func WithDevelopmentMode(enabled bool) Option {
	return func(r *Router) {
		r.developmentMode = enabled
	}
}

func WithClusterName(name string) Option {
	return func(r *Router) {
		r.clusterName = name
	}
}

func WithInstanceID(id string) Option {
	return func(r *Router) {
		r.instanceID = id
	}
}

func WithConfigVersionHeader(include bool) Option {
	return func(r *Router) {
		r.setConfigVersionHeader = include
	}
}

func WithWebSocketConfiguration(cfg *config.WebSocketConfiguration) Option {
	return func(r *Router) {
		r.Config.webSocketConfiguration = cfg
	}
}

func WithSubgraphErrorPropagation(cfg config.SubgraphErrorPropagationConfiguration) Option {
	return func(r *Router) {
		r.Config.subgraphErrorPropagation = cfg
	}
}

func WithAccessLogs(cfg *AccessLogsConfig) Option {
	return func(r *Router) {
		r.accessLogsConfig = cfg
	}
}

func WithTLSConfig(cfg *TlsConfig) Option {
	return func(r *Router) {
		r.tlsConfig = cfg
	}
}

func WithApolloCompatibilityFlagsConfig(cfg config.ApolloCompatibilityFlags) Option {
	return func(r *Router) {
		if cfg.EnableAll {
			cfg.ValueCompletion.Enabled = true
			cfg.TruncateFloats.Enabled = true
			cfg.SuppressFetchErrors.Enabled = true
			cfg.ReplaceUndefinedOpFieldErrors.Enabled = true
			cfg.ReplaceInvalidVarErrors.Enabled = true
			cfg.ReplaceValidationErrorStatus.Enabled = true
			cfg.SubscriptionMultipartPrintBoundary.Enabled = true
		}
		r.apolloCompatibilityFlags = cfg
	}
}

func WithApolloRouterCompatibilityFlags(cfg config.ApolloRouterCompatibilityFlags) Option {
	return func(r *Router) {
		r.apolloRouterCompatibilityFlags = cfg
	}
}

func WithClientHeader(cfg config.ClientHeader) Option {
	return func(r *Router) {
		r.clientHeader = cfg
	}
}

type ProxyFunc func(req *http.Request) (*url.URL, error)

func newHTTPTransport(opts *TransportRequestOptions) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   opts.DialTimeout,
		KeepAlive: opts.KeepAliveProbeInterval,
	}
	// Great source of inspiration: https://gitlab.com/gitlab-org/gitlab-pages
	// A pages proxy in go that handles tls to upstreams, rate limiting, and more
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		// The defaults value 0 = unbounded.
		// We set to some value to prevent resource exhaustion e.g max requests and ports.
		MaxConnsPerHost: opts.MaxConnsPerHost,
		// The defaults value 0 = unbounded. 100 is used by the default go transport.
		// This value should be significant higher than MaxIdleConnsPerHost.
		MaxIdleConns: opts.MaxIdleConns,
		// The default value is 2. Such a low limit will open and close connections too often.
		// Details: https://gitlab.com/gitlab-org/gitlab-pages/-/merge_requests/274
		MaxIdleConnsPerHost: opts.MaxIdleConnsPerHost,
		ForceAttemptHTTP2:   true,
		IdleConnTimeout:     opts.KeepAliveIdleTimeout,
		// Set more timeouts https://gitlab.com/gitlab-org/gitlab-pages/-/issues/495
		TLSHandshakeTimeout:   opts.TLSHandshakeTimeout,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		ExpectContinueTimeout: opts.ExpectContinueTimeout,
	}
}

func or[T any](maybe *T, or T) T {
	if maybe != nil {
		return *maybe
	}
	return or
}
