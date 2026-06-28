package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/tobyS/agent-creance/internal/config"
)

// newIncludeCmd implements `agent-creance include PATH` — append PATH to the project
// config's top-level include: list and recompile so a running cage picks up the merged
// fragment. It is the config-composition counterpart to allow/deny: where those append
// egress rules, this adds a whole config fragment (a shared baseline or layer). Unlike
// allow it has no --once/--global: an include path resolves relative to the file that
// declares it, so writing one into the global config or the out-of-tree session overlay
// would resolve from a surprising base; include therefore always edits the project file
// (AC-0054).
func newIncludeCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "include PATH",
		Short: "Add an include entry to the project config and recompile the policy",
		Long: "Append PATH to the project config's include: list (preserving comments and\n" +
			"formatting) and recompile the policy. PATH may be relative to the project\n" +
			"config's directory, absolute, or ~/-relative. The target is validated before\n" +
			"the entry is written, so a missing or unparseable include is reported up front.",
		Example: "  # Layer in a shared baseline config\n" +
			"  agent-creance include ../shared/agent-creance-base.yaml",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInclude(cmd.Context(), app, ".", args[0])
		},
	}
}

// runInclude is the testable body: dir (the project directory, "." in production) is a
// parameter — not a global — so unit tests can drive it against the sysdep fakes. It
// pre-checks that the include resolves and parses before touching the config, so a bad
// path errors without leaving a half-edited file.
func runInclude(ctx context.Context, app *App, dir, inc string) error {
	path, label, err := mutationTarget(app, dir, false /*once*/, false /*global*/)
	if err != nil {
		return err
	}
	if err := config.NewLoader(app.FS, app.Paths).ValidateInclude(path, inc); err != nil {
		return err
	}
	return applyAndRecompile(ctx, app, dir, path, label, inc, "included",
		func(src []byte) ([]byte, bool, error) { return config.AppendInclude(src, inc) })
}
