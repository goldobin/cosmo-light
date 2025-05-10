package execution_config

import (
	"bytes"
	"encoding/json"
	"github.com/wundergraph/cosmo/router/internal/rconf"
	"os"
)

// FromFile creates a new router config from the file at the given path.
func FromFile(path string) (*rconf.RouterConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return UnmarshalConfig(data)
}

// UnmarshalConfig deserializes the router config from the given byte slice.
func UnmarshalConfig(config []byte) (*rconf.RouterConfig, error) {
	d := json.NewDecoder(bytes.NewReader(config))
	d.UseNumber()

	var cfg rconf.RouterConfig
	if err := d.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
