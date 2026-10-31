package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "The user paused the task",
		"The user paused the task via the task control API (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was actively canceled; running intents return to frontier(open) and will be re-claimed from the start after the task resumes")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "The orchestrator agent paused the task",
		"The orchestrator agent called the pause_task tool to pause this task. This Planner/Worker run was actively canceled; running intents return to frontier(open) and will be re-executed after resume")
	AbortTaskDeleted = cause("task_deleted", "The task was deleted",
		"The task is being deleted (DELETE /api/tasks/{id}); the deletion barrier canceled this task's running Planner, Worker, and main Agent. This run's results will no longer be used")
	AbortPausedOnReload = cause("paused_on_reload", "Backend restored the task pause",
		"On startup, the backend restored the task pause from its persisted database state. This run was canceled; normally no agents are running during recovery")
	AbortGoalMet = cause("goal_met", "The planner judged the task goal met",
		"The planner judged the task goal achieved and set the task to done, then canceled still-running Workers; these intents are marked stopped, not failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Task timeout drain wait expired",
		"After the task reached its timeout, the system waited for running Workers to wrap up gracefully, but even the 90-second drain grace was insufficient, so a hard cancel was issued; intents are marked exhausted, and facts/assets already written during wrap-up are preserved")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "The planner terminated this intent",
		"The planner called kill_work to actively terminate this intent, usually meaning the direction drifted or no longer has value; the intent is marked stopped and will not be re-claimed automatically")
	AbortWorkPausedByUser = cause("work_paused_by_user", "The user paused this worker intent",
		"The user paused the running Worker. This call was canceled and the intent turns to paused; all registered intents, facts, findings, and activity records are preserved, and it re-executes from the start after resume")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "The user deleted this worker intent",
		"The user deleted the running Worker. This call was canceled; after the Worker exits its write zone, the server handles the intent per the user's chosen deletion mode — a soft delete only marks it deleted and keeps all outputs, while a hard delete cascades to remove the intent and downstream nodes supported only by it")
	AbortWorkFinished = cause("work_finished", "Worker finished; context released",
		"The Worker ended normally; the engine released its context resources in detachWork. This is not a run interruption; if it appears in an interruption message, the cancel and finish events raced")
	AbortPausedRaceGuard = cause("paused_race_guard", "No new runs while task paused",
		"While the task is paused, the engine refuses to issue a new execution context, preventing a race between claim and pause from starting Workers; claimed intents return to the frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "The user stopped this conversation turn",
		"The user clicked stop, actively aborting this main-agent or session-agent run. Activity records already produced are preserved; you can send the next message")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task pause ended the chat turn",
		"When the user paused the task, the running main-agent conversation was canceled in sync. Activity records already produced are preserved; after resume this turn's message is not replayed automatically")
	AbortChatTurnFinished = cause("chat_turn_finished", "Chat turn ended; context released",
		"This conversation turn ended normally; the server is releasing this turn's context resources. This is not a run interruption; if it appears in an interruption message, the cancel and finish events raced")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "The backend process is shutting down",
		"The backend received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running agents are canceled; after restart, leftover running intents reset to open and re-execute")
	AbortRunHardTimeout = cause("run_hard_timeout", "The per-run hard-timeout backstop fired",
		"A single run exceeded its soft wall-clock budget plus extra grace, meaning a model request or some tool has not returned for a long time and the normal round-boundary wrap-up could not run. Focus on checking the last tool call that never returned before the interruption")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "The upstream context hit its deadline",
			"The upstream context hit its deadline, but the setter did not attach a named cause via WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "The canceller attached no named cause",
			"The upstream context was canceled, but the canceller did not attach a named cause via context.WithCancelCause; register the reason in agent/cancelcause.go and wire it to this cancellation point", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
