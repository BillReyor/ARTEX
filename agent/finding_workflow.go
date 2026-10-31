package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**Finding id conventions**: finding_id is the independent finding-record ID; finding_node_id is the exploration-node ID. The id returned by list_findings / list_task_findings / node_detail / get_task_node_detail stays the exploration-node ID; read the independent number from finding_id in the same response. get_finding_traffic / bind_finding_traffic take the independent finding_id. The legacy update_finding_report finding_id parameter still takes finding_node_id. Do not use the number on report_finding's first line for evidence tools, and do not guess other numbers after an id mismatch."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default the report agent verifies and binds traffic before writing the report. The reporter keeps verification commands, key output, existing real traffic IDs and their purposes in evidence for the report agent to check against execution records; no extra packet lookup is needed for binding. Explicit immediate binding is also supported: traffic_refs or evidence_hint_id can submit verified references, the latter reading the structured references of a hint in this task; if any is invalid the whole report fails. TCP/no-packet cases need none of these optional parameters. The returned finding_id and finding_node_id denote the independent record and the exploration node respectively."
		case "add_hint", "add_task_hint":
			note = "\nWhen handing over a confirmed finding, keep the verified traffic's IDs, purposes, notes and order in the corresponding hint's traffic_refs (single at top level, batched in the matching hints element), and state in text which specific vulnerability it proves. Callers must not hand over only text while dropping existing traffic references. Unverified candidates cannot be passed as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Traffic evidence handover (optional)**: automatic binding is done by default by the report agent after the finding is recorded and before writing the report. The reporter should keep verification commands, key output, existing real traffic IDs and their purposes in evidence, with intent_id on the task, so the report agent can trace back; no extra packet lookup is needed for binding. When Auto / Planner reports on behalf of the executor, do not drop references the executor already has. add_hint / add_task_hint can hand over via traffic_refs; explicit immediate binding remains compatible with report_finding's traffic_refs / evidence_hint_id. For TCP or no-packet cases, register normally — do not guess IDs, and do not re-probe just to fill in packets."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nWhen a platform conversation has no task context, do not call report_finding directly; hand over via add_task_hint to the existing matching task, let its agent register it, and verify with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore judging a goal complete, finish reporting/handing over this round's existing evidence. Do not end the task or cancel workers just because a textual finding was recorded while evidence handover is unfinished; no-packet cases do not require waiting or forced capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Auto-associate traffic before reporting (enabled)**: you are responsible for verifying and binding traffic for the finding that triggered this run, then writing the report. First obtain the explicit finding_id and finding_node_id from report_finding's returned JSON or get_task_node_detail / list_task_findings. Read the finding details, the corresponding intent's execution records and the existing evidence list; prefer the real IDs handed over by the reporter. If this verification was HTTP and traffic tools are available, use traffic_search to filter candidates, then verify one by one with traffic_get that the request/response truly supports the finding; domain and time are filters only, not proof of attribution. Associate confirmed evidence in reproduction order via bind_finding_traffic(finding_id, traffic_refs), choosing baseline / proof / verification / supporting and stating each purpose. Operate only on this finding — do not re-create it or re-probe the target. After a successful binding, call get_finding_traffic again for the latest version, read the needed bodies, and pass the actually-read version as evidence_version to update_finding_report (whose finding_id parameter still uses finding_node_id). Do not re-append existing bindings. For TCP, uncollected traffic, unavailable tools, or no exact match, skip auto-binding and write the report normally from textual/command evidence, stating why; do not guess to make up traffic. Do not claim success when binding fails; keep existing evidence and explain the unbound reason in the report."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: verified traffic references corresponding to the specific vulnerability in this hint, order preserved; after handover report_finding can carry them via evidence_hint_id.", "items": obj(map[string]any{"traffic_id": str("Real traffic id"), "role": str("baseline / proof / verification / supporting"), "note": str("What conclusion this traffic supports")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must be a hint node of this task (inherited hints cannot be used for binding directly)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
