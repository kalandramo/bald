package env

import "os"

// lookupEnv is a thin wrapper around os.LookupEnv.
// It is separated into its own file so tests can easily mock it if needed.
func lookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}
