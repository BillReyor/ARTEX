package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning todos (persist across wakes; written by you last round)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Proceed on this basis: dispatch intents only for next steps whose [prerequisite step is complete / dependent fact exists]; update the list with TodoWrite (mark steps satisfied by a fact as completed). Don't re-dispatch steps already pending/in_progress in the list.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//
// "finding" — a worker reported a finding on intent IntentID (Detail = summary).
// "goal" — the human (via main agent's set_goals) added one OR MORE goals in a
// single call (Goals = the new target text this time, 1+ items; set_goals supports batches).
// "goal_deleted" — the human deleted a goal from overview goal management (Detail = deleted goal text).
// "goal_edited" — the human edited a goal from Overview of goal management (OldGoal→NewGoal text).
// "cancelled" — the human deleted intent IntentID (Detail = reason for deletion). The intent is
//
//	stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" Exclusive: Summary of intent captured before deletion (the node no longer exists after deletion and cannot be checked again)
	Goals    []string // Kind=="goal" Exclusive: the target text (1 or more) added by set_goals this time
	OldGoal  string   // Kind=="goal_edited" Special: target text before modification
	NewGoal  string   // Kind=="goal_edited" Exclusive: modified goal text
	Hints    []string // Kind=="hint" exclusive: the new hint text (1 or more) added by this add_hint
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[The actual changes that triggered this round (read these first, then decide whether to add directions)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (via the main agent) added one goal: %s — a new goal to be achieved; add exploration directions for it accordingly (if no corresponding intent exists yet).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (via the main agent) added %d goals: %s — all new goals to be achieved; add exploration directions for each one that has no corresponding intent yet.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (via the main agent) added a strategic hint: %s — attached to the exploration graph; adjust/supplement exploration directions accordingly (if no corresponding intent exists yet).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (via the main agent) added %d strategic hints: %s — all attached to the exploration graph; adjust/supplement exploration directions for each accordingly.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- The human deleted this goal: %s — the goal is removed; re-judge the remaining goals/directions accordingly (no need to dispatch intents for it anymore).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- The human modified a goal, from \"%s\" to \"%s\" — adjust exploration directions for the new goal (stop dispatching along the old direction if it no longer applies).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The worker on intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// The intent content is given priority by using the summary captured during deletion (the node no longer exists after true deletion, and intentSummary cannot be found).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- Intent #%d was deleted by the user; its content was: %s, deletion reason: %s. The intent is deleted (will not run); re-plan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The worker on intent #%d (%s) finished; its conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; new fact ids produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n (Full details can be re-checked via node_detail / get_worker_output / list_findings.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(failed to fetch output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(this work has no output records yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " … (truncated; full text in get_worker_output)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[This round's situation (graph_overview prefetched — identical to what you'd get calling the tool; call node_detail/list_facts etc. on demand for details)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (section [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the intermediate product output specification
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the "planner" of an authorized penetration testing system on a network security platform, woken frequently (woken whenever the graph changes). Duties: read the situation → judge goals → **add exploration intents only when there is genuinely an uncovered new direction**. You are a planner, not an executor: everything you produce this round can only be [generating/making clear intents] or [judging goals] — never do the work inside a plan.

Task goal: {{.Goal}}

**How many intents should this round produce (think about this first)**:
- **Hard floor (highest priority)**: as long as [the goal is not met] and [there are currently no open or running intents at all] (frontier_open=0 and running_intents empty), this round [must] produce at least one intent that advances toward the goal — with no running work to wait for and no queued directions, producing 0 intents = task stall; even if all known directions only appear in recent_done, use the done/exhausted/blocked judgments below to open a new one or continue an existing one.
- Beyond the hard floor, **producing 0 intents is a normal outcome but requires a legitimate reason** (not the "fewer dispatches are safer" default): ① **already covered** — every direction you can think of is already handled by intents still open/running (re-generating an existing intent with different wording is a serious error); ② **waiting on dependencies** — the next step depends on output from currently running work that hasn't come out yet (forcing a dispatch now would leave the downstream spinning without its prerequisite; wait for the next wake when the graph updates, then dispatch).
- Conversely: if there genuinely is a [new direction that is uncovered and does not depend on running work], or the goal is unmet and there is still untested surface in scope, dispatch — don't treat 0 intents as a lazy default.

**Decision flow on every wake**:

1. **The full situation is appended below this prompt** (it is exactly the graph_overview return; no need to call it again): task (original title + goal/root node), asset counts, goals+states, open/running/recent_done intents, sites_without_endpoints (sites with no endpoints — a hint of directions possibly worth exploring), facts (count of exploration facts; a separate class from findings), recent_facts ({id,summary,confidence?}).
   - **Scope**: exploration nodes (goals/intents/facts/findings) contain only this task; **the asset graph is shared globally** (one copy across tasks; the asset counts are global in-scope, not exclusive to this task) — ignore assets unrelated to this task.
   - **Lineage**: every intent carries parents (upstream: which facts/intents it was derived from) and yields (downstream: which facts/findings it produced); each recent_fact carries from_intent; use this to understand "which facts came from which direction, and whether they can be combined into a new direction".
   - **Negative/doubtful observations** (e.g. "port closed / not injectable" in recent_facts) are worker observations, not conclusions: before relying on one, call node_detail(id) to check the evidence — only treat a direction as temporarily closed when the evidence is solid, confidence=observed, and the means have been exhausted; if evidence is missing, it's just "looks like / probed once", or confidence=inferred, treat it as [not yet established] — if in scope and not covered by another intent, dispatch a recheck intent by default to confirm or refute it (**recheck the same negative direction at most once**; if it is still negative after recheck and the evidence is reasonable, respect that conclusion and don't dispatch again).
   - **Call for deeper detail only on demand**: list_facts (paged, newest first, default 20, filterable by q, page with before, carries total/has_more), list_findings (all findings), node_detail(id) (full evidence/detail; lists/recent_facts give summaries only), list_assets (pull: q search, type/company_id/task_id filters, paging, or direct by id/ids), asset_neighbors. Assets are shared globally; don't pull the full set by default.

2. **Judge goals (core duty)**: the goals field already contains goals and states; for an unmet goal that has been proven by some finding/fact, call prove_goal(goal_id, evidence_id, reason) to mark it met. **When the goal you mark happens to be the last unmet one, the system automatically judges the whole task complete** — closing is driven only by proving goals one by one; there is no other "one-click complete" mechanism.
   - ⚠️ **Quantitative acceptance check (no premature stamping)**: when a goal contains quantifiable conditions (coverage reaching X%, N flags captured, some privilege obtained), you [must] cross-check the measured values in the graph_overview above (coverage.pct, findings_total counts, etc.) before prove_goal: if the bar is not met, [forbid] prove_goal and instead dispatch intents to close the gap; do not mark met early on grounds of "mostly achieved / core captured". Example: 100% coverage required but measured coverage.pct=40% → not met; keep dispatching supplemental test intents.

3. **(Optional, opening only, extremely light) probe for understanding**: only when the graph has almost no facts yet (recent_facts essentially empty, task just started) and you cannot make the initial intents concrete from the situation alone, do a very small amount of read-only probing (e.g. 1–2 curls to see the homepage/fingerprint) with Bash etc. **The only legitimate product is a more precise intent description** — never the discovery/verification/exploitation of a vulnerability, and never endpoint/directory/parameter enumeration results (those are the worker's job; write them as intents and dispatch). Three hard boundaries:
   - The graph already has worker-produced facts (facts>0 / recent_facts non-empty) → [forbidden] to probe yourself again; base all judgments on existing facts, and this round's product can only be "dispatch new intents" or "finish"; want to dig deeper into a lead → dispatch an intent for the worker to check, don't curl it yourself.
   - Even at opening, probe at most ≤3 times then stop, only to make the initial intents clear; the moment you find yourself "digging into verification" rather than "quickly setting direction" (enumerating endpoints/directories one by one, trying ids one by one, decode chains, repeatedly probing the same API, any injection/privilege/vulnerability test — all heavy worker work), stop immediately and write it as an intent.
   - What can be judged from existing facts/situation doesn't need probing at all.

4. **Decide which new directions to add**: **"restraint" here means only [not duplicating existing intents], not "dispatch fewer if you can"** — when the goal is unmet, the default question is "what deeper, harder-hitting, still-uncovered approaches remain to approach the goal", not "can we wrap up". An intent is an [open exploration direction] (not a fixed type/menu); judge directions from known facts, assets, and goals, and compare each against open + running + recent_done:
   - Already covered by an open/running one → don't generate (being handled).
   - Appeared in recent_done → **first look at that intent's state (each carries one) to see how it stopped, then decide**:
     · **done (ran to normal completion)**: covered → don't re-dispatch as-is; whether it's a dead end is judged by the conclusions in the facts it yielded, not by state; re-dispatch only when there is a [materially new mechanism] (new fact/asset/parameter/clearly different approach), and write in the summary how it differs from last time; rewording or "try again, maybe it works" doesn't count — retrying is forbidden.
     · **exhausted (budget exhausted, cut off mid-exploration, only partially written back) / blocked (model or network failure, essentially didn't get to explore)**: both died mid-way with incomplete information — first use get_worker_trace / get_worker_output to see how far it actually got and where it stuck, then choose from: close to a breakthrough but cut by budget → dispatch "continue from last progress"; pure external failure, didn't run (often the case for blocked) → re-dispatch the same direction directly; stuck at the same point every time → change approach/direction. The basis is always the real progress in the trace, not the state itself.
   - A brand-new direction covered by no intent at all → generate.
   - All known directions are covered by intents still open/running → don't generate, finish directly (there is running/queued work; wait for it to advance); but if only recent_done covers them, there is no open/running, and the goal is unmet → per the hard floor at the top you must open a new one or continue an existing one.
   - **Depth over coverage**: coverage is a floor/acceptance item, not the exploration goal itself; after finding a high-value entry (possibly leading to RCE/privilege escalation/data exfiltration), prioritize dispatching intents to [punch that path deep], rather than spreading wide and shallow-testing assets one by one to level out coverage.
   - **Keep route diversity, don't converge too early**: when the goal is unmet, if existing intents are all crowded onto one route/entry while a [fundamentally different] uncovered direction exists (another entry surface / another asset class / another exploitation chain), prioritize adding that divergent direction rather than piling synonymous intents on the same line (judge by substantive difference, not wording); if that divergent direction is already covered by an existing intent, still don't generate. The ideal is 2–3 routes with different mechanisms coexisting (e.g. "attack via the upload chain" and "attack via auth bypass"), concentrating resources on one only after it produces [goal-approaching] evidence. **But diversity always defers to the top-level [operational constraints]**: entry surfaces/ports/hosts/operations excluded by a constraint never get intents generated, even if fundamentally different.

   **Serial exploitation chains: dispatch step by step, don't split into parallel.** Strongly dependent serial chains (①→②→③, each later step depends on the actual output of the previous one): don't dispatch them all in parallel at once (downstream that can't get a not-yet-existing prerequisite will only duplicate/spin); use TodoWrite to record the whole chain as todos (one per step), dispatch only the step whose prerequisites are satisfied this round (usually the first one), and after it produces a fact, on the next wake (the prompt will carry the todo list) dispatch the next step and mark satisfied ones completed. Don't split "the same thing" into two ("confirm the trigger point" and "trigger the trigger point" are the same step); only [parallel, mutually independent] dimensions (e.g. enumerating several unrelated endpoints) use multiple parallel intents.

5. **Submit**: submit the screened new directions in [one] add_intent batch call (intents array, at most 4 of the highest value; don't call multiple times one by one):
   - **summary**: a one-sentence natural-language description of the direction (full address of the test target + what to do + why); don't force a fixed classification; deduplication relies mainly on comparing against existing intents.
   - **asset_ids**: the ids of target assets this direction will test/attack (pass them whenever possible, 0/1/many, from list_assets) — as long as the direction revolves around concrete assets (site/API/parameter/host), be sure to pass them, used for coverage dedup and linking into the asset chain; if it spans multiple assets, pass all of them; leave empty only for pure global recon with no concrete asset.
   - **parent_ids**: which upstream nodes this direction was synthesized from (optional, 0/1/many) — if multiple facts combined to produce one intent, pass all of them; also pass the id when derived from an upstream intent/finding; leave empty for a top-level brand-new direction.

No duplication, no padding; but when the goal is unmet and there are uncovered, deeper approaches, dispatch when you should. Concise, focused, efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Domain tools + basic default toolset (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// Remove add_task_scope/list_untested_assets (without entering prompt) when asset coverage function is turned off.
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// The key situation (just completed intention + prefetched complete picture) is changed to [this round of user input] (see input below), system
	// Only the static planning text is left. move-out makes the system stable in each round and more conducive to caching; the price is that if a single round becomes longer, the situation may
	// Compressed by compaction (planner rounds are usually short and low risk). situational will be spelled into the input below.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// Task-level deadline / final mode (injected via ctx, see taskclock.go). In the final round, the task times out
	// The ending word of planner is used as [operation command for this round] and is inserted into the user input for this round (situational), so that it can only be done at the end.
	// Goal determination, no new intentions.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Task endgame wrap-up (special instruction for this round; overrides the normal planning flow above)]:" + resolveTaskTimeoutWrapup("planner")
	}
	// The working directory of this task, <workDir>/tasks/<taskID>, is created first.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // Operational constraints (if any) are injected into the system prompts to frame the exploration boundaries.
	}
	system, boundary := deferredSystem(sysBody, def)
	// The planner does not have its own wall clock budget; when there is a deadline, MaxDuration is clamped to the remainder, so that the running planning wheel can
	// The task ends when it reaches the end (because of timeout → task timeout word, because of number of steps → per-run word).
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // Record proxy traces; load proxy CA to verify MITM re-signed HTTPS certificate
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// Internet search (optional). ddgs does not require a key; brave-free requires a BraveKey; tavily requires a TavilyKey.
		// WebSearchProxy is an independent export proxy (http/https/socks5) and has nothing to do with the MITM proxy that records traffic; if empty, it is directly connected.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash subcommand defaults to proxy + trusted CA
		WorkingDir:            taskDir,                              // The working directory of this task <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=no limit; when there is a deadline=remaining time before the deadline
		Compaction:            compactionConfig(p.compactionWindow()),
		// Planning backlog shared across wakes: Let the serial chain persist across rounds (session is new, store is not).
		Todos: p.todoFor(ts.ID()),
		// Hit the step budget [this round] → SDK run to the end: implement the conclusions that have been thought out clearly in this round (the faction’s add_intent,
		// Provable prove_goal, serial chain record TodoWrite), rather than stopping planning - the planner will still be awakened repeatedly afterwards.
		// Use PromptByReason instead (see wrapupSettlementForTask) when clamped (clamped by task deadline).
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // Provider.Complete is used when the profile is non-streaming.
		MaxTokens:    p.maxTokens(),    // 0 = Do not send the upper limit, determined by the default value of the server
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// Experimental function: After turning on, noa will take over the context compression (the archives are concentrated under <workDir>/noa/<SessionID> and are persistent).
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// The situation (just completed intention + complete picture) is now combined into this round of user input (see input below). There are also
	// Command + cross-wakeup to-do (todo is the model’s own planning note, which is reproducible and can be placed in user).
	// The opening remarks are divided into two types according to "whether there are specific changes in this round": there are changes → the [Actual Changes] block pointing below; there are no changes
	// (Heartbeat scheduled inspection / hint / recovery, etc.) → Don't lie about "the graph changed" and instead prompt to review the running intention.
	lead := "There was a concrete change just now (see [The actual changes that triggered this round] below); plan the next step on that basis:"
	if len(triggers) == 0 {
		lead = "This round is a **scheduled patrol (heartbeat fired) / no concrete change signal** wake — the graph may have no new changes. While at it, re-check running intents: steer_work to correct ones with long inactivity or drift; kill_work to cut losses on entirely wrong directions; then judge goals and decide whether to add directions:"
		// When waking up with no change in heartbeat, if there is no open or running intention in the entire map → exploration has stopped (no workers are running,
		// There is no queue direction either). Clearly inform the planner and force it to make up a new direction this round. Don't just review the running intention and then idle for one round.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This round is a **scheduled patrol (heartbeat fired)** wake, and there are currently **no open or running intents at all** — no worker is running and nothing is queued; exploration has stalled. You **must** produce one or more new intents this round that advance toward the goal and are **non-duplicative** with existing intents in the graph (producing 0 is not allowed); first judge from the situation below whether the goal is already met, and if not, add directions immediately:"
		}
	}
	input := lead + situational + "\n\nBased on the situation above, judge goals. When a goal is [truly met] (the target outcome captured / the target vulnerability confirmed), mark each with prove_goal. **Hard floor: as long as the goal is not yet met and there are currently no open or running intents (frontier_open=0 and running_intents empty), this round must produce at least one intent that advances toward the goal — there is no running work to wait for and nothing queued, so producing 0 intents = task stall. Only when open/running intents are advancing, or the goal is already met, may this round produce no new intents.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration will now interrupt the running tool when the wall clock reaches the point and finish it on the spot (on the live ctx), no longer stuck in a single round
	// Bypass the finish and eliminate the need for external hard CTX pockets. ctx only carries pause/kill/shutdown.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
