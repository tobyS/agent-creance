package doctor

import (
	"encoding/json"
	"fmt"
)

// String renders a Status as a stable lowercase token for the --json surface.
// Kept separate from the int enum so the wire format never exposes the integer
// values (which are an implementation detail of the render ordering).
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	case StatusProblem:
		return "problem"
	case StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// jsonReport is the machine-readable shape of a doctor Report. It is a dedicated
// output struct (not json tags on Report) so the schema is stable and string-
// valued regardless of the internal section types — the same convention as
// internal/policy/render.
type jsonReport struct {
	Prerequisites []jsonPrereq `json:"prerequisites"`
	CA            jsonSection  `json:"ca"`
	Credential    jsonSection  `json:"credential"`
	// MintedCredentials is omitted for a project with no minted credentials, so the
	// existing --json schema is unchanged for every non-minting project (AC-0069a).
	MintedCredentials []jsonMintedCred `json:"minted_credentials,omitempty"`
	Proxy             jsonProxy        `json:"proxy"`
	Exposed           jsonExposed      `json:"exposed_services"`
	Filesystem        jsonFS           `json:"filesystem"`
	// Actionable mirrors Report.Actionable() — the labels that drive the non-zero
	// exit. Always present (empty array, not null, when healthy).
	Actionable []string `json:"actionable"`
}

type jsonPrereq struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Tested    string `json:"tested,omitempty"`
	Skew      string `json:"skew"`
}

type jsonSection struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type jsonMintedCred struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type jsonProxy struct {
	// State is the derived verdict, mirroring renderProxy's precedence:
	// none/cleaned/orphan/stranded/running/down.
	State  string `json:"state"`
	PID    int    `json:"pid,omitempty"`
	Port   int    `json:"port,omitempty"`
	Agents int    `json:"agents"`
}

type jsonExposed struct {
	State     string         `json:"state"`
	Listeners []jsonListener `json:"listeners"`
	Detail    string         `json:"detail,omitempty"`
}

type jsonListener struct {
	Command string `json:"command"`
	PID     int    `json:"pid"`
	Address string `json:"address"`
}

type jsonFS struct {
	State    string          `json:"state"`
	Warnings []jsonFSWarning `json:"warnings"`
}

type jsonFSWarning struct {
	Label  string `json:"label"`
	Path   string `json:"path"`
	FSType string `json:"fs_type"`
	Reason string `json:"reason"`
}

// RenderJSON returns the doctor report as machine-readable JSON for `doctor
// --json`. It serializes the same data the human Render shows; the exit-code
// verdict is unchanged (the caller still acts on Report.Actionable()).
func RenderJSON(r Report) (string, error) {
	out := jsonReport{
		Prerequisites:     make([]jsonPrereq, 0, len(r.Version)),
		CA:                jsonSection{State: r.CA.State.String(), Detail: r.CA.Detail},
		Credential:        jsonSection{State: r.Cred.State.String(), Detail: r.Cred.Detail},
		MintedCredentials: mintedJSON(r.Minted),
		Proxy:             proxyJSON(r.Proxy),
		Exposed:           exposedJSON(r.Exposed),
		Filesystem:        fsJSON(r.FS),
		Actionable:        r.Actionable(),
	}
	if out.Actionable == nil {
		out.Actionable = []string{}
	}
	for _, v := range r.Version {
		out.Prerequisites = append(out.Prerequisites, jsonPrereq{
			Name:      v.Tool.Name,
			Installed: v.Installed,
			Version:   v.Version,
			Tested:    v.Tool.Tested,
			Skew:      v.Skew.String(),
		})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render: marshal doctor report: %w", err)
	}
	return string(data) + "\n", nil
}

// proxyJSON derives the proxy section, mirroring renderProxy's precedence so the
// JSON state word agrees with the human line.
func proxyJSON(sec ProxySection) jsonProxy {
	d := sec.Diag
	out := jsonProxy{PID: d.ProxyPID, Port: d.Port, Agents: len(d.LiveAgents)}
	switch {
	case !d.LockPresent:
		out.State = "none"
		out.PID, out.Port = 0, 0
	case sec.Cleaned != nil && sec.Cleaned.Cleaned:
		out.State = "cleaned"
	case d.Orphan:
		out.State = "orphan"
	case d.Stranded:
		out.State = "stranded"
	case d.BrokerDown:
		out.State = "broker-down"
	case d.ProxyUp:
		out.State = "running"
	default:
		out.State = "down"
	}
	return out
}

// mintedJSON returns nil for a project with no minted credentials, so the omitempty
// field drops out and existing --json output is unchanged.
func mintedJSON(sec MintedCredSection) []jsonMintedCred {
	if len(sec.Creds) == 0 {
		return nil
	}
	out := make([]jsonMintedCred, 0, len(sec.Creds))
	for _, mc := range sec.Creds {
		out = append(out, jsonMintedCred{Name: mc.Name, Kind: mc.Kind, State: mc.State.String(), Detail: mc.Detail})
	}
	return out
}

func exposedJSON(sec ExposedSection) jsonExposed {
	out := jsonExposed{
		State:     sec.State.String(),
		Listeners: make([]jsonListener, 0, len(sec.Listeners)),
		Detail:    sec.Detail,
	}
	for _, l := range sec.Listeners {
		out.Listeners = append(out.Listeners, jsonListener{
			Command: l.Command,
			PID:     l.PID,
			Address: l.Address,
		})
	}
	return out
}

func fsJSON(sec FSSection) jsonFS {
	out := jsonFS{
		State:    sec.State.String(),
		Warnings: make([]jsonFSWarning, 0, len(sec.Warnings)),
	}
	for _, w := range sec.Warnings {
		out.Warnings = append(out.Warnings, jsonFSWarning{
			Label:  w.Label,
			Path:   w.Path,
			FSType: w.FSType,
			Reason: w.Reason,
		})
	}
	return out
}
