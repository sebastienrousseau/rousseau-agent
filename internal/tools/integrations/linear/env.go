package linear

import "os"

func envKey() string { return os.Getenv(EnvToken) }
