package health

import (
	"fmt"

	"go.opentelemetry.io/otel/trace"
)

// Option is the health-container options type
type Option func(*Health) error

// WithChecks adds checks to newly instantiated health-container
func WithChecks(checks ...Config) Option {
	return func(h *Health) error {
		for _, c := range checks {
			if err := h.Register(c); err != nil {
				return fmt.Errorf("could not register check %q: %w", c.Name, err)
			}
		}

		return nil
	}
}

// WithTracerProvider sets trace provider for the checks and instrumentation name that will be used
// for tracer from trace provider.
func WithTracerProvider(tp trace.TracerProvider, instrumentationName string) Option {
	return func(h *Health) error {
		h.tp = tp
		h.instrumentationName = instrumentationName

		return nil
	}
}

// WithComponent sets the component description of the component to which this check refer
func WithComponent(component Component) Option {
	return func(h *Health) error {
		h.component = component

		return nil
	}
}

// WithMaxConcurrent sets max number of concurrently running checks.
// Set to 1 if want to run all checks sequentially.
func WithMaxConcurrent(n int) Option {
	return func(h *Health) error {
		h.maxConcurrent = n
		return nil
	}
}

// WithSystemInfo enables the option to return system information about the go process.
func WithSystemInfo() Option {
	return func(h *Health) error {
		h.systemInfoEnabled = true
		return nil
	}
}
