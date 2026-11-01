package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the "model-driven context compression" mechanism introduced by norma v0.4.0. As a platform experimental function, users can
// Switch in system settings. It is mutually exclusive with the built-in compaction: noaadapter.Enable is the only entry, once mounted
// The context takeover (Compactor), Compress tool and three resident prompt words will be closed if Enable is not called.
// (Built-in compaction works as usual). The switch is parsed by noaEnabledFn injected by each agent, read every run
// Once, the switch only affects subsequent runs and does not require rebuilding the agent.

// enableNoa Connect noa to opts when parser reporting is enabled. archiveRoot is the persistent base directory for compressing the original text
// (Take the global workDir, and all agents fall under <workDir>/noa and are not scattered with the task/intent directory), sessionID
// Name the archive subdirectory under it (globally unique, so there is no conflict within the same base directory).
//
// noa is an experimental feature: access failure must not interrupt real tasks. When an error occurs, it is reported via onWarn and the built-in compression is rolled back.
// Clear opts.Compaction when enabled successfully to avoid agentcore alarms due to "two context managers are set at the same time".
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa compaction failed to enable; falling back to built-in compaction:" + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but when both coexist, agentcore will alert each time; clear it explicitly.
	opts.Compaction = nil
}
