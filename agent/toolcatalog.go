package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// This document changes the "built-in tools" from pure code into an enumerable directory that can be overridden by DB:
// - BuiltinToolSeeds(): Expand the three built-in tool sets of the execution agent into seed records (key +
// Description + parameter schema + default bound agent), for idempotent seeding into the tools table when the server is started.
// - ToolResolve hook: When running, press the tools row in the DB to filter the installed tools by agent +
// Override description/schema + inject parameter default values". The key/handler is still at the code level, and the DB only changes "prose and default values".
// The handler (Call behavior) always comes from the code - DB cannot change it, it can only change the description and default input parameters seen by the model.

// ToolSeed is a seedable snapshot of a built-in tool: the key is CoreTool.Name() (tied to handler,
// UI is read-only), Desc/Schema is taken from the tool definition in the code, Agents is the agents that the code gives it to by default.
type ToolSeed struct {
	Key    string         // = CoreTool.Name(), primary key, cannot be changed
	Desc   string         // Top-level description (can be overridden in UI)
	Schema map[string]any // Parameter JSON-Schema (structure is read-only, description/default can be changed in the UI)
	Agents []string       // Default bound agent key (worker/planner/mainagent)
}

// builtinToolsByAgent uses a "read-only empty shell" ToolSet (nil stores) to construct each execution agent
// Domain toolset. The tool constructor only stuffs the closure into Spec and does not dereference the store during construction, so nil is safe——
// These tools are only used here to read Name()/Description()/InputSchema(), never Call.
//
// Deliberately [excluding] SDK general tools actool.DefaultTools() (Read/Write/Edit/MultiEdit/LS/Glob/
// Grep/Bash): Each of them has a fixed agent, there is no choice of "who to bind to", and most of the instructions are in Prompt()
// (This table only covers Description(), which will cause half-coverage to be misleading). No seed → No DB row → ToolResolve
// Release as is, without overwriting, and behave as before. Only artex's own domain tools can be managed in the table.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals (goal disassembler) are bound by default to set_goals + set_constraints: they rely on them to separate the goals,
		// The extracted operation constraints are written into the library. It shares the same managed tool with mainagent. The description/schema can be changed on the web side and checked by agent.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto is bundled with vulnerability reporting + asset management tools by default. Other domain tools can be checked as needed in the UI.
		// New libraries are written by this seed; old libraries are migrated by seedAutoDefaultBindings.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// Pentest (independent penetration agent) is bound by default: check assets/insert assets/report vulnerabilities/check vulnerabilities/check enterprises.
		// New libraries are written by this seed; old libraries are migrated by seedPentestDefaultBindings.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound: These system tools will be entered into the directory as usual (visible on the web side and can be manually checked by agent), but
// Default [Do not bind any agent] - ToolResolve will discard all tools that are bound to empty objects and must be explicitly opt-in.
// The reason why it remains in the base tool set of a certain agent (such as goal_met in PlannerTools): First, it allows the seed to
// Construct it to get desc/schema. Secondly, the user manually binds it back and then runs it only if it is in the base and ToolResolve can be retained.
//
// goal_met: Bypassing prove_goal one by one and directly announcing [the entire task is completed] from the global perspective, it has heavy weight and risks of misjudgment, and is inconsistent with
// "prove_goal marks the last goal → automatically ends" is repeated, so no agent is given by default and can be tied manually when needed.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds combines the built-in tool sets of each agent into a seed list: tools with the same name (such as list_assets
// Multiple agents) are combined into one, and Agents takes the union; the tools in defaultUnbound force the binding to be empty.
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // Enter the directory and can be manually bound, but no agent is given by default (save [] instead of null, consistent with other tools)
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// The default input parameter is get injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched — only default values are merged in.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
