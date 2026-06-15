package cli

// init_imports.go holds init's optional first-run import steps (AC-0051): seeding
// the project config from the project's Claude Code settings (allowed web domains
// and MCP servers) and from static dev-port detection, plus the end-of-init agent
// prompt. Each step is an independent y/N gate and is only reached on an
// interactive terminal (runInit gates the whole thing on Terminal.IsInteractive),
// so a non-interactive run scaffolds exactly as before.

import (
	"fmt"

	"github.com/tobyS/agent-creance/internal/claudeimport"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/portscan"
)

// gatherImports offers each import step and returns the accepted allow rules and
// host_services (each deduplicated). A step is only offered when its source yields
// candidates, so the engineer is never prompted for nothing. Warnings from
// unreadable/malformed source files are printed but never abort init.
func gatherImports(app *App, dir string) ([]config.Rule, []config.HostService, error) {
	var (
		allow []config.Rule
		ports []config.HostService
	)

	res, warns := claudeimport.Project(app.FS, app.Paths, dir)
	printWarnings(app, warns)

	// Step 1: allowed web domains (WebFetch + sandbox), GET-only intercept.
	if len(res.WebRules) > 0 {
		ok, err := confirm(app, fmt.Sprintf("Import %d allowed web domain(s) from .claude settings?", len(res.WebRules)))
		if err != nil {
			return nil, nil, fmt.Errorf("read confirmation: %w", err)
		}
		if ok {
			allow = append(allow, res.WebRules...)
		}
	}

	// Step 2: MCP servers — remote hosts (passthrough) and any localhost ports,
	// imported together since both come from the MCP config.
	if len(res.MCPRules) > 0 || len(res.Ports) > 0 {
		ok, err := confirm(app, fmt.Sprintf("Import %d MCP server(s) from .claude config?", len(res.MCPRules)+len(res.Ports)))
		if err != nil {
			return nil, nil, fmt.Errorf("read confirmation: %w", err)
		}
		if ok {
			allow = append(allow, res.MCPRules...)
			ports = append(ports, res.Ports...)
		}
	}

	// Step 3: statically detected dev ports.
	detected, pwarns := portscan.Detect(app.FS, dir)
	printWarnings(app, pwarns)
	if len(detected) > 0 {
		ok, err := confirm(app, fmt.Sprintf("Add %d detected dev port(s)?", len(detected)))
		if err != nil {
			return nil, nil, fmt.Errorf("read confirmation: %w", err)
		}
		if ok {
			ports = append(ports, detected...)
		}
	}

	return dedupeRulesByHost(allow), dedupePortsByPort(ports), nil
}

func printWarnings(app *App, warns []string) {
	for _, w := range warns {
		fmt.Fprintf(app.Stdout, "  (skipped: %s)\n", w)
	}
}

// dedupeRulesByHost keeps the first rule per host (web rules are added before MCP
// rules, so a host appearing in both keeps its GET-only intercept form).
func dedupeRulesByHost(rules []config.Rule) []config.Rule {
	seen := map[string]bool{}
	var out []config.Rule
	for _, r := range rules {
		if seen[r.Host] {
			continue
		}
		seen[r.Host] = true
		out = append(out, r)
	}
	return out
}

// dedupePortsByPort keeps the first host service per port (the port is the
// meaningful identity; the label is cosmetic).
func dedupePortsByPort(services []config.HostService) []config.HostService {
	seen := map[int]bool{}
	var out []config.HostService
	for _, hs := range services {
		if seen[hs.Port] {
			continue
		}
		seen[hs.Port] = true
		out = append(out, hs)
	}
	return out
}

// maybeOfferAgentPrompt offers, at the end of an interactive init, to print a
// prompt the engineer can hand to their agent to generate the config that can't be
// inferred statically (stack documentation hosts and any remaining ports).
func maybeOfferAgentPrompt(app *App) {
	if !app.Terminal.IsInteractive() {
		return
	}
	ok, err := confirm(app, "Print a prompt to have your agent suggest more config (ports, docs hosts)?")
	if err != nil || !ok {
		return
	}
	fmt.Fprintln(app.Stdout, msgAgentConfigPrompt)
}

// msgAgentConfigPrompt is the copy-paste prompt for the engineer's agent. It tells
// the agent to write a schema-conforming fragment to a file the engineer then
// reviews and merges with `agent-creance import`.
const msgAgentConfigPrompt = `
----------------------------------------------------------------------
Copy the prompt below to your coding agent. It will inspect this project
and write a config fragment; review that file, then run:
    agent-creance import agent-creance.suggested.yaml
----------------------------------------------------------------------
Analyze this project and produce an agent-creance config fragment so it can run
inside an egress-filtered cage. Write the result to a new file named
"agent-creance.suggested.yaml" in the project root, in this exact YAML shape:

    network:
      host_services:
        - <label>:<port>          # each local port the dev environment listens on
      egress:
        allow:
          - host: <hostname>      # an external host the project's stack needs
            methods: [GET]        # restrict to the methods actually used
            reason: "<why it is needed>"

Rules:
- host_services: list every local port needed for development (app servers,
  databases, caches, message brokers, etc.), each with a short label.
- egress.allow: list external hosts the project's stack needs, especially the
  documentation sites of the databases, frameworks, and services it uses. Prefer
  methods: [GET] for documentation hosts.
- Only include hosts you can justify. Never use a wildcard like "*".
- Output only the YAML file — no prose.

When done, tell me to review agent-creance.suggested.yaml and run
"agent-creance import agent-creance.suggested.yaml".`
