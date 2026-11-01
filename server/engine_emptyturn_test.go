package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// To identify and continue the idle round (only thinking, no text and no tools), see steerHooks.Stop.

func assistantThinking(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: text, Signature: "sig"},
	}}
}

func TestIsThinkingOnlyTurn(t *testing.T) {
	toolUse := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "先扫端口"},
		{Type: llm.BlockToolUse, ID: "t1", Name: "run_nuclei"},
	}}
	cases := []struct {
		name string
		msgs []llm.Message
		want bool
	}{
		{"仅思考", []llm.Message{llm.UserText("开始"), assistantThinking("想想")}, true},
		{"思考+工具", []llm.Message{llm.UserText("开始"), toolUse}, false},
		{"思考+正文", []llm.Message{assistantThinking("想想"), {
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("结论")},
		}}, false},
		{"正文只有空白字符", []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("  \n ")},
		}}, true},
		{"完全空的 assistant 回合", []llm.Message{{Role: llm.RoleAssistant}}, true},
		// The tool result is the user role, and the judgment must be traced back to the assistant before it, rather than a nearby misjudgment.
		{"最后一条是工具结果", []llm.Message{toolUse, {
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1"}},
		}}, false},
		{"没有 assistant 消息", []llm.Message{llm.UserText("开始")}, false},
		{"空历史", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isThinkingOnlyTurn(c.msgs); got != c.want {
				t.Fatalf("isThinkingOnlyTurn = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeHooks is a programmable inner HookRunner used to verify that steerHooks respect inner decisions.
type fakeHooks struct {
	prevent  bool
	blocking []string
	msg      string
}

func (f fakeHooks) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return false, "", nil
}
func (f fakeHooks) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (f fakeHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return f.prevent, f.blocking, f.msg
}

func TestSteerHooksStopNudgesEmptyTurn(t *testing.T) {
	empty := []llm.Message{assistantThinking("我应该先枚举子域名")}

	t.Run("Inject continue command into idling round", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges, label: "worker-1 · #1"}
		prevent, blocking, _ := h.Stop(context.Background(), empty)
		if prevent {
			t.Fatal("Idle rounds should not be hard stopped")
		}
		if len(blocking) != 1 || blocking[0] != emptyTurnNudge {
			t.Fatalf("blocking = %v, want [emptyTurnNudge]", blocking)
		}
	})

	t.Run("Do not intervene when there is text or tools", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		normal := []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.TextBlock("已完成扫描，未发现开放端口")},
		}}
		if _, blocking, _ := h.Stop(context.Background(), normal); blocking != nil {
			t.Fatalf("The normal ending was misjudged as idling: %v", blocking)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("It should not be counted when not involved, got %d", n)
		}
	})

	t.Run("Release and end after reaching the upper limit", func(t *testing.T) {
		const limit = 5 // The user sets the "number of empty response retries" to 5
		h := steerHooks{nudges: &atomic.Int64{}, limit: limit}
		for i := 1; i <= limit; i++ {
			if _, blocking, _ := h.Stop(context.Background(), empty); len(blocking) != 1 {
				t.Fatalf("The %d time should still be within the quota, blocking = %v", i, blocking)
			}
		}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("Still injecting after exceeding the upper limit: %v", blocking)
		}
	})

	// "Number of empty response retries" is configured with -1 = this layer is turned off, emptyTurnNudgeLimit resolves to 0.
	t.Run("Do not intervene when configuration is closed", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: 0}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("Closed and still injecting: %v", blocking)
		}
	})

	t.Run("inner determines not to superimpose during a hard stop.", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{prevent: true, msg: "guard 拒绝收场"}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		prevent, blocking, msg := h.Stop(context.Background(), empty)
		if !prevent || msg != "guard 拒绝收场" || blocking != nil {
			t.Fatalf("The hard stop of inner is rewritten: prevent=%v blocking=%v msg=%q", prevent, blocking, msg)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("Giving way to inner should not consume quota, got %d", n)
		}
	})

	t.Run("inner does not overlap when it is about to continue running.", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{blocking: []string{"guard 的续跑理由"}}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		_, blocking, _ := h.Stop(context.Background(), empty)
		if len(blocking) != 1 || blocking[0] != "guard 的续跑理由" {
			t.Fatalf("The continuation message of inner has been rewritten: %v", blocking)
		}
	})

	t.Run("The behavior remains unchanged when no counter is installed.", func(t *testing.T) {
		h := steerHooks{limit: defaultEmptyTurnNudges} // For example, other call points in the future forget to pass nudges.
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("Should not be injected when there is no counter: %v", blocking)
		}
	})
}
