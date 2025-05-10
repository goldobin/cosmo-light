// Package loadvariable implements helper functions for retrieving values from wgpb.ConfigurationVariable
// instances. If the TS side uses InputVariable<T> (e.g. InputVariable<number> or InputVariable<boolean>)
// then the error messages returned by these functions can be used as is. This is because the only way
// to provide an invalid value would be through an environment variable (a hardcoded or default would come
// from a number or a boolean from the TS side and converted to string internally), and we can retrieve
// the environment variable name from the ConfigurationVariable and include it in the error message.
package config

import (
	"github.com/wundergraph/cosmo/router/internal/rconf"
	"os"
)

// LoadStringVariable is a shorthand for LookupStringVariable when you do not care about
// the value being explicitly set
func LoadStringVariable(variable *rconf.ConfigurationVariable) string {
	return LookupStringVariable(variable)
}

// LookupStringVariable returns the value for the given configuration variable as well
// as whether it was explicitly set. If the variable is nil or the environment
// variable it references is not set, it returns false as its second value.
// Otherwise, (e.g. environment variable set but empty, static string), the
// second return value is true. If you don't need to know if the variable
// was explicitly set, use LoadStringVariable.
func LookupStringVariable(variable *rconf.ConfigurationVariable) string {
	if variable == nil {
		return ""
	}
	switch variable.Kind {
	case rconf.ConfigurationVariableKind_ENV_CONFIGURATION_VARIABLE:
		if varName := variable.EnvironmentVariableName; varName != "" {
			value, found := os.LookupEnv(variable.EnvironmentVariableName)
			if found {
				return value
			}
		}
		defValue := variable.EnvironmentVariableDefaultValue
		return defValue
	default:
		return variable.StaticVariableContent
	}
}
