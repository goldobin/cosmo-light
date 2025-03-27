package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/dustin/go-humanize"
	"github.com/wundergraph/cosmo/router/core"
	"github.com/wundergraph/cosmo/router/pkg/authentication"
	"github.com/wundergraph/cosmo/router/pkg/config"
	"github.com/wundergraph/cosmo/router/pkg/logging"
	"go.uber.org/automaxprocs/maxprocs"
	"go.uber.org/zap"
)

// Params are all required for the router to start up
type Params struct {
	Config *config.Config
	Logger *zap.Logger
}

// NewRouter creates a new router instance.
//
// additionalOptions can be used to override default options or options provided in the config.
func NewRouter(ctx context.Context, params Params, additionalOptions ...core.Option) (*core.Router, error) {
	// Automatically set GOMAXPROCS to avoid CPU throttling on containerized environments
	_, err := maxprocs.Set(maxprocs.Logger(params.Logger.Sugar().Debugf))
	if err != nil {
		return nil, fmt.Errorf("could not set max GOMAXPROCS: %w", err)
	}

	if os.Getenv("GOMEMLIMIT") != "" {
		params.Logger.Info("GOMEMLIMIT set by user", zap.String("limit", os.Getenv("GOMEMLIMIT")))
	} else {
		// Automatically set GOMEMLIMIT to 90% of the available memory.
		// This is an effort to prevent the router from being killed by OOM (Out Of Memory)
		// when the system is under memory pressure e.g. when GC is not able to free memory fast enough.
		// More details: https://tip.golang.org/doc/gc-guide#Memory_limit
		mLimit, err := memlimit.SetGoMemLimitWithOpts(
			memlimit.WithRatio(0.9),
			memlimit.WithProvider(memlimit.FromCgroupHybrid),
		)
		if err == nil {
			params.Logger.Info("GOMEMLIMIT set automatically", zap.String("limit", humanize.Bytes(uint64(mLimit))))
		} else if !params.Config.DevelopmentMode {
			params.Logger.Warn("GOMEMLIMIT was not set. Please set it manually to around 90% of the available memory to prevent OOM kills", zap.Error(err))
		}
	}

	cfg := params.Config
	logger := params.Logger

	authenticators, err := setupAuthenticators(ctx, logger, cfg)
	if err != nil {
		return nil, fmt.Errorf("could not setup authenticators: %w", err)
	}

	options := []core.Option{
		core.WithListenerAddr(cfg.ListenAddr),
		core.WithOverrideRoutingURL(cfg.OverrideRoutingURL),
		core.WithOverrides(cfg.Overrides),
		core.WithLogger(logger),
		core.WithIntrospection(cfg.IntrospectionEnabled),
		core.WithQueryPlans(cfg.QueryPlansEnabled),
		core.WithPlayground(cfg.PlaygroundEnabled),
		core.WithApolloCompatibilityFlagsConfig(cfg.ApolloCompatibilityFlags),
		core.WithApolloRouterCompatibilityFlags(cfg.ApolloRouterCompatibilityFlags),
		core.WithGraphQLPath(cfg.GraphQLPath),
		core.WithModulesConfig(cfg.Modules),
		core.WithGracePeriod(cfg.GracePeriod),
		core.WithPlaygroundConfig(cfg.PlaygroundConfig),
		core.WithPlaygroundPath(cfg.PlaygroundPath),
		core.WithHealthCheckPath(cfg.HealthCheckPath),
		core.WithLivenessCheckPath(cfg.LivenessCheckPath),
		core.WithReadinessCheckPath(cfg.ReadinessCheckPath),
		core.WithClusterName(cfg.Cluster.Name),
		core.WithInstanceID(cfg.InstanceID),
		core.WithHeaderRules(cfg.Headers),
		core.WithRouterTrafficConfig(&cfg.TrafficShaping.Router),
		core.WithSubgraphTransportOptions(core.NewSubgraphTransportOptions(cfg.TrafficShaping)),
		core.WithSubgraphRetryOptions(
			cfg.TrafficShaping.All.BackoffJitterRetry.Enabled,
			cfg.TrafficShaping.All.BackoffJitterRetry.MaxAttempts,
			cfg.TrafficShaping.All.BackoffJitterRetry.MaxDuration,
			cfg.TrafficShaping.All.BackoffJitterRetry.Interval,
		),
		core.WithTLSConfig(&core.TlsConfig{
			Enabled:  cfg.TLS.Server.Enabled,
			CertFile: cfg.TLS.Server.CertFile,
			KeyFile:  cfg.TLS.Server.KeyFile,
			ClientAuth: &core.TlsClientAuthConfig{
				CertFile: cfg.TLS.Server.ClientAuth.CertFile,
				Required: cfg.TLS.Server.ClientAuth.Required,
			},
		}),
		core.WithDevelopmentMode(cfg.DevelopmentMode),
		core.WithEngineExecutionConfig(cfg.EngineExecutionConfiguration),
		core.WithCacheControlPolicy(cfg.CacheControl),
		core.WithAuthorizationConfig(&cfg.Authorization),
		core.WithWebSocketConfiguration(&cfg.WebSocket),
		core.WithSubgraphErrorPropagation(cfg.SubgraphErrorPropagation),
		core.WithClientHeader(cfg.ClientHeader),
	}

	options = append(options, additionalOptions...)

	if cfg.AccessLogs.Enabled {
		c := &core.AccessLogsConfig{
			Attributes:         cfg.AccessLogs.Router.Fields,
			SubgraphEnabled:    cfg.AccessLogs.Subgraphs.Enabled,
			SubgraphAttributes: cfg.AccessLogs.Subgraphs.Fields,
		}

		if cfg.AccessLogs.Output.File.Enabled {
			f, err := logging.NewLogFile(cfg.AccessLogs.Output.File.Path)
			if err != nil {
				return nil, fmt.Errorf("could not create log file: %w", err)
			}
			if cfg.AccessLogs.Buffer.Enabled {
				bl, err := logging.NewJSONZapBufferedLogger(logging.BufferedLoggerOptions{
					WS:            f,
					BufferSize:    int(cfg.AccessLogs.Buffer.Size.Uint64()),
					FlushInterval: cfg.AccessLogs.Buffer.FlushInterval,
					Development:   cfg.DevelopmentMode,
					Level:         zap.InfoLevel,
					Pretty:        !cfg.JSONLog,
				})
				if err != nil {
					return nil, fmt.Errorf("could not create buffered logger: %w", err)
				}
				c.Logger = bl.Logger
			} else {
				c.Logger = logging.NewZapAccessLogger(f, cfg.DevelopmentMode, !cfg.JSONLog)
			}
		} else if cfg.AccessLogs.Output.Stdout.Enabled {
			if cfg.AccessLogs.Buffer.Enabled {
				bl, err := logging.NewJSONZapBufferedLogger(logging.BufferedLoggerOptions{
					WS:            os.Stdout,
					BufferSize:    int(cfg.AccessLogs.Buffer.Size.Uint64()),
					FlushInterval: cfg.AccessLogs.Buffer.FlushInterval,
					Development:   cfg.DevelopmentMode,
					Level:         zap.InfoLevel,
					Pretty:        !cfg.JSONLog,
				})
				if err != nil {
					return nil, fmt.Errorf("could not create buffered logger: %w", err)
				}
				c.Logger = bl.Logger
			} else {
				c.Logger = logging.NewZapAccessLogger(os.Stdout, cfg.DevelopmentMode, !cfg.JSONLog)
			}
		}

		options = append(options, core.WithAccessLogs(c))
	}

	executionConfigPath := cfg.ExecutionConfig.File.Path
	if executionConfigPath == "" {
		executionConfigPath = cfg.RouterConfigPath
	}

	if executionConfigPath == "" {
		return nil, fmt.Errorf("config file path should be specified")
	}

	options = append(options, core.WithExecutionConfig(&core.ExecutionConfig{
		Watch:         cfg.ExecutionConfig.File.Watch,
		WatchInterval: cfg.ExecutionConfig.File.WatchInterval,
		Path:          executionConfigPath,
	}))

	if len(authenticators) > 0 {
		options = append(options, core.WithAccessController(core.NewAccessController(authenticators, cfg.Authorization.RequireAuthentication)))
	}

	return core.NewRouter(options...)
}

func setupAuthenticators(ctx context.Context, logger *zap.Logger, cfg *config.Config) ([]authentication.Authenticator, error) {
	jwtConf := cfg.Authentication.JWT
	if len(jwtConf.JWKS) == 0 {
		// No JWT authenticators configured
		return nil, nil
	}

	var authenticators []authentication.Authenticator
	configs := make([]authentication.JWKSConfig, 0, len(jwtConf.JWKS))

	for _, jwks := range cfg.Authentication.JWT.JWKS {
		configs = append(configs, authentication.JWKSConfig{
			URL:               jwks.URL,
			RefreshInterval:   jwks.RefreshInterval,
			AllowedAlgorithms: jwks.Algorithms,
		})
	}

	tokenDecoder, err := authentication.NewJwksTokenDecoder(ctx, logger, configs)
	if err != nil {
		return nil, err
	}

	// create a map for the `httpHeaderAuthenticator`
	headerSourceMap := map[string][]string{
		jwtConf.HeaderName: {jwtConf.HeaderValuePrefix},
	}

	// The `websocketInitialPayloadAuthenticator` has one key and uses a flat list of prefixes
	prefixSet := make(map[string]struct{})

	for _, s := range jwtConf.HeaderSources {
		if s.Type != "header" {
			continue
		}

		for _, prefix := range s.ValuePrefixes {
			headerSourceMap[s.Name] = append(headerSourceMap[s.Name], prefix)
			prefixSet[prefix] = struct{}{}
		}

	}

	opts := authentication.HttpHeaderAuthenticatorOptions{
		Name:                 "jwks",
		HeaderSourcePrefixes: headerSourceMap,
		TokenDecoder:         tokenDecoder,
	}

	authenticator, err := authentication.NewHttpHeaderAuthenticator(opts)
	if err != nil {
		logger.Error("Could not create HttpHeader authenticator", zap.Error(err))
		return nil, err
	}

	authenticators = append(authenticators, authenticator)

	if cfg.WebSocket.Authentication.FromInitialPayload.Enabled {
		headerPrefixes := make([]string, 0, len(prefixSet))
		for prefix := range prefixSet {
			headerPrefixes = append(headerPrefixes, prefix)
		}

		opts := authentication.WebsocketInitialPayloadAuthenticatorOptions{
			TokenDecoder:        tokenDecoder,
			Key:                 cfg.WebSocket.Authentication.FromInitialPayload.Key,
			HeaderValuePrefixes: headerPrefixes,
		}
		authenticator, err = authentication.NewWebsocketInitialPayloadAuthenticator(opts)
		if err != nil {
			logger.Error("Could not create WebsocketInitialPayload authenticator", zap.Error(err))
			return nil, err
		}
		authenticators = append(authenticators, authenticator)
	}

	return authenticators, nil
}
