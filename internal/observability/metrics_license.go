package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// LicenseValid is 1 while a verified, unexpired licence is loaded
	// and 0 otherwise (core tier, bad signature, expired). An alert on
	// this gauge is how an operator learns that SSO, audit egress and
	// advanced governance switched off over a weekend.
	LicenseValid = factory.NewGauge(prometheus.GaugeOpts{
		Name: "rousseau_license_valid",
		Help: "1 when a verified, unexpired licence is loaded; 0 otherwise.",
	})

	// LicenseExpiresAt is the licence expiry as a Unix timestamp, or
	// 0 when no valid licence is loaded. `rousseau_license_expires_timestamp_seconds - time()`
	// is the renewal runway.
	LicenseExpiresAt = factory.NewGauge(prometheus.GaugeOpts{
		Name: "rousseau_license_expires_timestamp_seconds",
		Help: "Licence expiry as a Unix timestamp; 0 when no valid licence is loaded.",
	})
)

// ObserveLicense publishes the licence state. Called once at startup
// and again on any reload.
func ObserveLicense(valid bool, expiresAt time.Time) {
	if valid {
		LicenseValid.Set(1)
	} else {
		LicenseValid.Set(0)
	}
	if valid && !expiresAt.IsZero() {
		LicenseExpiresAt.Set(float64(expiresAt.Unix()))
		return
	}
	LicenseExpiresAt.Set(0)
}
