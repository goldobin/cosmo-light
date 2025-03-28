package core

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/wundergraph/astjson"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/httpclient"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/graphqlerrors"

	"github.com/wundergraph/cosmo/router/internal/expr"
	"github.com/wundergraph/cosmo/router/pkg/art"
	"github.com/wundergraph/cosmo/router/pkg/config"
)

type PreHandlerOptions struct {
	Logger             *zap.Logger
	Executor           *Executor
	OperationProcessor *OperationProcessor
	Planner            *OperationPlanner
	AccessController   *AccessController
	RouterPublicKey    *ecdsa.PublicKey
	ComplexityLimits   *config.ComplexityLimits

	DevelopmentMode           bool
	AlwaysIncludeQueryPlan    bool
	AlwaysSkipLoader          bool
	QueryPlansEnabled         bool
	QueryPlansLoggingEnabled  bool
	ClientHeader              config.ClientHeader
	ApolloCompatibilityFlags  *config.ApolloCompatibilityFlags
	DisableVariablesRemapping bool
	ExprManager               *expr.Manager
}

type PreHandler struct {
	log                       *zap.Logger
	executor                  *Executor
	operationProcessor        *OperationProcessor
	planner                   *OperationPlanner
	accessController          *AccessController
	developmentMode           bool
	alwaysIncludeQueryPlan    bool
	alwaysSkipLoader          bool
	queryPlansEnabled         bool // queryPlansEnabled is a flag to enable query plans output in the extensions
	queryPlansLoggingEnabled  bool // queryPlansLoggingEnabled is a flag to enable logging of query plans
	routerPublicKey           *ecdsa.PublicKey
	complexityLimits          *config.ComplexityLimits
	clientHeader              config.ClientHeader
	apolloCompatibilityFlags  *config.ApolloCompatibilityFlags
	variableParsePool         astjson.ParserPool
	disableVariablesRemapping bool
	exprManager               *expr.Manager
}

type httpOperation struct {
	requestContext *requestContext
	body           []byte
	files          []*httpclient.FileUpload
	requestLogger  *zap.Logger
	traceTimings   *art.TraceTimings
}

func NewPreHandler(opts *PreHandlerOptions) *PreHandler {
	return &PreHandler{
		log:                       opts.Logger,
		executor:                  opts.Executor,
		operationProcessor:        opts.OperationProcessor,
		planner:                   opts.Planner,
		accessController:          opts.AccessController,
		routerPublicKey:           opts.RouterPublicKey,
		developmentMode:           opts.DevelopmentMode,
		complexityLimits:          opts.ComplexityLimits,
		alwaysIncludeQueryPlan:    opts.AlwaysIncludeQueryPlan,
		alwaysSkipLoader:          opts.AlwaysSkipLoader,
		queryPlansEnabled:         opts.QueryPlansEnabled,
		queryPlansLoggingEnabled:  opts.QueryPlansLoggingEnabled,
		clientHeader:              opts.ClientHeader,
		apolloCompatibilityFlags:  opts.ApolloCompatibilityFlags,
		disableVariablesRemapping: opts.DisableVariablesRemapping,
		exprManager:               opts.ExprManager,
	}
}

func (h *PreHandler) getBodyReadBuffer(preferredSize int64) *bytes.Buffer {
	if preferredSize <= 0 {
		preferredSize = 1024 * 4 // 4KB
	} else if preferredSize > h.operationProcessor.maxOperationSizeInBytes {
		preferredSize = h.operationProcessor.maxOperationSizeInBytes
	}
	return bytes.NewBuffer(make([]byte, 0, preferredSize))
}

// Error and Status Code handling
//
// When a server receives a well-formed GraphQL-over-HTTP request, it must return a
// well‐formed GraphQL response. The server's response describes the result of validating
// and executing the requested operation if successful, and describes any errors encountered
// during the request. This means working errors should be returned as part of the response body.
// That also implies parsing or validation errors. They should be returned as part of the response body.
// Only in cases where the request is malformed or invalid GraphQL should the server return an HTTP 4xx or 5xx error code.
// https://github.com/graphql/graphql-over-http/blob/main/spec/GraphQLOverHTTP.md#response

func (h *PreHandler) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		var (
			traceTimings *art.TraceTimings
		)

		requestContext := getRequestContext(r.Context())
		requestLogger := requestContext.logger
		requestContext.operation = &operationContext{}

		executionOptions, traceOptions, err := h.parseRequestOptions()
		if err != nil {
			requestContext.SetError(err)
			writeRequestErrors(r, w, http.StatusBadRequest, graphqlerrors.RequestErrorsFromError(err), requestLogger)
			return
		}

		requestContext.operation.protocol = OperationProtocolHTTP
		requestContext.operation.executionOptions = executionOptions
		requestContext.operation.traceOptions = traceOptions

		if traceOptions.Enable {
			r = r.WithContext(resolve.SetTraceStart(r.Context(), traceOptions.EnablePredictableDebugTimings))
			traceTimings = art.NewTraceTimings(r.Context())
		}

		var body []byte
		var files []*httpclient.FileUpload

		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			requestContext.SetError(&httpGraphqlError{
				message:    "file upload not supported",
				statusCode: http.StatusOK,
			})
			writeOperationError(r, w, requestLogger, requestContext.error)
			return
		} else if r.Method == http.MethodPost {

			var err error
			body, err = h.operationProcessor.ReadBody(r.Body, h.getBodyReadBuffer(r.ContentLength))
			// We set it before the error so that users could log the body if it exists in case of an error
			if h.exprManager.VisitorManager.IsBodyUsedInExpressions() {
				requestContext.expressionContext.Request.Body.Raw = string(body)
			}
			if err != nil {
				requestContext.SetError(err)

				// Don't produce errors logs here because it can only be client side errors
				// e.g. too large body, slow client, aborted connection etc.
				// The error is logged as debug log in the writeOperationError function

				writeOperationError(r, w, requestLogger, err)
				return
			}
		}

		variablesParser := h.variableParsePool.Get()
		defer h.variableParsePool.Put(variablesParser)

		// If we have authenticators, we try to authenticate the request
		if h.accessController != nil {

			validatedReq, err := h.accessController.Access(w, r)
			if err != nil {
				requestContext.SetError(err)
				requestLogger.Error("Failed to authenticate request", zap.Error(err))

				writeOperationError(r, w, requestLogger, &httpGraphqlError{
					message:    err.Error(),
					statusCode: http.StatusUnauthorized,
				})
				return
			}

			r = validatedReq

			requestContext.expressionContext.Request.Auth = expr.LoadAuth(r.Context())
		}

		err = h.handleOperation(r, variablesParser, &httpOperation{
			requestContext: requestContext,
			requestLogger:  requestLogger,
			traceTimings:   traceTimings,
			files:          files,
			body:           body,
		})
		if err != nil {
			requestContext.SetError(err)
			writeOperationError(r, w, requestLogger, err)
			return
		}

		art.SetRequestTracingStats(r.Context(), traceOptions, traceTimings)

		if traceOptions.Enable {
			reqData := &resolve.RequestData{
				Method:  r.Method,
				URL:     r.URL.String(),
				Headers: r.Header,
				Body: resolve.BodyData{
					Query:         requestContext.operation.rawContent,
					OperationName: requestContext.operation.name,
					Variables:     json.RawMessage(requestContext.operation.variables.String()),
				},
			}
			r = r.WithContext(resolve.SetRequest(r.Context(), reqData))
		}

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		// The request context needs to be updated with the latest request to ensure that the context is up to date
		requestContext.request = r
		requestContext.responseWriter = ww

		// Call the final handler that resolves the operation
		// and enrich the context to make it available in the request context as well for metrics etc.
		next.ServeHTTP(ww, r)
	})
}

func (h *PreHandler) handleOperation(req *http.Request, variablesParser *astjson.Parser, httpOperation *httpOperation) error {
	operationKit, err := h.operationProcessor.NewKit()
	if err != nil {
		return err
	}

	defer func() {
		// the kit must be freed before we're doing io operations
		// the kit is bound to the number of CPUs, and we must not hold onto it while doing IO operations
		// it needs to be called inside a defer to ensure it is called in panic situations as well
		if operationKit != nil {
			operationKit.Free()
		}

	}()

	requestContext := httpOperation.requestContext

	// Handle the case when operation information are provided as GET parameters
	if req.Method == http.MethodGet {
		if err := operationKit.UnmarshalOperationFromURL(req.URL); err != nil {
			return &httpGraphqlError{
				message:    fmt.Sprintf("error parsing request query params: %s", err),
				statusCode: http.StatusBadRequest,
			}
		}
	} else if req.Method == http.MethodPost {
		if err := operationKit.UnmarshalOperationFromBody(httpOperation.body); err != nil {
			return &httpGraphqlError{
				message:    "error parsing request body",
				statusCode: http.StatusBadRequest,
			}
		}
		// If we have files, we need to set them on the parsed operation
		if len(httpOperation.files) > 0 {
			requestContext.operation.files = httpOperation.files
		}
	}

	requestContext.operation.extensions = operationKit.parsedOperation.Request.Extensions
	requestContext.operation.variables, err = variablesParser.ParseBytes(operationKit.parsedOperation.Request.Variables)
	if err != nil {
		return &httpGraphqlError{
			message:    fmt.Sprintf("error parsing variables: %s", err),
			statusCode: http.StatusBadRequest,
		}
	}

	var (
		skipParse bool
	)

	// If the persistent operation is already in the cache, we skip the parse step
	// because the operation was already parsed. This is a performance optimization, and we
	// can do it because we know that the persisted operation is immutable (identified by the hash)
	if !skipParse {
		httpOperation.traceTimings.StartParse()
		startParsing := time.Now()

		err = operationKit.Parse()
		if err != nil {
			requestContext.operation.parsingTime = time.Since(startParsing)
			if !requestContext.operation.traceOptions.ExcludeParseStats {
				httpOperation.traceTimings.EndParse()
			}

			return err
		}

		requestContext.operation.parsingTime = time.Since(startParsing)
		if !requestContext.operation.traceOptions.ExcludeParseStats {
			httpOperation.traceTimings.EndParse()
		}
	}

	requestContext.operation.name = operationKit.parsedOperation.Request.OperationName
	requestContext.operation.opType = operationKit.parsedOperation.Type

	if req.Method == http.MethodGet && operationKit.parsedOperation.Type == "mutation" {
		return &httpGraphqlError{
			message:    "Mutations can only be sent over HTTP POST",
			statusCode: http.StatusMethodNotAllowed,
		}
	}

	/**
	* Normalize the operation
	 */

	if !requestContext.operation.traceOptions.ExcludeNormalizeStats {
		httpOperation.traceTimings.StartNormalize()
	}

	startNormalization := time.Now()
	_, err = operationKit.NormalizeOperation()
	if err != nil {
		requestContext.operation.normalizationTime = time.Since(startNormalization)
		if !requestContext.operation.traceOptions.ExcludeNormalizeStats {
			httpOperation.traceTimings.EndNormalize()
		}

		return err
	}

	requestContext.operation.normalizationCacheHit = operationKit.parsedOperation.NormalizationCacheHit

	/**
	* Normalize the variables
	 */

	// Normalize the variables returns list of uploads mapping if there are any of them present in a query
	// type UploadPathMapping struct {
	// 	VariableName       string - is a variable name holding the direct or nested value of type Upload, example "f"
	// 	OriginalUploadPath string - is a path relative to variables which have an Upload type, example "variables.f"
	// 	NewUploadPath      string - if variable was used in the inline object like this `arg: {f: $f}` this field will hold the new extracted path, example "variables.a.f", if it is an empty, there was no change in the path
	// }
	uploadsMapping, err := operationKit.NormalizeVariables()
	if err != nil {
		requestContext.operation.normalizationTime = time.Since(startNormalization)
		if !requestContext.operation.traceOptions.ExcludeNormalizeStats {
			httpOperation.traceTimings.EndNormalize()
		}

		return err
	}

	// update file uploads path if they were used in nested field in the extracted variables
	for mapping := range slices.Values(uploadsMapping) {
		// if the NewUploadPath is empty it means that there was no change in the path - e.g. upload was directly passed to the argument
		// e.g. field(fileArgument: $file) will result in []UploadPathMapping{ {VariableName: "file", OriginalUploadPath: "variables.file", NewUploadPath: ""} }
		if mapping.NewUploadPath == "" {
			continue
		}

		// look for the corresponding file which was used in the nested argument
		// we are matching original upload path passed via uploads map with the mapping items
		idx := slices.IndexFunc(requestContext.operation.files, func(file *httpclient.FileUpload) bool {
			return file.VariablePath() == mapping.OriginalUploadPath
		})

		if idx == -1 {
			continue
		}

		// if NewUploadPath is not empty the file argument was used in the nested object, and we need to update the path
		// e.g. field(arg: {file: $file}) normalized to field(arg: $a) will result in []UploadPathMapping{ {VariableName: "file", OriginalUploadPath: "variables.file", NewUploadPath: "variables.a.file"} }
		// so "variables.file" should be updated to "variables.a.file"
		requestContext.operation.files[idx].SetVariablePath(uploadsMapping[idx].NewUploadPath)
	}

	// RemapVariables is updating and sort variables name to be able to have them in a predictable order
	// after remapping requestContext.operation.remapVariables map will contain new names as a keys and old names as a values - to be able to extract the old values
	// because it does not rename variables in a variables json
	err = operationKit.RemapVariables(h.disableVariablesRemapping)
	if err != nil {
		requestContext.operation.normalizationTime = time.Since(startNormalization)

		if !requestContext.operation.traceOptions.ExcludeNormalizeStats {
			httpOperation.traceTimings.EndNormalize()
		}

		return err
	}

	requestContext.operation.hash = operationKit.parsedOperation.ID
	requestContext.operation.internalHash = operationKit.parsedOperation.InternalID
	requestContext.operation.remapVariables = operationKit.parsedOperation.RemapVariables

	if !h.disableVariablesRemapping && len(uploadsMapping) > 0 {
		// after variables remapping we need to update the file uploads path because variables relative path has changed
		// but files still references the old uploads locations
		// key `to` is a new variable name
		// value `from` is an old variable name
		// we are looping through remapped variables to find a match between old variable name and variable which was holding an upload
		for to, from := range maps.All(requestContext.operation.remapVariables) {

			// loop over upload mappings to find a match between variable name and upload variable name
			for uploadMapping := range slices.Values(uploadsMapping) {
				if uploadMapping.VariableName != from {
					continue
				}

				uploadPath := uploadMapping.NewUploadPath
				// if NewUploadPath is empty it means that there was no change in the path - e.g. upload was directly passed to the argument
				if uploadPath == "" {
					uploadPath = uploadMapping.OriginalUploadPath
				}

				// next step is to compare file upload path with the original upload path from the upload mappings
				for file := range slices.Values(requestContext.operation.files) {
					if file.VariablePath() != uploadPath {
						continue
					}

					// trim old variable name prefix
					oldUploadPathPrefix := fmt.Sprintf("variables.%s.", from)
					relativeUploadPath := strings.TrimPrefix(uploadPath, oldUploadPathPrefix)

					// set new variable name prefix
					updatedPath := fmt.Sprintf("variables.%s.%s", to, relativeUploadPath)
					file.SetVariablePath(updatedPath)
				}
			}
		}
	}

	requestContext.operation.rawContent = operationKit.parsedOperation.Request.Query
	requestContext.operation.content = operationKit.parsedOperation.NormalizedRepresentation
	requestContext.operation.variables, err = variablesParser.ParseBytes(operationKit.parsedOperation.Request.Variables)
	if err != nil {
		if !requestContext.operation.traceOptions.ExcludeNormalizeStats {
			httpOperation.traceTimings.EndNormalize()
		}
		return err
	}
	requestContext.operation.normalizationTime = time.Since(startNormalization)

	if !requestContext.operation.traceOptions.ExcludeNormalizeStats {
		httpOperation.traceTimings.EndNormalize()
	}

	/**
	* Validate the operation
	 */

	if !requestContext.operation.traceOptions.ExcludeValidateStats {
		httpOperation.traceTimings.StartValidate()
	}

	startValidation := time.Now()
	_, err = operationKit.Validate(requestContext.operation.executionOptions.SkipLoader, requestContext.operation.remapVariables, h.apolloCompatibilityFlags)
	if err != nil {
		requestContext.operation.validationTime = time.Since(startValidation)

		if !requestContext.operation.traceOptions.ExcludeValidateStats {
			httpOperation.traceTimings.EndValidate()
		}

		return err
	}

	// Validate that the planned query doesn't exceed the maximum query depth configured
	// This check runs if they've configured a max query depth, and it can optionally be turned off for persisted operations
	if h.complexityLimits != nil {
		_, _, queryDepthErr := operationKit.ValidateQueryComplexity(h.complexityLimits, operationKit.kit.doc, h.executor.RouterSchema)
		if queryDepthErr != nil {
			requestContext.operation.validationTime = time.Since(startValidation)
			httpOperation.traceTimings.EndValidate()

			return queryDepthErr
		}
	}

	requestContext.operation.validationTime = time.Since(startValidation)
	httpOperation.traceTimings.EndValidate()

	/**
	* Plan the operation
	 */

	// If the request has a query parameter wg_trace=true we skip the cache
	// and always plan the operation
	// this allows us to "write" to the plan
	if !requestContext.operation.traceOptions.ExcludePlannerStats {
		httpOperation.traceTimings.StartPlanning()
	}
	startPlanning := time.Now()
	planOptions := PlanOptions{
		ClientInfo:       requestContext.operation.clientInfo,
		TraceOptions:     requestContext.operation.traceOptions,
		ExecutionOptions: requestContext.operation.executionOptions,
	}

	err = h.planner.plan(requestContext.operation, planOptions)
	if err != nil {

		httpOperation.requestLogger.Error("failed to plan operation", zap.Error(err))

		if !requestContext.operation.traceOptions.ExcludePlannerStats {
			httpOperation.traceTimings.EndPlanning()
		}

		return err
	}

	requestContext.operation.planningTime = time.Since(startPlanning)
	httpOperation.traceTimings.EndPlanning()

	// we could log the query plan only if query plans are calculated
	if (h.queryPlansEnabled && requestContext.operation.executionOptions.IncludeQueryPlanInResponse) ||
		h.alwaysIncludeQueryPlan {

		switch p := requestContext.operation.preparedPlan.preparedPlan.(type) {
		case *plan.SynchronousResponsePlan:
			p.Response.Fetches.NormalizedQuery = operationKit.parsedOperation.NormalizedRepresentation
		}

		if h.queryPlansLoggingEnabled {
			switch p := requestContext.operation.preparedPlan.preparedPlan.(type) {
			case *plan.SynchronousResponsePlan:
				printedPlan := p.Response.Fetches.QueryPlan().PrettyPrint()

				if h.developmentMode {
					h.log.Sugar().Debugf("Query Plan:\n%s", printedPlan)
				} else {
					h.log.Debug("Query Plan", zap.String("query_plan", printedPlan))
				}
			}
		}
	}

	return nil
}

func (h *PreHandler) parseRequestOptions() (resolve.ExecutionOptions, resolve.TraceOptions, error) {
	ex, tr, err := h.internalParseRequestOptions()
	if err != nil {
		return ex, tr, err
	}
	if h.alwaysIncludeQueryPlan {
		ex.IncludeQueryPlanInResponse = true
	}
	if h.alwaysSkipLoader {
		ex.SkipLoader = true
	}
	if !h.queryPlansEnabled {
		ex.IncludeQueryPlanInResponse = false
	}
	return ex, tr, nil
}

func (h *PreHandler) internalParseRequestOptions() (resolve.ExecutionOptions, resolve.TraceOptions, error) {
	// Disable tracing / query plans for all other cases
	traceOptions := resolve.TraceOptions{}
	traceOptions.DisableAll()
	return resolve.ExecutionOptions{
		SkipLoader:                 false,
		IncludeQueryPlanInResponse: false,
	}, traceOptions, nil
}

func (h *PreHandler) parseRequestExecutionOptions(r *http.Request) resolve.ExecutionOptions {
	options := resolve.ExecutionOptions{
		SkipLoader:                 false,
		IncludeQueryPlanInResponse: false,
	}
	if r.Header.Get("X-WG-Skip-Loader") != "" {
		options.SkipLoader = true
	}
	if r.URL.Query().Has("wg_skip_loader") {
		options.SkipLoader = true
	}
	if r.Header.Get("X-WG-Include-Query-Plan") != "" {
		options.IncludeQueryPlanInResponse = true
	}
	if r.URL.Query().Has("wg_include_query_plan") {
		options.IncludeQueryPlanInResponse = true
	}
	return options
}
