package status

import (
	"encoding/json"
	"fmt"
)

// jsonReport is the machine-readable shape of a status Report — a dedicated
// output struct (not json tags on Report) so the schema is stable and string-
// valued, the same convention as internal/policy/render and internal/doctor.
type jsonReport struct {
	// Projects is always present (empty array, not null, when no cages).
	Projects []jsonProject `json:"projects"`
}

type jsonProject struct {
	// Project is the display identity: the recorded canonical path, or the
	// state-dir hash when the lock predates path recording.
	Project string `json:"project"`
	Hash    string `json:"hash"`
	// State mirrors the human table: running/orphan/stranded/down.
	State  string `json:"state"`
	Port   int    `json:"port"`
	PID    int    `json:"pid"`
	Agents int    `json:"agents"`
}

// RenderJSON returns the running-cages report as machine-readable JSON for
// `status --json`. The projects array is always present (empty, not null).
func RenderJSON(r Report) (string, error) {
	out := jsonReport{Projects: make([]jsonProject, 0, len(r.Projects))}
	for _, p := range r.Projects {
		out.Projects = append(out.Projects, jsonProject{
			Project: project(p),
			Hash:    p.Hash,
			State:   stateLabel(p.Diag),
			Port:    p.Diag.Port,
			PID:     p.Diag.ProxyPID,
			Agents:  len(p.Diag.LiveAgents),
		})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render: marshal status report: %w", err)
	}
	return string(data) + "\n", nil
}
