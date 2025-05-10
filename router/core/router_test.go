package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/wundergraph/cosmo/router/pkg/config"
)

func TestTrafficShapingRules(t *testing.T) {
	allRequestTimeout := 10 * time.Second
	allDialTimeout := 0 * time.Second
	subgraphRequestTimeout := 15 * time.Second
	subgraphDialTimeout := 0 * time.Second

	defaults := DefaultTransportRequestOptions()

	config := config.TrafficShapingRules{
		All: config.GlobalSubgraphRequestRule{
			RequestTimeout: &allRequestTimeout,
			DialTimeout:    &allDialTimeout,
		},
		Subgraphs: map[string]*config.GlobalSubgraphRequestRule{
			"some-subgraph": {
				RequestTimeout: &subgraphRequestTimeout,
				DialTimeout:    &subgraphDialTimeout,
			},
		},
	}

	options := []Option{
		WithSubgraphTransportOptions(NewSubgraphTransportOptions(config)),
	}
	router, err := NewRouter(options...)
	assert.Nil(t, err)

	// Assert that configs are loaded for real, zero and absent values.
	assert.Equal(t, allRequestTimeout, router.subgraphTransportOptions.RequestTimeout)
	assert.Equal(t, allDialTimeout, router.subgraphTransportOptions.DialTimeout)
	assert.Equal(t, defaults.MaxIdleConns, router.subgraphTransportOptions.MaxIdleConns)

	assert.Equal(t, subgraphRequestTimeout, router.subgraphTransportOptions.SubgraphMap["some-subgraph"].RequestTimeout)
	assert.Equal(t, subgraphDialTimeout, router.subgraphTransportOptions.SubgraphMap["some-subgraph"].DialTimeout)
	assert.Equal(t, defaults.MaxIdleConns, router.subgraphTransportOptions.SubgraphMap["some-subgraph"].MaxIdleConns)
}

// Confirms that defaults and fallthrough works properly
func TestNewTransportRequestOptions(t *testing.T) {
	defaults := DefaultTransportRequestOptions()

	subgraphRequestTimeout := 10 * time.Second
	subgraphDialTimeout := 0 * time.Second
	subgraphConfig := &config.GlobalSubgraphRequestRule{
		RequestTimeout: &subgraphRequestTimeout,
		DialTimeout:    &subgraphDialTimeout,
	}

	// Test that the defaults are set properly
	transportCfg := NewTransportRequestOptions(*subgraphConfig)

	// The two set values are preserved, including the manually specified zero
	assert.Equal(t, subgraphRequestTimeout, transportCfg.RequestTimeout)
	assert.Equal(t, subgraphDialTimeout, transportCfg.DialTimeout)

	// The rest of the values are set to the defaults
	assert.Equal(t, defaults.MaxIdleConns, transportCfg.MaxIdleConns)
	assert.Equal(t, defaults.MaxIdleConnsPerHost, transportCfg.MaxIdleConnsPerHost)
}
