module github.com/wundergraph/cosmo/router

go 1.23

require (
	github.com/KimMachineGun/automemlimit v0.6.1
	github.com/MicahParks/jwkset v0.5.19
	github.com/MicahParks/keyfunc/v3 v3.3.5
	github.com/buger/jsonparser v1.1.1
	github.com/caarlos0/env/v11 v11.3.1
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/cloudflare/backoff v0.0.0-20161212185259-647f3cdfc87a
	github.com/dgraph-io/ristretto/v2 v2.1.0
	github.com/dustin/go-humanize v1.0.1
	github.com/expr-lang/expr v1.17.0
	github.com/go-chi/chi/v5 v5.1.0
	github.com/gobwas/ws v1.4.0
	github.com/goccy/go-yaml v1.13.4
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.1
	github.com/hashicorp/go-multierror v1.1.1
	github.com/hashicorp/go-retryablehttp v0.7.7
	github.com/jensneuse/abstractlogger v0.0.4
	github.com/klauspost/compress v1.17.9
	github.com/mitchellh/mapstructure v1.5.0
	github.com/pkg/errors v0.9.1
	github.com/pquerna/cachecontrol v0.2.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.1
	github.com/sebdah/goldie/v2 v2.5.3
	github.com/stretchr/testify v1.10.0
	github.com/tidwall/gjson v1.18.0
	github.com/tidwall/sjson v1.2.5
	github.com/wundergraph/astjson v0.0.0-20250106123708-be463c97e083
	github.com/wundergraph/graphql-go-tools/v2 v2.0.0-rc.166
	go.uber.org/atomic v1.11.0
	go.uber.org/automaxprocs v1.5.3
	go.uber.org/zap v1.27.0
	golang.org/x/sync v0.10.0
	golang.org/x/text v0.21.0
	golang.org/x/time v0.5.0
)

require (
	github.com/99designs/gqlgen v0.17.49 // indirect
	github.com/cilium/ebpf v0.9.1 // indirect
	github.com/containerd/cgroups/v3 v3.0.2 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/jensneuse/byte-template v0.0.0-20231025215717-69252eb3ed56 // indirect
	github.com/kingledion/go-tools v0.6.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/opencontainers/runtime-spec v1.1.0 // indirect
	github.com/pbnjay/memory v0.0.0-20210728143218-7b4eea64cf58 // indirect
	github.com/phf/go-queue v0.0.0-20170504031614-9abe38d0371d // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/r3labs/sse/v2 v2.8.1 // indirect
	github.com/sergi/go-diff v1.3.1 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/vektah/gqlparser/v2 v2.5.16 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.33.0 // indirect
	golang.org/x/sys v0.29.0 // indirect
	google.golang.org/protobuf v1.34.1 // indirect
	gopkg.in/cenkalti/backoff.v1 v1.1.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Remember you can use Go workspaces to avoid using replace directives in multiple go.mod files
// Use what is best for your personal workflow. See CONTRIBUTING.md for more information

// replace github.com/wundergraph/graphql-go-tools/v2 => ../../graphql-go-tools/v2
