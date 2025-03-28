package rconf

import (
	"encoding/json"
	"google.golang.org/protobuf/runtime/protoimpl"
)

type Subgraph struct {
	Id         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	RoutingUrl string `json:"routingUrl,omitempty"`
}

type RouterConfig struct {
	EngineConfig         *EngineConfiguration `json:"engineConfig,omitempty"`
	Version              string               `json:"version,omitempty"`
	Subgraphs            []*Subgraph          `json:"subgraphs,omitempty"`
	CompatibilityVersion string               `json:"compatibilityVersion,omitempty"`
}

type EngineConfiguration struct {
	DefaultFlushInterval     json.Number                `json:"defaultFlushInterval,omitempty"`
	GraphqlSchema            string                     `json:"graphqlSchema,omitempty"`
	StringStorage            map[string]string          `json:"stringStorage,omitempty"`
	GraphqlClientSchema      string                     `json:"graphqlClientSchema,omitempty"`
	TypeConfigurations       []*TypeConfiguration       `json:"typeConfigurations,omitempty"`
	DatasourceConfigurations []*DataSourceConfiguration `json:"datasourceConfigurations,omitempty"`
	FieldConfigurations      []*FieldConfiguration      `json:"fieldConfigurations,omitempty"`
}

type FieldConfiguration struct {
	TypeName                    string                       `json:"typeName,omitempty"`
	FieldName                   string                       `json:"fieldName,omitempty"`
	ArgumentsConfiguration      []*ArgumentConfiguration     `json:"argumentsConfiguration,omitempty"`
	AuthorizationConfiguration  *AuthorizationConfiguration  `json:"authorizationConfiguration,omitempty"`
	SubscriptionFilterCondition *SubscriptionFilterCondition `json:"subscriptionFilterCondition,omitempty"`
}

type ArgumentSource string

const (
	ArgumentSource_OBJECT_FIELD   ArgumentSource = "OBJECT_FIELD"
	ArgumentSource_FIELD_ARGUMENT ArgumentSource = "FIELD_ARGUMENT"
)

type ArgumentConfiguration struct {
	Name       string         `json:"name,omitempty"`
	SourceType ArgumentSource `json:"sourceType,omitempty"`
}

type AuthorizationConfiguration struct {
	RequiresAuthentication bool      `json:"requiresAuthentication,omitempty"`
	RequiredOrScopes       []*Scopes `json:"requiredOrScopes,omitempty"`
}

type Scopes struct {
	RequiredAndScopes []string `json:"requiredAndScopes,omitempty"`
}

type SubscriptionFilterCondition struct {
	And []*SubscriptionFilterCondition `json:"and,omitempty"`
	In  *SubscriptionFieldCondition    `json:"in,omitempty"`
	Not *SubscriptionFilterCondition   `json:"not,omitempty"`
	Or  []*SubscriptionFilterCondition `json:"or,omitempty"`
}

type SubscriptionFieldCondition struct {
	FieldPath []string `json:"fieldPath,omitempty"`
	Json      string   `json:"json,omitempty"`
}

type TypeConfiguration struct {
	TypeName string `json:"typeName,omitempty"`
	RenameTo string `json:"renameTo,omitempty"`
}

type DataSourceKind string

const (
	DataSourceKind_STATIC  DataSourceKind = "STATIC"
	DataSourceKind_GRAPHQL DataSourceKind = "GRAPHQL"
)

type DataSourceConfiguration struct {
	Kind                       DataSourceKind                  `json:"kind,omitempty"`
	RootNodes                  []*TypeField                    `json:"rootNodes,omitempty"`
	ChildNodes                 []*TypeField                    `json:"childNodes,omitempty"`
	OverrideFieldPathFromAlias bool                            `json:"overrideFieldPathFromAlias,omitempty"`
	CustomGraphql              *DataSourceCustom_GraphQL       `json:"customGraphql,omitempty"`
	CustomStatic               *DataSourceCustom_Static        `json:"customStatic,omitempty"`
	Directives                 []*DirectiveConfiguration       `json:"directives,omitempty"`
	RequestTimeoutSeconds      json.Number                     `json:"requestTimeoutSeconds,omitempty"`
	Id                         string                          `json:"id,omitempty"`
	Keys                       []*RequiredField                `json:"keys,omitempty"`
	Provides                   []*RequiredField                `json:"provides,omitempty"`
	Requires                   []*RequiredField                `json:"requires,omitempty"`
	EntityInterfaces           []*EntityInterfaceConfiguration `json:"entityInterfaces,omitempty"`
	InterfaceObjects           []*EntityInterfaceConfiguration `json:"interfaceObjects,omitempty"`
}

type DataSourceCustom_Static struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Data *ConfigurationVariable `json:"data,omitempty"`
}

type ConfigurationVariable struct {
	Kind                            ConfigurationVariableKind `json:"kind,omitempty,default=STATIC_CONFIGURATION_VARIABLE"`
	StaticVariableContent           string                    `json:"staticVariableContent,omitempty"`
	EnvironmentVariableName         string                    `json:"environmentVariableName,omitempty"`
	EnvironmentVariableDefaultValue string                    `json:"environmentVariableDefaultValue,omitempty"`
	PlaceholderVariableName         string                    `json:"placeholderVariableName,omitempty"`
}

type ConfigurationVariableKind string

const (
	ConfigurationVariableKind_STATIC_CONFIGURATION_VARIABLE ConfigurationVariableKind = "STATIC_CONFIGURATION_VARIABLE"
	ConfigurationVariableKind_ENV_CONFIGURATION_VARIABLE    ConfigurationVariableKind = "ENV_CONFIGURATION_VARIABLE"
)

type InternedString struct {
	// key to index into EngineConfiguration.stringStorage
	Key string `json:"key,omitempty"`
}

type TypeField struct {
	TypeName           string   `json:"typeName,omitempty"`
	FieldNames         []string `json:"fieldNames,omitempty"`
	ExternalFieldNames []string `json:"externalFieldNames,omitempty"`
}

type DirectiveConfiguration struct {
	DirectiveName string `json:"directiveName,omitempty"`
	RenameTo      string `json:"renameTo,omitempty"`
}

type RequiredField struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	TypeName              string               `json:"typeName,omitempty"`
	FieldName             string               `json:"fieldName,omitempty"`
	SelectionSet          string               `json:"selectionSet,omitempty"`
	DisableEntityResolver bool                 `json:"disableEntityResolver,omitempty"`
	Conditions            []*FieldSetCondition `json:"conditions,omitempty"`
}

type FieldSetCondition struct {
	FieldCoordinatesPath []*FieldCoordinates `json:"fieldCoordinatesPath,omitempty"`
	FieldPath            []string            `json:"fieldPath,omitempty"`
}

type FieldCoordinates struct {
	FieldName string `json:"fieldName,omitempty"`
	TypeName  string `json:"typeName,omitempty"`
}

type EntityInterfaceConfiguration struct {
	InterfaceTypeName string   `json:"interfaceTypeName,omitempty"`
	ConcreteTypeNames []string `json:"concreteTypeNames,omitempty"`
}

type DataSourceCustom_GraphQL struct {
	Fetch                  *FetchConfiguration               `json:"fetch,omitempty"`
	Subscription           *GraphQLSubscriptionConfiguration `json:"subscription,omitempty"`
	Federation             *GraphQLFederationConfiguration   `json:"federation,omitempty"`
	UpstreamSchema         *InternedString                   `json:"upstreamSchema,omitempty"`
	CustomScalarTypeFields []*SingleTypeField                `json:"customScalarTypeFields,omitempty"`
}

type FetchConfiguration struct {
	// You should either configure url OR a combination of baseURL and path
	// If url resolves to a non empty string, it takes precedence over baseURL and path
	// If url resolves to an empty string, the url will be configured as "{{baseURL}}{{path}}"
	Url    *ConfigurationVariable   `json:"url,omitempty"`
	Method HTTPMethod               `json:"method,omitempty"`
	Header map[string]*HTTPHeader   `json:"header,omitempty"`
	Body   *ConfigurationVariable   `json:"body,omitempty"`
	Query  []*URLQueryConfiguration `json:"query,omitempty"`
	// urlEncodeBody defines whether the body should be URL encoded or not
	// by default, the body will be JSON encoded
	// setting urlEncodeBody to true will render the body empty,
	// the Header Content-Type will be set to application/x-www-form-urlencoded,
	// and the body will be URL encoded and set as the URL Query String
	UrlEncodeBody bool                   `json:"urlEncodeBody,omitempty"`
	Mtls          *MTLSConfiguration     `json:"mtls,omitempty"`
	BaseUrl       *ConfigurationVariable `json:"baseUrl,omitempty"`
	Path          *ConfigurationVariable `json:"path,omitempty"`
	HttpProxyUrl  *ConfigurationVariable `json:"httpProxyUrl,omitempty"`
}

type HTTPMethod string

const (
	HTTPMethod_GET     HTTPMethod = "GET"
	HTTPMethod_POST    HTTPMethod = "POST"
	HTTPMethod_PUT     HTTPMethod = "PUT"
	HTTPMethod_DELETE  HTTPMethod = "DELETE"
	HTTPMethod_OPTIONS HTTPMethod = "OPTIONS"
)

type HTTPHeader struct {
	Values []*ConfigurationVariable `json:"values,omitempty"`
}

type URLQueryConfiguration struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

type MTLSConfiguration struct {
	Key                *ConfigurationVariable `json:"key,omitempty"`
	Cert               *ConfigurationVariable `json:"cert,omitempty"`
	InsecureSkipVerify bool                   `json:"insecureSkipVerify,omitempty"`
}

type GraphQLSubscriptionConfiguration struct {
	Enabled bool                   `json:"enabled,omitempty"`
	Url     *ConfigurationVariable `json:"url,omitempty"`
	// @deprecated - Kept for backwards compatibility when decoding. Use protocol instead.
	UseSSE               *bool                        `json:"useSSE,omitempty"`
	Protocol             *GraphQLSubscriptionProtocol `json:"protocol,omitempty"`
	WebsocketSubprotocol *GraphQLWebsocketSubprotocol `json:"websocketSubprotocol,omitempty"`
}

type GraphQLSubscriptionProtocol string

const (
	// Subscribe with a websocket, automatically negotiating the subprotocol
	GraphQLSubscriptionProtocol_GRAPHQL_SUBSCRIPTION_PROTOCOL_WS GraphQLSubscriptionProtocol = "GRAPHQL_SUBSCRIPTION_PROTOCOL_WS"
	// Subscribe via SSE with a GET request
	GraphQLSubscriptionProtocol_GRAPHQL_SUBSCRIPTION_PROTOCOL_SSE GraphQLSubscriptionProtocol = "GRAPHQL_SUBSCRIPTION_PROTOCOL_SSE"
	// Subscribe via SSE with a POST request
	GraphQLSubscriptionProtocol_GRAPHQL_SUBSCRIPTION_PROTOCOL_SSE_POST GraphQLSubscriptionProtocol = "GRAPHQL_SUBSCRIPTION_PROTOCOL_SSE_POST"
)

type GraphQLWebsocketSubprotocol string

const (
	GraphQLWebsocketSubprotocol_GRAPHQL_WEBSOCKET_SUBPROTOCOL_AUTO         GraphQLWebsocketSubprotocol = "GRAPHQL_WEBSOCKET_SUBPROTOCOL_AUTO"
	GraphQLWebsocketSubprotocol_GRAPHQL_WEBSOCKET_SUBPROTOCOL_WS           GraphQLWebsocketSubprotocol = "GRAPHQL_WEBSOCKET_SUBPROTOCOL_WS"
	GraphQLWebsocketSubprotocol_GRAPHQL_WEBSOCKET_SUBPROTOCOL_TRANSPORT_WS GraphQLWebsocketSubprotocol = "GRAPHQL_WEBSOCKET_SUBPROTOCOL_TRANSPORT_WS"
)

type GraphQLFederationConfiguration struct {
	Enabled    bool   `json:"enabled,omitempty"`
	ServiceSdl string `json:"serviceSdl,omitempty"`
}

type SingleTypeField struct {
	TypeName  string `json:"typeName,omitempty"`
	FieldName string `json:"fieldName,omitempty"`
}
