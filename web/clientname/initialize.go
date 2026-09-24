package clientname

import (
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils/exitinfo"
)

// Settings holds the startup defaults written for the automatic naming
// feature so the values are visible in the admin settings API.
func defaultSettings() map[string]any {
	return map[string]any{
		EnabledKey:                defaultEnabled,
		EchoURLKey:                exitinfo.DefaultEndpoint,
		TimeoutSecondsKey:         defaultTimeoutSeconds,
		DefaultIntervalSecondsKey: DefaultIntervalSeconds,
	}
}

// Initialize installs the naming defaults, creates the resolver, and injects it
// into the clients package. It must run before the HTTP server accepts reports
// and before any deployment profile is generated for a new node.
//
// The returned shutdown function stops the lookup workers.
func Initialize() (func() error, error) {
	if err := config.SetMany(defaultSettings()); err != nil {
		return nil, err
	}
	// Keep the deployment default aligned with the setting.
	clients.DefaultDeploymentIntervalSeconds = DefaultInterval()

	namer := New()
	SetActive(namer)
	clients.SetNodeNamer(namer)
	return namer.Shutdown, nil
}
