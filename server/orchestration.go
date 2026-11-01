package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// This document implements P2 "Cross-task Orchestration Toolset" (docs/Benchmark Orchestration §2 P2). These are the host tools - required
// Access the Manager (Store for any task), Engine (pause), and build task processes, so it lives in the server layer.
// Reading tools redirect "existing per-task tools" to the target task's store for running (create a temporary ToolSet
// and Call its corresponding tool), thereby reusing exactly the same logic; the control class (spawn/pause) directly calls Manager/Engine.
// They are seeded into the tools table like traffic tools and bound by agent (visible only when bound to the orchestration agent).

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools := append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...) // Platform operation tools (modify skill/tools/MCP for Auto use)
	custom, err := s.customTools()
	if err != nil {
		log.Printf("[custom-tool] load failed: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := s.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
	}
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id is required"), nil
	}
	t, ok := s.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("task not found: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets() != nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // Universal wakeup (write operations without dedicated callbacks go through it; read tools are no-op)
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → Remember a "People have added N strategic hints:..." trigger and wake up the planner
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"Lists all tasks (id/description/goal/status/runtime/parent task/LLM config). The orchestration agent uses it to get the big picture: which tasks are stuck too long and which LLM each one runs on. Runtime: running = created→now, terminal = created→last activity (seconds). llm_profile: the config name the task's planner/worker uses; (active profile) = follows the globally active config.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName := map[int64]string{}
			if profs, err := s.m.pg.ListProfiles(); err == nil {
				for _, p := range profs {
					profName[p.ID] = p.Name
				}
			}
			out := make([]map[string]any, 0)
			for _, t := range s.m.List() {
				status := s.deriveTaskStatus(t)
				end := lastAct[t.ExpID]
				if live := s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur := int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row := map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef != "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState := t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(active profile)"
				} else if n, ok := profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d (deleted)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"Lists the available LLM configs (profiles): id, name, model, format, and whether it is the currently active config. Use an id in spawn_task's llm_profile_id to give a subtask its own LLM (e.g. a cheap model for recon, a strong one for exploitation). API keys are not included.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := s.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"Creates a new subtask and starts its exploration engine; returns task_id. Use it to turn one thing (a question / a goal) into an independent task. parent_ref is optional: set it to the id of the currently associated parent task for a parent-child link.",
		objSchema(map[string]any{
			"description":            strParam("Task description (short title)"),
			"goal":                   strParam("Task goal (what to achieve)"),
			"parent_ref":             strParam("Optional: parent task id (for a parent-child link)"),
			"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("Optional: list of source task ids for read-only inheritance (up to %d). The subtask can read these tasks' discovered assets/conclusions as a starting point; unlike parent_ref's pure pointer, this is content inheritance.", db.MaxTaskSourceCount)},
			"llm_profile_id":         map[string]any{"type": "integer", "description": "Optional: id of the LLM config for this subtask's planner/worker (see list_llm_profiles); if left empty it inherits the parent task's, then falls back to the globally active config"},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "Optional: task-level timeout (seconds). When it elapses, a graceful wrap-up is triggered and the task enters the terminal timeout state; empty or 0 = no limit"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "Optional: planner heartbeat trigger interval (seconds). Once this much time has passed since the last planning round ended / the task started with no trigger in between → run a planning round (deadlock backstop + wake up to supervise in-flight workers). Empty or 0 = default 600 (10 min);"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "Optional: enable for simple tasks — at creation, a seed intent (content = description + goal) is issued directly so the worker can start testing without waiting for the first planner round; default false (standard plan-then-execute)."},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				SourceTaskIDs        []string        `json:"source_task_ids"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "Unnamed task"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("goal is required"), nil
			}
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// Read-only inheritance source tasks: upper limit of number + validity/deduplication/existence of each ID, verification rules are consistent with HTTP creation tasks.
			if len(a.SourceTaskIDs) > db.MaxTaskSourceCount {
				return actool.Errorf(fmt.Sprintf("At most %d related tasks can be selected", db.MaxTaskSourceCount)), nil
			}
			sourceIDs := make([]int64, 0, len(a.SourceTaskIDs))
			seenSources := map[int64]bool{}
			for _, raw := range a.SourceTaskIDs {
				id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil || id <= 0 || seenSources[id] {
					return actool.Errorf("Related task id is invalid or duplicated"), nil
				}
				if _, ok := s.m.Task(strconv.FormatInt(id, 10)); !ok {
					return actool.Errorf(fmt.Sprintf("Related task #%d does not exist", id)), nil
				}
				seenSources[id] = true
				sourceIDs = append(sourceIDs, id)
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id := parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok := s.loadProfileConfig(id); !ok {
					return actool.Errorf(fmt.Sprintf("LLM config #%d does not exist or has no API key set", id)), nil
				}
				pin = &id
			} else if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			var llmIDs []int64
			if pin != nil {
				llmIDs = []int64{*pin}
			}
			t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				SourceTaskIDs:        sourceIDs,
				LLMProfileIDs:        llmIDs,
				TimeoutSeconds:       a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.ParentRef != "" {
				t.ParentRef = a.ParentRef
				if id, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
					_ = s.m.PG().SetParentRef(id, a.ParentRef)
				}
			}
			// The shared post-construction process reuses the same launchTask section as the HTTP creation task (server.go createTask):
			// seed + do goal decomposition visibly in the background (round 0/LLM steps/goals one by one) + engine.Run.
			// seed_first_intent defaults to false (standard is planned first and then executed); simple tasks can be enabled to directly deliver a work test.
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "Pauses the given task (stops its planner/worker loops).",
		objSchema(map[string]any{"task_id": strParam("Id of the task to pause")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok := s.m.Task(a.TaskID)
			if !ok {
				return actool.Errorf("task not found: " + a.TaskID), nil
			}
			if _, err := s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "Reads the exploration-graph overview of the given task (same as graph_overview: asset counts / frontier / findings / coverage, etc.); the task is selected via task_id.",
		objSchema(map[string]any{"task_id": strParam("Task id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "Reads the confirmed findings of the given task (with flag/PoC; each entry carries id/task_id/intent_id/vulnclass/severity/summary/status); the task is selected via task_id.",
		objSchema(map[string]any{"task_id": strParam("Task id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "Injects a strategic hint into the given task (the task's planner will read it when generating intents next round).\n"+
		"★ Batch first: put multiple hints in the hints array and submit them at once (returns an ids array, same length and order as hints; failed items get id=0); for a single hint omit hints and give the top-level text directly.",
		objSchema(map[string]any{
			"task_id":      strParam("Task id"),
			"hints":        map[string]any{"type": "array", "description": "[Prefer this] Array of hints; each element has the same fields as the top level (text/asset_ids/traffic_refs).", "items": objSchema(map[string]any{"text": strParam("Hint content"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[Single] hint content"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Anchored asset ids (optional, 0/1/many; asset ids within that task)"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"Views the execution of a work (intent) in the given task: get_task_worker_trace(task_id, intent_id) shows step summaries; add step_ids=[...] to fetch the full content of those steps (at most 5 at a time; extras beyond 5 are not returned).",
		objSchema(map[string]any{
			"task_id":   strParam("Task id"),
			"intent_id": map[string]any{"type": "integer", "description": "Intent id (the work in that task)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Optional: step ids to fetch full content for (at most 5 at a time; extras beyond 5 are listed in omitted_step_ids)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "Lists which works (intents) have run in the given task + their step counts, to find which works are worth reviewing (then use get_task_worker_trace).",
		objSchema(map[string]any{"task_id": strParam("Task id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "Searches all works' execution in the given task by keyword (returns matched step summaries + intent_id).",
		objSchema(map[string]any{"task_id": strParam("Task id"), "q": strParam("Search keyword")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"Reads the full content of an exploration-graph node in the given task (finding/fact/intent/goal: summary + detail/evidence/PoC). id is the exploration node id (e.g. returned by report_finding, or an id from list_task_findings). Use it to get a vulnerability's complete evidence before writing its report.",
		objSchema(map[string]any{
			"task_id": strParam("Task id"),
			"id":      map[string]any{"type": "integer", "description": "Exploration-graph node id (not an asset id)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"Writes/updates the [detailed report] of a registered finding (full Markdown text; replaces the old content wholesale). Pass as finding_id the id returned by report_finding (the number in \"finding recorded: <id>\"). The report should include: vulnerability overview, impact and harm, reproduction steps, evidence/PoC, remediation advice.",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "Target finding id (the id returned by report_finding)"},
			"report":           strParam("Full detailed report text, in Markdown"),
			"evidence_version": map[string]any{"type": "integer", "description": "The evidence version returned by get_finding_traffic; prevents the report from overwriting newer evidence changes"},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID := parseProfileID(a.FindingID) // Reuse "number or numeric string" parsing
			if nodeID <= 0 {
				return actool.Errorf("finding_id is invalid"), nil
			}
			n, err := s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("No finding record found for finding_id=%d (register it with report_finding first)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	}
	return "created"
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + platform tools default-bind to the built-in Auto agent
	// operating platform). The first insertion of SeedTool takes effect; rows that have been seeded in the old library are supplemented by seedAutoDefaultBindings.
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range s.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		if t.Name() == "bind_finding_traffic" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range s.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedCompanyScopeRebind()
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces Unbind from worker by default (one-time)
	s.seedWorkerReadbackRebind()  // Fix accidental deletion of old migration: bind search_all_worker_traces/get_worker_trace/node_detail back to worker (one-time)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt()             // The goals prompt word is added to the "pumping operation constraint" step → a new default version is added to the old library (one-time)
	s.reseedMainAgentPrompt()         // The mainagent prompt word is added "add_intent asks whether to create a goal after the goal is achieved" (one-time)
	s.reseedPlannerPrompt()           // Planner prompt: rewrite "0 intention" justification + add quantitative acceptance check (one-time)
	s.reseedWorkerPrompt()            // worker prompt word: add negative conclusion evidence threshold (one-time)
	s.seedReporterAgent()             // Preset "report writing" agent + tool binding + finding trigger (one-time)
	s.upgradeReporterTriggerMessage() // Migration of old database: let reporter return evidence_version (one-time)
	s.seedFindingTrafficTools()       // Added optional evidence parameters and read-only evidence tools, retaining user configurations
	s.seedFindingWorkflowTools()
	// Note: pentest's default tool bindings do not need to be migrated - BuiltToolSeeds is
	// list_assets/insert_assets/report_finding/list_findings/list_companies together with
	// Just seed pentest together (the project does not have old libraries yet and will not be migrated).
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. spawn_task of llm_profile) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v7_list_facts_paging"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// At the same time, some built-in agent tools are refreshed as code defaults:
	// - goal_met: The description of the old library seed is misleading with "end this round of planning", which will make the planner regard it as
	// The method of "ending empty rounds" and misjudgment that the entire mission was completed as soon as the run started.
	// - insert_assets: Add related input parameter (mark whether the asset is related to the current task and determine whether to enter coverage),
	// SeedTool is first inserted only if the schema of the old library has been seeded, otherwise this new parameter will not be received.
	// - list_facts: changed to paging, added limit/before/q input parameters; the old library has been seeded with an empty schema, otherwise
	// "No parameters" is displayed on the tool management page, and the model cannot obtain these parameter descriptions.
	refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true}
	for _, sd := range agent.BuiltinToolSeeds() {
		if !refreshBuiltin[sd.Key] {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		if err := s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
	log.Printf("[tools] refreshed orchestration/platform tool schemas to code defaults (one-time)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
		log.Printf("[tools] failed to unbind goal_met from planner: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// reseedGoalsPrompt refreshes the prompt word of the goals goal disassembler to [current code default] - because the default text is added
// "First extract the operation constraints (set_constraints) and then remove the target" step, and SeedPromptIfEmpty is first inserted into only, the old library
// The existing version 1 cannot receive this step. Use version management here [Add a new version] and switch to it (ResetPromptToDefault),
// The old version is still retained in the history. If the user has customized it, it can be retrieved from the version record. settings flag guard → do it only once;
// In the future, the default will be to bump this flag. Brand new libraries do not need to be processed (SeedPromptIfEmpty has been seeded with the latest default).
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Only try once whether successful or not
	a, err := s.m.pg.GetAgentByKey("goals")
	if err != nil || a == nil {
		return // When the new library has not yet created an agent line, seedPrompts will directly seed the latest default, without this migration.
	}
	tmpl := agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded and the latest default → the current version is equal to the code default, no need to add duplicate versions.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to reseed the goals prompt with the new default: %v", err)
		return
	}
	log.Printf("[prompts] appended a new default version of the goals prompt (adds the extract-operation-constraints step; one-time)")
}

// reseedMainAgentPrompt Refresh the mainagent prompt word to [current code default] - the default text has added "Target All
// After achieving the add_intent direct investment intention, ask the person whether to register as an official target" this guidance, and SeedPromptIfEmpty is first inserted
// Only, existing versions of the old library cannot be received. Use version management [Add a new version] and switch to it (ResetPromptToDefault), the old version is still
// It is retained in the history. If the user has customized it, it can be retrieved from the version record. settings flag guard → Do this only once. Brand new library does not need to be processed
// (SeedPromptIfEmpty has seeded latest default). Completely isomorphic to reseedGoalsPrompt.
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Only try once whether successful or not
	a, err := s.m.pg.GetAgentByKey("mainagent")
	if err != nil || a == nil {
		return // When the new library has not yet created an agent line, seedPrompts will directly seed the latest default, without this migration.
	}
	tmpl := agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded and the latest default → the current version is equal to the code default, no need to add duplicate versions.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to reseed the mainagent prompt with the new default: %v", err)
		return
	}
	log.Printf("[prompts] appended a new default version of the mainagent prompt (adds asking whether to register a goal after goals are met; one-time)")
}

// reseedPlannerPrompt Refresh the planner prompt word to [current code default] - the default text has been streamlined and restructured, and "restraint" has been downgraded to
// Only duplicates are removed, "depth takes precedence over coverage" and "hard bottom line: the goal must be produced if the goal is not achieved and there is no intention to run", and a bound is added to the review of negative conclusions.
// Every time there is a substantial change to the default, bump the flag below (currently v2) to let the existing old library refresh again. SeedPromptIfEmpty is first inserted only. The existing version of the old library cannot be received, so version management is used.
// [Add a new version] and switch to it (ResetPromptToDefault). The old version is still retained in the history. If the user has customized it, he can record it from the version.
// retrieve. settings flag guard → Do this only once. Brand new libraries do not need to be processed (SeedPromptIfEmpty has been seeded with the latest default). with
// reseedGoalsPrompt is completely isomorphic.
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_compact_realistic_v2"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Only try once whether successful or not
	a, err := s.m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return // When the new library has not yet created an agent line, seedPrompts will directly seed the latest default, without this migration.
	}
	tmpl := agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded and the latest default → the current version is equal to the code default, no need to add duplicate versions.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to reseed the planner prompt with the new default: %v", err)
		return
	}
	log.Printf("[prompts] appended a new default version of the planner prompt (compact rewrite + restraint demoted to dedup + depth-first + cap on negative re-verification; one-time)")
}

// reseedWorkerPrompt refreshes the worker prompt word to [current code default] - the default text record_fact section deletes the "negative conclusion"
// Write the entire sentence "observation + tentative reading", and decouple confidence (observed/inferred) from "whether the original intention and means have been exhausted" (these are easy to mislead planners),
// At the same time, the facts array is tightened into a few exceptions that are "completely independent of each other and cannot be merged". Bump flag to v3 to refresh the existing old library.
// SeedPromptIfEmpty is first inserted only. The existing version of the old library cannot be received, so use version management [Add a new version] and cut it. The old version will still be retained in the history and can be retrieved.
// settings flag guard → Do this only once. Brand new libraries require no processing. Completely isomorphic to reseedGoalsPrompt.
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_compact_v4"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Only try once whether successful or not
	a, err := s.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		return // When the new library has not yet created an agent line, seedPrompts will directly seed the latest default, without this migration.
	}
	tmpl := agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded and the latest default → the current version is equal to the code default, no need to add duplicate versions.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to reseed the worker prompt with the new default: %v", err)
		return
	}
	log.Printf("[prompts] appended a new default version of the worker prompt (context-lookup section narrowed to list_assets/list_findings; drops list_facts/node_detail/asset_neighbors; one-time)")
}

// reporterToolCallMessage must unconditionally require get_finding_traffic to be read once before writing the report.
// This tool is read-only and "does not rely on the capture switch". The evidence of manual binding can be read whether automatic binding is turned off or off. If here
// It is written as "Read only when automatic binding is enabled". When the default configuration is turned off, the reporter will not pass evidence_version.
// SetFindingReportVersionByNodeID writes -1 according to legacy semantics, vulnerability details and Markdown export
// From now on, "The evidence has changed and the report needs to be updated" is always there, and there is no entrance on the UI to clear it.
const reporterToolCallMessage = "A finding was just registered via report_finding above. Read the returned JSON's finding_id (independent finding record ID) and finding_node_id (exploration node ID), " +
	"first use get_finding_traffic(finding_id) to read the current evidence list and its version (an empty list is normal — write the report as usual); " +
	"if auto-binding is enabled in the run guidance, verify and associate this finding's traffic before reading. Use finding_node_id for node details. " +
	"Finally call update_finding_report(finding_id=finding_node_id, report, evidence_version=<version actually read>) to save; " +
	"evidence_version must be passed, otherwise the report will be permanently marked as needing an update. Don't mix up the two IDs."

// Legacy trigger messages (0.3.8 and earlier). Only records that are still word-for-word identical to it will be overwritten by the migration, and changes made by the user will remain unchanged.
const reporterToolCallMessageV1 = "A finding was just registered by report_finding. Extract finding_id " +
	"(the number in the tool result \"finding recorded: <id>\") and the task ID from the trigger context, write the detailed finding report according to your role, " +
	"then call update_finding_report(finding_id, report) to save it."

// upgradeReporterTriggerMessage refreshes the reporter trigger message that still has the default copy in the old library to a new version.
// seedReporterAgent is guarded by reporter_agent_seed_v1 and only writes triggers when creating a new agent, so
// The upgraded library cannot get new copy - the tool schema is completed by seedFindingTrafficTools
// evidence_version, but nothing tells the reporter to use it. It is a one-time operation and only covers unmodified copy.
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Just try it once
	triggers, err := s.m.pg.ListTriggersFor("reporter")
	if err != nil {
		log.Printf("[reporter] failed to read triggers: %v", err)
		return
	}
	for _, t := range triggers {
		if !t.OnToolCall || t.ToolCallMessage != reporterToolCallMessageV1 {
			continue // If the user changes it or it is not a finding trigger, it will not move.
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err := s.m.pg.UpdateTrigger(t); err != nil {
			log.Printf("[reporter] failed to upgrade the trigger message: %v", err)
			return
		}
		log.Printf("[reporter] trigger message upgraded to read and return evidence_version")
	}
}

// seedReporterAgent presets a "report writing" custom agent (builtin=false, can be edited/delete in the UI):
// Bind update_finding_report + task query tool, and hang a "report_finding is triggered when called"
// Trigger - triggers it to write a detailed report every time a vulnerability is registered. One-time (settings flag guard): It will not be recreated after the user deletes it.
// Dependency: The orchestration tool has been stored in SeedTool above this function, so it can be bound.
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Only try once whether successful or not

	if exist, _ := s.m.pg.GetAgentByKey("reporter"); exist != nil {
		return // The key is occupied (created manually by the user) - not overwritten
	}
	a, err := s.m.pg.CreateAgent("reporter", "Report Writer",
		"Detailed vulnerability reporting: auto-triggered when a finding is registered; gathers evidence and the execution process, writes a Markdown report, and saves it back.")
	if err != nil {
		log.Printf("[reporter] failed to create the agent: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
		log.Printf("[reporter] failed to seed the prompt: %v", err)
	}
	// Trigger run strategy: parallel + none - one vulnerability report, multiple findings written in parallel.
	// merge must be none: otherwise (default all) a wave of finding will be merged into one run, and parallelization will be meaningless.
	// maxParallel=5: Maximum of 5 reporting sessions at the same time to avoid too many LLM calls at once.
	if err := s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
		log.Printf("[reporter] failed to set the trigger run policy: %v", err)
	}
	// Tools needed to bind it: write reports + read evidence/execution process/situation.
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err != nil {
		log.Printf("[reporter] failed to bind tools: %v", err)
	}
	// Trigger: report_finding is triggered when it is called (the tool returns "finding recorded: <id>" with finding_id,
	// The task id is also in the trigger message).
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "reporter",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err != nil {
		log.Printf("[reporter] failed to create the trigger: %v", err)
	}
	log.Printf("[reporter] preset the Report Writer agent + finding trigger")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
		log.Printf("[auto] failed to bind report_finding by default: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
		log.Printf("[planner] failed to bind report_finding by default: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
		log.Printf("[planner] failed to bind list_assets by default: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // worker→planner default binding switch
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
		log.Printf("[planner] failed to bind add_company_scope by default: %v", err)
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
		log.Printf("[worker] failed to unbind add_company_scope: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k := range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err := s.m.pg.RemoveAgentFromTool("worker", k); err != nil {
			log.Printf("[worker] failed to unbind %s from worker: %v", k, err)
			return // If an error occurs, the flag will not be dropped and will be tried again next time.
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" // v2: append node_detail
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err != nil {
		log.Printf("[worker] failed to re-bind the look-back/detail tools: %v", err)
		return // If an error occurs, the flag will not be dropped and will be tried again next time.
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: Replace the old asset tool name and add insert_assets/add_company_scope
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := make([]string, 0, len(platformToolKeys)+12)
	for _, t := range s.orchestrationTools() {
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// Asset tools: Auto operating platform often needs to view/register assets and manage company scope.
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err := s.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
		log.Printf("[auto] failed to apply default bindings: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
