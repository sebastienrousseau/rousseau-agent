package github

import "os"

func envToken() string { return os.Getenv(EnvToken) }
