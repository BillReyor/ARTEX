package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// Closing prompt (wrap-up / settlement prompt): When the agent runs out of steps (MaxTurns) or
// When [Timeout (run_seconds/MaxDuration)] is terminated, the settlement phase of the SDK will inject this prompt.
// Let the agent first store the content that has been identified but not written back, and then output a summary to avoid unfinished work.
//
// The closing prompt word of each agent can be overwritten on demand in the background (save agents.wrapup_prompt). If left blank, use here
// Built-in default. Only [prompt word text] can be edited; which tools to disable and how many rounds of budgets to end with are code fixed strategies.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default closing prompt words, indexed by agent key. Workers reuse the historically hard-coded settleWrapUpPrompt
// (Defined in worker.go), planner/mainagent each has its own version; the missing one (custom agent) is the universal one.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: The built-in default round budget of each agent's closing stage [self] (can be overridden by background >0).
// Each game is given 10 rounds to ensure that there are enough moves in the final stage. Missed away genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "Your step budget for this planning round is about to run out — note that only [this round] ends; the system will wake you again as the situation changes to continue planning. This is not task termination, so you don't need to wrap up the whole plan here. Land the conclusions you've already worked out this round — don't let this round go to waste — but also [don't pad intents just to wrap up] (0 intents this round is still a perfectly normal result): (1) if you've judged an exploration direction that [should be dispatched now], submit it in one add_intent batch call (don't hold back what you've thought through); (2) for goals proven met by some finding/fact, call prove_goal to mark them met (don't miss any); (3) if you've identified a serial exploitation chain that needs step-by-step dispatch, record it with TodoWrite so the next wake can continue. When done, end this round directly — no summary text needed."

const mainAgentWrapUpDefault = "Your step budget is about to run out and this interaction is ending. Don't start any new exploration/operations. Please **in a single plain-text sentence** summarize the current progress, key conclusions, and suggested next step for the user."

const genericWrapUpDefault = "You are about to be terminated because your budget is exhausted. First write back the completed results that haven't been persisted, then **in a single plain-text sentence** summarize what you did and which key conclusions you reached (this sentence will be displayed as the run's result)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- Task-level timeout closing words (see docs/task-level timeout and closing design.md) ----------
//
// The ending words with per-run are [two sets]: per-run is "your budget for this run has been used up"; task timeout is
// "The entire mission is coming to an end." The semantics are often opposite (especially planner: per-run says "don't stop planning",
// Task timeout says "stop planning and make final judgment when the point is reached"). Only configured for worker/planner.

// WrapupTaskTimeoutOverride / ...TurnsOverride: DB override of task timeout ending word and round number
// (wire to agents.task_timeout_wrapup_prompt/_max_turns, worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not this run's budget — the entire exploration has reached its deadline). This is your last chance: (1) persist [everything] you've identified but not yet written back — new assets via insert_assets, exploration conclusions/facts via record_fact, confirmed findings via report_finding; (2) don't start any new commands/probes; (3) **finally, in a single plain-text sentence**, summarize your key conclusions on this intent."

const plannerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not this round — the entire task is terminating). Based on [all] current facts and findings, make one final goal judgment: for goals proven met by evidence, call prove_goal to mark them met (don't miss any). **Don't generate any new intents** (intents dispatched now won't be executed anyway). Once judged, converge — no summary text needed."

// TaskTimeoutWrapupDefault returns the built-in default ending word of an agent's task timeout (for background placeholder/restore default).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // Not configured (mainagent/chat) returns an empty string
}

// resolveTaskTimeoutWrapup: DB override (non-null) > built-in default. An empty string indicates that the agent has no task timeout word.
// (non-worker/planner), the caller should fallback per-run words at this time.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // The per-run number of rounds is used by default
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
// - clamped=true → This run is clamped by the task deadline: due to Timeout ending = task deadline → task timeout word;
// Because MaxTurns ends = the number of steps in the pinch window is exhausted first, and there are still a few minutes left in the task → fall back to the per-run word.
// - clamped=false → It's still early in the task: both reasons use per-run words (i.e. degenerate into wrapupSettlement).
//
// The PromptByReason given to the harness is selected on-site according to the [actual] reason at the end, and is mismatched when there is no build.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // Back to the bottom (also the values of the two reasons when not clamped)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // Mission accomplished
				harness.ReasonMaxTurns: perRun, // The number of steps is exhausted first, and there is still time left in the task.
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
