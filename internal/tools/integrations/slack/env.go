package slack

import "os"

func envToken() string { return os.Getenv(EnvToken) }
