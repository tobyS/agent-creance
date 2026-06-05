package config

import (
	"fmt"
	"strconv"
	"strings"
)

// validate checks cross-field schema constraints, recording every problem on verr
// (it does not stop at the first). Rule-level validation lands in Phase 2.
func (c *Config) validate(verr *ValidationError) {
	_ = verr
}

// parseHostService parses a "label:port" entry into a typed HostService. The label
// is cosmetic but must be non-empty; the port must be a number in 1-65535.
func parseHostService(s string) (HostService, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return HostService{}, fmt.Errorf("host_services entry %q is not in label:port form", s)
	}
	label, portStr := s[:i], s[i+1:]
	if label == "" {
		return HostService{}, fmt.Errorf("host_services entry %q has an empty label", s)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return HostService{}, fmt.Errorf("host_services entry %q has a non-numeric port %q", s, portStr)
	}
	if port < 1 || port > 65535 {
		return HostService{}, fmt.Errorf("host_services entry %q has port %d out of range 1-65535", s, port)
	}
	return HostService{Label: label, Port: port}, nil
}
