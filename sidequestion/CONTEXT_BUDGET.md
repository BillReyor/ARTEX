# Bypass long conversations and context budgeting

2026-09-11 Fixed. The original implementation directly regards the number of characters in the requested JSON as a token and inherits the 32K output reservation of the main task. Therefore, when there are many HTML, JS and tool results, the normal bypass problem will be rejected in advance.

## Open source implementation verification

- [Grok CLI bypass context] (https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739): Extract fragments from recent user and assistant texts. The character budget is about 2000, and each fragment can be intercepted at most 400 characters. No continuous bypass question and answer history is maintained on this path.
- [Grok CLI independent request] (https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts): independent cancellation signal; the output limit is 2048 tokens when the model supports it; no tools are provided.
- [Grok CLI Main Session Compression] (https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts): Estimate tokens, retain recent content, update new content into old digests, and handle cross-round truncation.
- [OpenCode Session Compression] (https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts): Full request estimation, output/buffer reservation, recent content plus rolling summary, toolless summary request; this implementation defaults to a recent budget of 8000 and a summary output limit of 4096 tokens.
- [OpenCode Overflow Recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts): Overflow recovery is attempted only when the assistant output has not started, and calls after recovery will no longer enter the same overflow recovery path.

ARTEX borrows from independent output budgets, recent content and rolling summaries, and limited recovery. Maintain the structured messaging and tool pairing of norma v0.3.6 and do not copy Grok's text excerpts; do not write OpenCode's main session compression events into the ARTEX main transcript.

## Request budget and execution

- The message follows norma's UTF-8 byte estimation and 4/3 margin by content block; additional system prompts, tool schema and message encapsulation overhead are included. Estimates are not model-exact token counts.
- The bypass output defaults to a maximum of 8192 tokens, which does not exceed the output upper limit set by the main configuration. Available service environment variable `ARTEX_BTW_MAX_OUTPUT_TOKENS` sets an upper limit of 256–32768; does not modify the product default model or main task parameters.
- The input budget is the context window minus the output upper limit and safety margin; the unknown window uses the platform default of 200K. The safety margin is 5% of the window, with a minimum of 128 and a maximum of 8192 tokens.
- A maximum of 20 groups of successful questions and answers can be loaded in each batch in ascending order. A maximum of 20 sets of original texts are retained, and their token budget is at most 1/4 of the input budget and does not exceed 16K.
- Oversubscribed Q&A updated to a rolling summary. The summary contains historical sources and context time; the historical assistant's answer is not equivalent to new tool evidence, and in the event of conflict, priority is given to the latest master snapshot.
- When the main context is still too long, only old messages in the digest copy, up to 8K tokens in the recent past, will be retained; interception points will not separate tool calls and results. Single groups that are too large will be entered into the summary as a whole.
- The digest input is UTF-8 securely divided according to the actual remaining window, and the output limit is 2048 tokens; empty digests, truncation, tool calls, or exceeding the digest budget will not write the cache. A single bypass is limited to a maximum of 12 digest calls, subject to the same 120-second timeout; reaching the upper limit will explicitly fail and will not loop indefinitely.
- If the model returns a context that exceeds the limit for the first time and has not yet output text or tool calls, it will be retried at most once after further reduction; if the estimated size does not decrease, recovery will stop immediately. Other model errors and partially streamed output do not trigger this recovery.
- All acquired usage, including summary, failed attempts, and canceled usage, is accumulated for the same bypass request. Providers that do not return usage can still only record zero and cannot disguise estimates as actual usage.

## Persistence and interface

`side_question_sessions.memory` Saves old Q&A summaries, overridden ordinal, and main context summaries cached by snapshot identity. `side_question_requests.context_info` Saves preparation phase, actual number of playback groups, summary usage, and budget estimate.

The digest is only saved while the original request is still running and the cleaned version matches; clearing also clears the cache, and late writes will not restore the cleaned data. New snapshots do not reuse old snapshot digests. The summary field is saved with the v3 task archive; when restoring the old v3 missing fields, fill in the blank objects, and v1/v2 will continue to be compatible.

POST accepts and returns the request first, preparation and compression are performed in the background, and no admission locks or database transactions are held. SSE/history displays the preparation, Q&A, compression copy, and answer phases; compression failure is saved as the failed final state of the bypass request. The front end retains errors and restores the draft of this failed issue, without the need for floating notifications to block the input box; historical polling no longer clears submission errors.

## Verify records

- 19/20/21/50 group playback, retention of old conclusions across 20 groups, summary cache restart reuse: automated passed.
- Ultra-long Chinese answers and code context, chunked request budget, tool pairing, snapshot unchanged, new snapshot cache invalidation: automation passed.
- Summary failure/cancellation/truncation/overlong/tool return, clear competition, call limit, only one overflow recovery and no retry for some flows: automation passed.
- Paging, restarts, v1/v2/v3 archiving, summary cache and budget metadata archive recovery for standalone PostgreSQL, old v3 missing new fields, 20 parent sessions sharing four concurrent quotas: Passed.
- Go candidate service builds, front-end TypeScript checks, Biome checks for modified components, and Next.js production builds in separate directories: Passed.
- Built-in browser using standalone UI fixtures, validation grooming phase at 1280×720 and 390×844, summary range prompts, draft recovery after failure, no hover error notifications, no horizontal overflow and no console errors. Temporary clamps have been removed.
- Read-only playback of existing Worker snapshots on this machine passes the new budget check; for example, the 293085 character snapshot of Worker #3 is no longer rejected by a local character count misjudgment. There are no external model calls for this item.
- This real conversation test of sending private Worker snapshots to Grok was rejected by the automatic approval review, was not executed, and was not considered a passing item.
- 2026-09-11 00:37 Restart the local backend according to user requirements, and continue to use the original database, data directory and login configuration. The run file is SHA-256 consistent with the candidate binary; `/api/health` for both the backend and frontend agents returns OK.

Verification command (only using standalone testing library):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

Front-end production builds use a separate copy to avoid overwriting the currently previewed `.next`. The candidate service is located at `/private/tmp/artex-btw-budget-candidate` and has been copied to `/private/tmp/artex-btw-preview/artex` and started; the original binary backup is `artex.before-context-budget` in the same directory.
