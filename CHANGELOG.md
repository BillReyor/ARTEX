# Update log

Important changes of this project are recorded in this file, and the format refers to [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/).

## [Unreleased]

### Interception

#### New features

- **New built-in interception rule "Delete class interface path"**: The previously built-in HTTP destructive rules only recognized the DELETE **method** (`curl -X DELETE`, `requests.delete(`, `method:'DELETE'`), and the vocabulary of the path class rule is only `/clear /wipe /flush /purge /truncate /drop /destroy /factory-reset /reset-all` - and the deletion interface of most applications can be triggered with GET/POST, so `curl 'http://t/api/user/delete?id=1'` This type of call does not hit any built-in rules and will actually delete the target data. Now add a `deny` rule to cover `/delete /del /remove /unlink /erase /destroy` (allow `/deleteAll`, `/delete_user`, `/delete-user` Such suffix form), the verb must be followed by a separator, `/delivery`, `/details`, `/delta`, `/delegate` will not be blocked by mistake. The rules have independent seed tags, which will be obtained after **existing instances are upgraded. Like other built-in rules, they can be deactivated or deleted in "System → Command Interception".
### Vulnerability push

#### New features

- **Vulnerability discovery supports pushing to IM**: Added "System → Notification Push" page, which can push scanned findings to six channels: DingTalk, Feishu, Enterprise WeChat, general Webhook, Telegram, and email**. You can configure any number of robot instances of the same type (such as one DingTalk robot each for "Emergency Response Group" and "Daily Broadcasting Group"), and each instance can independently set start and stop, current limiting and filtering rules.
- **Push timing is divided into two modes: real-time and summary**: in real-time mode, hits are sent one by one; in summary mode, a batch of vulnerabilities are merged into one message according to the global cycle (default 30 minutes), and "M new vulnerabilities have been added in the past N minutes" and the level distribution are given at the beginning, making it easy to judge at a glance whether they need to be processed immediately. If you want to do "high-risk real-time and other summary", you can build two channels and configure them separately. The strategy should not be hard-coded in the code.
- **Filtering rules support four dimensions**: lowest severity level, limited task/asset scope, vulnerability type keyword inclusion and exclusion (exclusion priority), and whether to accept changes in disposition status (off by default, because most people refer to "push" as discovering new vulnerabilities, not status logs). A single message brings back a "View Details" button to jump to the vulnerability details. The target address is configured by the global "Return Address". If left blank, there will be no button.
- **Delivery history and manual resend**: The notification page lists the status, number of attempts, failure reasons and channels of each delivery, and can be filtered by channel and status; failed items can be resent with one click (resending will clear the retry count, because manual resending means that the cause of the failure has been dealt with).
- **Channel Credential Mask Echo**: Webhook address, signing key, Bot Token, SMTP password, etc. are declared by each channel (`SecretKeys()`). The interface only echoes the mask value with the tail number prompt; returning it as it is when submitting means "no modification". Clearing the input box will delete the field.

#### Fixed issues

> This section records the conclusions of a round of security audits after the first version of the function was completed. Each item is reproduced first, then repaired, and then retested for regression.

- **Fix mask can be bypassed by "Change target address, keep credentials" (serious)**: The purpose of the mask mechanism is "credentials are not echoed to the browser", but the target address and credentials are two sets of independent fields, and the configuration merge will retain the original value of "unmentioned keys" in the library. So by just changing the address and avoiding talking about the credentials, the server can send the real credentials in the library to any address - the `Authorization` header of the general Webhook, Telegram's Bot Token (into the request path), and the SMTP password of the email channel (handed to the peer after STARTTLS) are all leaked, completely silently and without relying on redirection. This path is feasible in actual tests and has been repeated through the four channels one by one. It is now stipulated that: **As long as the target address changes, the caller must explicitly state each credential field** (give a new value, or explicitly leave it blank to indicate that it is no longer needed) - returning the mask value as it is is equal to "inherit the old credentials", which is exactly the shape of the attack and should be rejected. Deliberately do not "automatically discard credentials", because for optional credential fields (such as `headers`), it will become "authentication is silent but the interface returns success", which is more difficult to troubleshoot than an error.
- **Fix markdown-based channels for zero-escaping untrusted content**: DingTalk, Enterprise WeChat, and Feishu did not do any escaping before (both Telegram and Email did this). The vulnerability title and summary come from the model output (the model reads the response of the target under test), and the `url` of the asset is the complete URL obtained by scanning and containing the target's controllable query string. `Login SQL injection\n[Urgent: verify your account here](http://attacker.tld)` will be rendered as a **clickable external link** in the security engineer's DingTalk/Feishu; `![](http://attacker.tld/beacon)` will be pulled by the client during rendering - which is equivalent to notifying "this vulnerability has been seen" and revealing the reader's IP. Now unified single line + markdown metacharacter escaping is used. At the same time, a related design error has fixed been: escaping must occur in the respective rendering exits and cannot be put into the title function shared by the four contexts (markdown escaping leaking into Telegram's HTML will leave visible backslashes).
- **Fix the SSRF side of the delivery address**: Previously, only the scheme and host, `169.254.169.254` (cloud metadata, instance credentials can be read out), loopback address, and intranet address can be delivered; when delivery fails, the first 200 bytes of the response body will enter `last_error` and be echoed by the delivery history interface, forming a semi-blind reading primitive (the first 200 bytes of any intranet HTTP endpoint response can be read) bytes). Now fortify in the **dial-up phase** (instead of just verifying when saving the configuration) - that is the final effective point, covering DNS rebinding and same-host redirection, and rejecting cross-host redirection (the credentials of these companies are in the URL, and following the jump is equivalent to handing over to the jump target). Loopback and link local addresses need to be explicitly set to `ARTEX_NOTIFY_ALLOW_LOCAL=1` before they are allowed (the local SMTP relay is a legal configuration and cannot be one-size-fits-all); **RFC1918 private network is deliberately allowed** because it is common for self-built Mattermost/SMTP relays on the intranet to achieve that level of protection, which will invalidate the normal deployment.
- **Fix push credentials leaked via error message**: When delivery fails, the `*url.Error` returned by `http.Client.Do` will enter the **complete URL** into the error text, while DingTalk's `access_token`, Enterprise WeChat's `key`, Feishu's hook id, and Telegram's `/bot<token>/` are all **right in the URL**. Therefore, the string flows into four exits: `notification_deliveries.last_error` clear text is dropped into the database, the original echo of the delivery history interface (bypassing the mask configured in the channel), the server log, and the error message returned to the front end by the test sending interface. Now unified desensitization - the error message only retains `scheme://host` and the underlying cause (enough to locate DNS/connectivity/certificate issues), and the path and query are discarded. The error text of the address verification itself (`url.Parse` failed) also contains the complete address and is processed in the same way as above; **Only the former was repaired in the last round, but the latter was missed, and the regression use cases at that time all took the scheme branch and did not cover the parsing failure path at all, which is a false guarantee** - now the use cases that actually cover this branch have been added.
- **Fix the silent loss caused by the entire batch being marked as delivered after the summary message is truncated**: Channels have upper limit lengths (Qiwei is the tightest at 4096 bytes). When a batch cannot be loaded, the message will be truncated, and after the delivery is successful, the entire batch of deliveries will be marked as delivered - the truncated ones are neither in the message nor in the failure list. The delivery history still shows success, and the vulnerability disappears. Now it is changed to package by **whole message**: only those that are loaded into this message are marked as delivered, and the rest are returned to the queue to wait for the next message to be sent, and the message header truthfully states "This message displays the first N messages, and the remaining M messages will be continued in the next message." Postponed entries do not consume retries (the optimistic +1 at claim time will be reduced), otherwise a 500-entry backlog would fail the tail entries in the third period - and they never go wrong.
- **Fix the typo in the lowest level threshold that will cause the filter to fail silently**: When `min_severity` is written as `hgih`, the unknown level is 0 in the ordinal table, and the judgment is degraded to `rank >= 0`. Constant truth - the user thought that "only high risk" was restricted, but actually poured all the vulnerabilities into the group, and the interface was completely indistinguishable from the correct configuration. The value is now verified on the write path, and the error message lists the optional values ​​(the read path remains tolerant: bad values ​​already in the library will not prevent the entire channel from being read).
- **Fix `rate_per_min = 0` (unlimited) unreachable**: Documents, interface prompts and token buckets all interpret 0 as "unlimited", only the writing library layer writes `if RatePerMin <= 0 { useDefault() }`, quietly changing the explicit 0 to 20 (DingTalk/Qiwei/Telegram) or 100 (Feishu) - the operator thought the current limit was released, but was actually stuck without any prompt. The The difference between "unspecified" and "explicit 0" can only be expressed by the request body, so the default value is changed to the interface layer to fill in the field by default.
- **Fixed summary channel to completely bypass token bucket**: `allow` was deducted by `takeTokens` but no one used it, `rate_per_min` has no effect on digest mode. The number of items received is now subject to both the "current round quota" and the upper limit of memory.
- **Repair failure treatment is judged based on the maximum number of attempts in the entire batch, and old deliveries are allowed to be combined with new ones**: The number of attempts for each item in the batch is not the same. An old delivery that has been retried twice will drag the new delivery in the same batch into failed - the new vulnerability will be lost permanently if it is not used after one retry, which is exactly the opposite of the original intention of "preventing the old line from dragging the new line into the water". Now decide one by one: immediate death if permanent failure, death if the number of retries is exhausted, and the rest will be rearranged according to their respective retreat levels.
- **Fix fix for unresolved snapshot deliveries in rollup batches being silently marked as successful**: such entries are skipped by the rendering phase (without dragging down the entire batch), but then count them all together as successful once the entire batch has been marked as delivered. Now they are explicitly failed and the reason is given, which can be found in the delivery history.
- **Fixed the problem that the number of items delivered in each round of a single channel may exceed the lease time**: the lease is 3 minutes, and if the number of items that can be serially delivered in one round is so large that it takes longer than the lease at worst, the peer will re-acquire the rows with expired leases in multi-instance deployment, send them repeatedly, and double-increment the number of attempts. Now press "Lease/Single Timeout" to deduce the upper limit of each round, and there is an assertion to nail the relationship between these three constants (when I wrote this assertion, I immediately found that the original value of 6 happened to be full of leases and zero margin, and had been adjusted to 5).
- **Fix Telegram truncation that may cut off HTML entities**: truncation only avoids half of the tag, but does not avoid the severed entity fragments such as `&amp`, and the parser may reject the **entire** message because of this - extremely long summary messages are already common and the cost is too high. Now avoids both open tags and solid fragments.
- **Fix the mail channel to treat temporary SMTP failures as permanent failures**: SMTP 4xx (such as greylist `450`) is a temporary rejection, the normal practice is to retry later, and before that, it will be a permanent failure - a mail server with greylisting enabled will make **every** push fail after the first try, and this is exactly the scenario where automatic retry should work best. Now distinguish according to the first position of the response code: 4xx can be retried, 5xx is permanent failure, and when the code cannot be obtained, it can be retried.
- **Fix that when submitting a nested structure, the mask literal will actually be written into the library**: Object fields like `webhook.headers` can only be masked or submitted as a whole; inserting the mask sentinel into the object cannot express "remain unchanged", and will be stored as a real value, causing subsequent authentication to silently fail without any error. This commit is now explicitly rejected.

#### Design instructions

- **The transaction that writes the vulnerability only performs one blind INSERT**: `notification_events` is written by `RecordFindingTx` in **the same transaction**. Submission ensures that the "vulnerability is dropped into the library" and the "push task exists" are atomically consistent. This INSERT deliberately does not read the channel table or run the user's filtering rules - otherwise a mismatched filter condition can contaminate or even terminate the transaction, preventing high-risk vulnerabilities from being stored in the database. In order to isolate the failure to this statement (an error in any statement within the transaction in PostgreSQL will invalidate the entire transaction and even `COMMIT` will fail), it is wrapped by `SAVEPOINT`. When it fails, it will only be logged and will not affect vulnerability writing.
- **Delivery uses lease acquisition instead of long transactions**: After `FOR UPDATE SKIP LOCKED` is acquired, the row is set to `sending` and `next_attempt_at` is pushed to the future as a lease. Network delivery is performed after the transaction is submitted, so the database lock is not held during the delivery period; the `sending` row left by the process crash will be reacquired in the next round after the lease expires, which is self-healing and will not cause infinite retries.
- **Current limiting does not consume the retry budget**: The engine first calculates how many messages can be sent in this round according to the channel token bucket, and then collects them according to this number. If the order is reversed (lead first and then abandon), the delivery blocked by the current limit will be counted as one attempt, and the three budgets will be exhausted by pure waiting and then fail. If the limit is exceeded, the message will not be lost, and the delivery will only be postponed to the next tick.

### Test infrastructure

#### Fixed issues

- **Fix the permanent residual assets caused by the cleanup sequence of the company's ICP attribution use case**: The cleanup of this use case is registered in `t.Cleanup`, but the connection is closed using `defer d.Close()` - `defer` is executed first when the function returns, followed by `t.Cleanup`, so the cleanup statements all fall on the **closed connection**, and the error is `_, _ =` Discarded, the test assets and company remain in the library permanently. It uses `MAX(companies.id)+1` as a fake TaskID to mark assets. Once this number collides with the task ID of other use cases, the use case that asserts "exactly N assets" will fail inexplicably and is extremely difficult to locate. Now instead of closing the connection, go to `t.Cleanup` and register it first (last in, first out, ensuring cleanup runs first), and at the same time, make cleanup failures visible instead of being swallowed. The same pattern (`defer d.Close()` + writing the library in `t.Cleanup`) has more than ten places in the `db` package. This time, only the one triggered by the evidence has been repaired.

### Traffic

#### New features

- **Add "Clear All" to the traffic list**: delete all traffic records at once, ignore the current filtering conditions, and additionally clean up the historical host directories that are no longer recorded in the index, leaving no residue. After clearing, do a full compaction (`optimize` + `VACUUM` + `wal_checkpoint(TRUNCATE)`) to return the disk space occupied by the index to the system. After completion, you will be prompted with the actual amount released. Traffic evidence bound to vulnerabilities is stored in a separate evidence library and is not affected. `VACUUM` on an empty database has almost no cost, so this is also a way for existing instances to convert the index database into incremental recovery mode - after clearing it once, you can reclaim the space by yourself by pressing host to delete it daily.

#### Fixed issues

- **Fixed the problem that disk space is not released after deleting traffic**: deletion only makes the data invisible, and the space remains in the index file. SQLite row deletion only puts the page in the freelist, but `auto_vacuum` is not enabled when the index database is built, and the file never shrinks; at the same time, `ex_fts` is the full-text index of `contentless_delete`, and `DELETE` only writes tombstones without recycling the original postings, which accumulate permanently without merging - deleting traffic makes the index larger. Since all texts below 256KB are inlined in this library, and the trigram index is about 2 times the text volume, instances with a large amount of captured packets will occupy several times the disk for a long time for long-deleted traffic (actually measured captured 6MB text → index 16MB, still 16MB after deletion). Now, `auto_vacuum=incremental` is directly enabled in the new index library. After each deletion and submission, "full-text index incremental merge + `incremental_vacuum` + `wal_checkpoint(TRUNCATE)`" is performed in the background in blocks, gradually returning the space to the operating system (the same scene drops back to 104KB after deletion). Recycling is performed in blocks and write locks are released between blocks, without blocking traffic recording; the process is immediately released when it exits, and the remaining work will be continued the next time it is deleted.

> Upgrade instructions: `auto_vacuum` can only be set when building the database, so **the index database of an existing instance is still in the old mode**, and `incremental_vacuum` is a no-op on it - a line of prompts will be printed when starting. After upgrading this type of library, the continuous growth of tombstone has stopped (full-text index merging is performed as usual), and the occupied volume can be recycled once by using "Clear All" in the traffic list - that step will convert the library into incremental recycling mode, and daily deletions will take effect automatically thereafter.

### LLM

#### Fixed issues

- **Fixed the problem that custom session headers do not take effect on the "one-time LLM call" path**: The header value of the session header is taken from the session id on the request context, and the id is written to the context by agentcore only when the transcript store is mounted. Target disassembly (round 0) and cold node compression (§4 body call) are both one-time calls without a store. There is no session id on the context, and the gateway cannot read the header. For endpoints such as opencode zen that "miss `x-opencode-session` directly 400 MissingSessionID", the performance is "0th round target disassembly 400 failure, subsequent planner The round is completely normal", and the compression failure only leaves one line of log, which is extremely difficult to correlate. Now the session ids (`exp<N>-goals` / `exp<N>-compactor`) that are stable for exploration are explicitly attached to these two paths: this not only allows the header to be used normally, but also allows llmrec to correctly attribute the token usage of these two paths to the corresponding exploration (which could not be remembered at all before).

### Discover

#### Fixed issues

- **Fixed the problem of no scroll bar after the asset list in the "By Asset" view overflows**: The asset tree on the left has a scrolling container, but the outer card only gives `max-height` without determining the height. The scrolling viewport is based on `height:100%` and cannot parse the height (only `max-height` and `height` are set in the CSS and are still `auto` The percentage height does not take effect), so when there are many assets, the list will either burst the card, or be truncated and cannot be scrolled. Now the height limit is directly placed on the native scrolling container of the asset tree (`overflow-y-auto` + `max-h`, adaptive with the window height). When the assets are small, the card shrinks with the content, and when the assets are large, the card is capped and the scroll bar is displayed.

## [0.3.14] - 2026-09-24

### Task list

#### New features

- **New "Vulnerabilities" column is added to the task list**: The number of severe / high / medium / low is displayed according to the severity level. Non-zero levels are colored according to the severity. You can see the scale and distribution of vulnerabilities of each task at a glance.

#### Modified functions

- **The width of the "Description/Target" columns has been narrowed**: Overlong content is omitted from display, and the full text can be viewed by hovering the mouse to reduce the horizontal space occupied by long text in the list.

### Overview of exploration map and planning situation

#### Modified functions

- **Streamline the planning situation overview (graph_overview) and set an upper limit on the number of each list**: Recent facts, ended intentions, to-do intentions, cold zone summaries and confirmed vulnerability details are all changed to "latest window + count bottom", and those not shown can be queried on demand; significantly reducing the LLM context volume of each round and avoiding the expansion of long task contexts (cold zones associated with task overviews are also limited).

#### Fixed issues

- **Eliminate inverse duplicate edges between digest and finding in the exploration graph after folding cold nodes**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.13] - 2026-09-19

### Asset interception

#### New features

- **Added global asset interception rules (blacklist)**: supports the entry of congruent and fuzzy matching domain names/IP/URLs and CIDR network segments, and provides the addition, deletion, modification, checking and activation/deactivation of rules. The management page is located in "System → Asset Interception"; the default built-in fuzzy interception of government (`.gov` / `.gov.cn`) and education (`.edu` / `.edu.cn`) websites.
- **Asset interception access execution chain**: Agent first makes an interception judgment on the target asset before issuing the intention (`add_intent`) and inserting the asset (`insert_assets`) - the intent that hits the interception will not be issued, the asset that hits the interception will not be inserted, and the asset information and interception reason will be returned to the Agent.
- **New task-level interception/allowance (whitelist) rules**: independent of global rules, only effective for this task, the judgment order is "intercept first, then allow" - if the interception is hit, it is prohibited; if the interception is not hit, but the allow rules are configured for this task and none of them hit, then "testing is not allowed"; when no allow rules are configured, the whitelist is not enabled. You can enter it when creating a task, or you can add, delete, modify, and enable/disable in the task details "Overview".
- **Task templates support preset categories and task-level blocking/allowing rules**: Templates can save task categories and a set of task-level rules, and bring them into the new task form when applying the template.

### Operation review

#### Fixed issues

- **Tighten the model referee output protocol to reduce erroneous releases caused by truncation**: Lower the upper limit of the verdict description (comment) from 500 Chinese characters to 120 Chinese characters and force "only output JSON without preface or code block" to prevent the verdict from being unable to be parsed after being truncated by `MaxTokens`, and then being released according to the model failure strategy (fail-open).

### Task archive

#### Fixed issues

- **Skip when archiving encounters a symbolic link instead of failing the entire package**: The archive format only supports ordinary files and directories end-to-end. Previously, any symbolic link in the working directory would cause the entire task to fail. Now, the symbolic link is skipped and logged, and the remaining files are archived normally (no links are followed, and no directory tree is left).

### Accounts and Compliance

#### New features

- **Added "Usage Instructions and Disclaimer" pop-up window before logging in**: You need to check the box to agree before logging in.

### Licensing and Dependencies

#### Modified functions

- **The project adopts the AGPL-3.0 open source agreement**, and the README license and disclaimer instructions are improved.
- **Upgrade norma to v0.4.1**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.12] - 2026-09-17

### Explore the link bulletin board

#### New features

- **New "Exploration Link Announcement Board" is added to the task details page** (#144): The exploration nodes of the task (starting point/goal/intention/facts/vulnerabilities/hints/compression) are presented in a timeline perspective, supporting filtering by type, keyword search, forward/reverse order, paging and automatic refresh; when in the "first page with the latest", it is a live broadcast position, refreshed in each round, when leaving this position, only the unread count is accumulated, the current reading is not disturbed, and "N new broadcasts· Return to the latest" with one click. Grouped by day, you can recognize "which day this happened" even when turning pages for long tasks.
- **Each line of the bulletin board displays the node id** (#145), making it easier to explore the link diagram and locate specific nodes.
- **Broadcast board node details are expanded to display upstream and downstream and anchored assets** (#147): Expand a broadcast to see the upstream (resulting from)/downstream (resulting from) relationship of the node. Hover the mouse over the associated entry to pop up the business card of the corresponding node (type/status/source/time/summary/payload fragment); and incidentally list the assets anchored by the node (type label + identifiable text). Relevant data is delivered together with the broadcast page, and no additional request is required for expansion.
- **Broadcast board search supports filtering by node id** (#150): The search box adds node id matching in addition to "Content/Source". Enter pure numbers or the "#41" format displayed on the interface to accurately locate the corresponding node.

### Intent Management

#### New features

- **Intent deletion supports two modes: "fake deletion/real deletion"** (#149): pending, running, and paused intentions can be deleted. The reason for deletion needs to be filled in; the deletion mode is selected in the confirmation pop-up window.
- **False Delete (Default)**: Set the intention to "Deleted" and record the reason in a separate field, retaining the intention node and all its output and lineage.
- **True deletion**: Physically remove the intent and the exclusive descendant nodes "supported only by it" (cascaded to leaves along the output/intent chain) to avoid leaving orphan data; the Token measurement of the deleted intent is archived and retained according to the original date; shared nodes (also referenced by other intentions), target and task root facts are all retained, and the true deletion pop-up window will display the number of nodes expected to be affected by the cascade.

Both modes will notify the planner that "the intent was deleted by the user + the reason" and re-plan accordingly.

### Operation review

#### New features

- **Approval records support filtering by status and judgment source** (#139): Approval records can be filtered by approval status and judgment source to quickly locate target records.

#### Fixed issues

- **Pending approval requests are loaded independently of history paging** (#133): Pending requests are no longer affected by history list paging and are still fully visible when browsing history records.

### Agent

#### Modified functions

- **Worker role description changed to common network security platform representation** (#138).

#### Fixed issues

- **Fixed cold node compaction never triggering**: Connect Compactor to the planner that runs by task. Cold node compression (cold-digest) was never executed because it was not connected. It has now been restored.

### Chat

#### New features

- **Chat supports @ references of multiple types of records and scrolling paging** (#135): Multiple types of records can be @ referenced in chat, and reference candidates support scrolling paging loading.

#### Fixed issues

- **Long message bubbles are constrained within the conversation panel** (#137): Too long message bubbles no longer overflow the conversation area.
- **Fixed the issue of "missing upload file" when uploading files when a new conversation is not created**: Composer's file selection snapshots `FileList` into an array before clearing the input and then calls back; previously, the draft state needed to create a session asynchronously, and the `FileList` bound to the input during recovery execution has been cleared, resulting in missing file fields in the upload and the backend returning 400.

### Traffic

#### Fixed issues

- **Traffic recording proxy only listens to 127.0.0.1 by default** (#129, #130): Avoid open proxies exposed to other network interfaces in the default configuration.

### Web

#### Fixed issues

- **Fixed the global header missing on static export task list page**.
- **Complete the demo vulnerability details page associated traffic mock and fix the white screen**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)
- [@dingpotian](https://github.com/dingpotian)

## [0.3.11] - 2026-09-15

### Operation review

#### New features

- **Approval records support source positioning** (#125): Click the "Source" of the approval record to jump back to the tool execution that triggered the approval - open the original complete session, automatically page and load to the target location, expand the command and results in place, and highlight them in the center of the scroll area. The previous and next messages remain readable; automatic correction stops when the user scrolls manually. Segmented sessions across ordinary conversations, task workers, planners, and main agents are all accurately located through persistent `tool_use_id` and task mapping (newly added in-scope call ID index). When missing, duplicated, or the association is unclear, explicit prompts are given instead of jumping to other executions. Provide clear prompts when sessions are deleted or task archives/records are missing.
- **Approval records support page turning** (#115).

#### Modified functions

- **Streamline model review input** (#124, #125): Model review (LLM referee after rule miss) input converges to versioned JSON of "current complete tool call + explicitly selected short context + local working directory". The background only takes real user messages (the current user messages of the main chat/task Agent). The Worker no longer comes with an intent summary, and the background inherited from the superior Agent is also cleared; the planner and automatically triggered sessions do not re-create user messages. It no longer comes with task descriptions, goals, operational constraints, global exploration situation, historical calls or complete Worker intentions - these are still used for Agent execution and independent session auditing according to the original logic, but do not enter the operational review. All rulings require the output of JSON `decision`/`comment`, and the description must include "actual operations, consequences after success, and hit rules"; instructions in parameters or background cannot change the review strategy. Save the input snapshot and fingerprint actually sent to the model. The old snapshot is retained and marked with the version, and the old input is not reconstructed with the current data.

#### Fixed issues

- **Model rulings are no longer silently released when wrapped in a code block** (#126): If the ruling JSON returned by the model is wrapped in a ``` code block, previous parsing failures will be silently released through the model failure policy; now, the code block fence is stripped off and then parsed. If the parsing still fails, it will be processed according to the configured failure policy.

### Agent

#### New features

- **Added experimental noa context compression** (norma upgraded to v0.4.0): An experimental feature that can be turned on in system settings and is turned off by default. After it is turned on, noa (model-driven context compression) takes over the context compression of the four types of Agents: main Agent, planner, worker, and chat, replacing the built-in compression; the compressed original text persistent archive is concentrated under `<workDir>/noa/<sessionID>/` (not scattered in each directory task), and is globally uniquely divided into directories according to the session ID. If access fails, built-in compression will automatically fall back without interrupting real tasks; the switch is read once per run, and the switch only affects subsequent runs.

#### Fixed issues

- **Exploration graph tools reject instead of nil crash when no task context**: Explicit errors are returned when calling exploration graph related tools without a task context, and no longer crash due to empty storage dereference.

### MCP

#### New features

- **Support for legacy SSE MCP services** (#117): Compatible with MCP servers that only provide legacy SSE transport.

### Traffic

#### Fixed issues

- **Traffic retrieval of hosts matching records by port awareness** (#114): The host matching of `traffic_search` is included in the port to avoid crosstalk between the same host records on different ports.
- **`traffic_search` Description migration errors will no longer interrupt subsequent reporter migrations**: A single migration failure will be isolated and will not affect subsequent migration execution.

### Web

#### Modified functions

- **Separation lines are added to task retest vulnerability options** (#122): Separation is added between retest vulnerability options to make the vision clearer.

#### Fixed issues

- **Fix the full page crash of the demo task details page**: In mock mode, the task details "Session" label will pull `GET /api/tasks/<id>/side-questions` (bypass question history), but the mock handler does not have this route. When the user presses "The path ends with s, it is a collection", `[]` is returned, causing `data.items` to be `undefined`, bypassing the hook. `merge()` throws `TypeError: t is not iterable` on its iteration; this exception occurs in the updater of `setItems`, and is postponed to the render stage by React to rethrow. The caller `catch` cannot catch it, and the entire page is taken over by the error boundary to display "This page couldn't load". Now the mock handler explicitly returns the empty bypass history, and `sideAPI.history` also normalizes the return value (`items` non-arrays are all converted into `[]`) as a defense depth.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.10] - 2026-09-13

### Network

#### New features

- **New DeepSeek official source for network search**: Directly reuse the currently activated LLM configuration. It is different in nature from the other three sources - DeepSeek does not have a directly callable search interface, and the search only exists within its Anthropic-compatible interface (`web_search_20250305` server tool), which is performed by the DeepSeek server. Therefore, it **only supports DeepSeek official endpoint + anthropic protocol** (OpenAI protocol endpoint will directly reject the server tool), and each search will consume **an additional model call**, the request **does not go through the search export proxy**, and is not counted in the traffic trace**, and the returned results **only have titles and links** (no abstract, and will be captured by WebFetch when the text is required). The settings page explains the above restrictions but does not perform verification and interception. It is up to the user to confirm whether they are met. You can use the "Test Search" button to run a real-time verification.

### Agent

#### New features

- **Worker gains cross-work review capability**: Newly added `search_all_worker_traces` (no need to know the intent_id first, search hit steps globally by keyword in the execution process of all work in this task) and `get_worker_trace` (lock the subsequent step flow of a certain work, search by keyword on the spot, and get the complete content by step_id), which are used to reuse observations seen by other work but not written into the fact to avoid duplication of work.
- **`node_detail` is delegated to workers**: With the above review tool, the worker can directly check the complete details of the node after getting the intent_id/node id.
- **The planning round triggered by `add_hint` will be explicitly reported**: Previously, the newly added prompt only folded the hint into the situation overview and let the planner discover it by himself; now `add_hint` will record a "People have added N new strategic tips:..." to the planner (one is counted in batches, and the screen is not refreshed one by one). The planner is clearly informed that "this round is triggered by the new hint" and directly sees the prompt content.

#### Modified functions

- **Global situation prompts are changed to a "relaxed" tone**: Encourage exploration and divergence, and report cross-intention clues in a timely manner, rather than premature convergence.
- **Simplified worker boundary wording**: Being blocked for the first time does not mean exploration. Focus on "exhausting the intended bypass methods before drawing a conclusion."
- `insert_assets` Remove the `related` input parameter of each asset: its only function is to decide whether to add this asset to the scope of this task, and the value will not be lost (the same asset will be invalidated if it is registered again, and it is not clear on the interface who is judged as irrelevant). In fact, it just allows the model to make one more judgment that cannot be retained.
- **The test scope (`task_scope`) is no longer affected by the asset coverage switch**: `insert_assets`'s automatic entry into the scope (`source='auto'`) is executed regardless of whether coverage is turned on or not, and `add_task_scope` is always provided to the planner/main agent within the task/target disassembly agent. The scope is the authorization boundary of the task and the filtering basis for asset query. The coverage switch only determines whether to use it as the denominator to calculate the indicator. It should not determine whether to accumulate the scope itself. Previously, turning off coverage would make the two writing paths `auto`/`agent` invalid at the same time, leaving `task_scope` with only lines manually added on the interface. `list_untested_assets` remains hidden with coverage turned off (itself a pure coverage view).

#### Fixed issues

- **Fix asset cross-linking between tasks** (#59): `list_assets` previously programmed the task id to 0, nakedly checked the entire shared asset library, and the model was unable to write the range filter conditions, so the assets of other tasks (especially IP) were regarded as cost task targets, and the test direction was biased. Now it only returns assets that fall within the test range (`task_scope`) of [this task and directly related tasks], and matches by **attribution** instead of literal value: if there is a certain root domain in the range, all subdomains/services/interfaces under its name can be found, and if there is a certain network segment, the hosts and services in the segment can be found; if you directly retrieve assets outside the range by id, you cannot get them either. Non-task context (Auto/pentest) has no scope to rely on, and still falls back to the entire library. Incidentally, fix the attribution blind spot where the IP directly connected host (such as `http://1.2.3.4/api`) cannot be hit by the network segment range. The UI's Test Assets view (filtered by task producer) is not affected.
- **Fixed the worker cross-work replay tool being deleted by mistake**: After `search_all_worker_traces` / `get_worker_trace` (and `node_detail`) were added to the worker default toolset, they were unbundled by an old migration that "converged the worker tool surface" at startup, resulting in the worker actually not being able to get these tools. They have been removed from the unbinding list, and the libraries that have run the old migration will be added back to workers at one time.
- **Fixed `/btw` side-channel question's occasional failure to enter the library**: side_question checkpoint strips the NUL (`\u0000`) escape that is not supported by JSONB before dropping it into the library to avoid errors when writing content containing this character.

### Trigger

#### Fixed issues

- **Merge trigger session to remove duplicate task descriptions/targets by task**: When multi-task merge is triggered, the task description/target of the same task will no longer be repeatedly spelled into the message to avoid repeated stacking of long targets and overwhelming the context.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.9] - 2026-09-11

### Agent

#### New features

- The main agent within the task supports **multi-session**: contexts can be created, switched, and reset independently, and each session can interact.
- `/btw` that supports persistence **Bypass Questioning**: Do not interrupt the main line to ask questions, the questions and answers will be dropped into the library and will still be there after restarting.
- `spawn_task` Added `source_task_ids`, the new task can **read-only inherit** the assets and discoveries of the source task.

#### Modified functions

- Turn off the worker's cross-engagement memory and converge its default tool surface: reading context and cross-work review are planning responsibilities, and the worker is only responsible for the execution and writeback of a single intention.
- Simplify the default worker prompt words; `get_worker_output` removes the `terminated` and `worker_name` fields; simplify the description of `insert_assets` / `list_assets`.

#### Fixed issues

- Fixed the problem of early interruption of work due to **idle round**: the model sometimes uses a whole round to only output thoughts, neither giving text nor adjusting tools. At this time, the harness sees a natural end (`end_turn` and no `tool_use`), directly ending with `completed` + empty summary - an unfinished intention is interrupted halfway, which is manifested as "the task ends normally but without any text summary." Five layers of LLM retries cannot reach it: it is not an error. The entry point for retrying in the same provider security window is "flow failure". The circuit breaker is directly judged as successful at `err == nil`, and the intention to rerun is only recognized as `model_error`; the SDK's empty response retry is also out of reach, because that layer is judged as "no yield event", and the increment itself is an event. Now the Stop hook of work will recognize this kind of round and inject a continue command, allowing the model to continue to the next step with the thoughts that have been produced - deliberately not selecting "resend as is", because this type of idling is usually determined by the prompt word and context shape, and is a stable behavior rather than random convulsions. Resending will only make the model think again.
- The number of **null response retries** in the "Retry and Backoff" page of the LLM page is reused (both are answering "the model is completed but no substantial content is produced", but the criteria and methods are different). The default is 2 times. Fill in `-1` to turn off and return to the original "idle and end" behavior. It is the upper limit of the **total amount** of an intention rather than the number of consecutive times: the The harness itself is limited. Continuous idling can only be pushed once, and the tool round quota is only refreshed when a tool round actually occurs. Therefore, this number of blocks is a cycle of "tool → idling → push → tool → idling" that consumes the intention budget. Log `[work <worker> · #<intent>] Empty turn; inject continuation instruction (n/N)` when triggered. Design see `docs/LLM重试设计.md` §1.1.
- Fixed the context budget and input layout of `/btw` under long conversations; non-secure context (non-HTTPS access) degradation generates bypass request ID.

### Traffic

#### New features

- Vulnerabilities support **associating multiple pieces of traffic evidence**, which can be sorted, commented, and distinguished by role (request/response/evidence).
- The reporting agent will automatically associate relevant traffic evidence before writing the report.
- Added Agent **traffic binding switch** and completed the handover of evidence between Agents.

### Task

#### New features

- Support **Session-level vulnerability retest**: The retest is run in an independent Agent session, and the running status is displayed in the list and details.

### Interception

#### New features

- Added **Details and Execution Audit** to the approval record: you can view the tool request context, model/rule preliminary judgment, execution output and parameter fingerprint.

### Assets

#### New features

- Mission test assets support **DSL search**.

### LLM

#### Fixed issues

- Fixed the issue where the test connection did not reissue the custom session header, causing opencode zen to return 400.

### Network

#### New features

- MCP's HTTP transmission supports **Skipping TLS certificate verification** to facilitate access to self-signed certificate services.

### UI

#### New features

- The conversation list is grouped by Agent and supports expansion, collapse, and independent pinning; conversations support filtering by Agent.
- Attack link graph rendering compresses (digests) nodes and shrinks its members.

#### Fixed issues

- Fixed a white screen caused by out-of-sync login credentials.
- The interception prompt copy was changed to clearly indicate "Platform Control" to avoid being misinterpreted as defense on the target side.

### Deployment and Update

#### New features

-Support **One-click page update**: A new "Version and Update" card is added to the system configuration page, and a prompt will appear in the top bar when a new version is available. The process is to download the release package → verify `SHA256SUMS` → smoke test → temporary storage → exit and restart by the daemon script to complete the change, and the page will automatically refresh.
- Added the new daemon startup script `start.sh` / `start.bat` as the official startup entrance (the release package and Docker image have been brought, `install.sh` remains unchanged), determine whether to restart based on the exit code, and are responsible for forwarding SIGTERM to artex. The verification and replacement logic are all in Go, and the script remains fool-proof.
- Automatic failure protection: if the verification or smoke fails, discard it and continue running the current version; if the new version fails to start three times in a row, the previous version will be rolled back. The settings page also has manual rollback (note that the database structure will not be rolled back).
- The update only recognizes the GitHub domain name and requires HTTPS. The release source cannot be configured; one-click update is disabled for development builds. GitHub query results are cached for 30 minutes to avoid the top bar prompts from exhausting the API quota.

#### Known limitations

- Under Docker, only the program is changed, not the image: the tool chain will not be upgraded, and rebuilding the container will return the version that comes with the image. `docker compose pull artex` will still be used when needed.
- `skills/` in the release package is not synchronized, and the built-in skills added in the new version will not take effect automatically.
- Restarting after updating will interrupt running tasks.

### Dependencies

#### Modified functions

- Upgraded norma to v0.3.7.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.8] - 2026-09-09

### LLM

#### New features

- A new "Retry and Backoff" tab is added to the LLM page: the **number** and **interval** of five-layer retries can be configured. The failure of a model call is experienced from the inside out through connection establishment retry (SDK, connection reset/timeout/429/5xx before the flow starts), empty response retry (SDK, normal end without any content, only openai format), same provider safety window retry (stream replay before no output is delivered to the caller), polling circuit breaker (cooling and skipping if continuous failure reaches the threshold), and intention rerun (worker with model_error After the end, the entire track is intended to be run again) with five layers. When the inner layer is exhausted, it is the turn of the outer layer. Each layer has two knobs with unified semantics: leave it blank = use the original default number of times and exponential backoff; fill in the number and use that number; fill in the interval and change the exponential backoff to a fixed interval; fill in -1 = turn off this layer and try again. The first three layers follow the endpoints and can override the global default field by field in each model configuration (those that only nail intervals still inherit the global times); circuit breakers and intent reruns are process-level semantics and only have a global copy. The heat will take effect after saving and no restart is required. Leave everything blank to represent the current behavior, and the old library will remain unchanged byte by byte after the upgrade (new columns default to 0, and the settings key does not exist, which means all defaults). The connection test deliberately does not take these parameters - it has a 30s hard timeout, and superimposing user-configured retries will only test the available endpoints as timeout failures. Design see `docs/LLM重试设计.md`.
- Each LLM configuration supports custom **session header** (`session_header_key`): when it is not empty, each LLM request will carry this HTTP header, and the header value is the currently running session id (chat session is `conv-<id>`, worker is `exp<x>-worker-i<intent>`, etc.), which is used for some gateways that perform prompt caching/sticky routing based on the session-id header. In implementation, the session id is read from the request context and injected without changing the norma. The same shared provider can also emit different header values ​​per session. The old library tape is migrated, and an input box is added to the front-end LLM configuration pop-up window.

#### Fixed issues

- Fixed the problem of directly reporting `23502` when saving the LLM configuration because the session header field is empty: the column is `NOT NULL DEFAULT ''`. Previously, `NULLIF($n,'')` was mistakenly set and the unfilled session header was written as NULL to trigger a non-null constraint. Now parameters are passed directly according to empty string semantics.

### Agent

#### New features

- The wall clock timeout is changed to **End in place**: relies on norma v0.3.6. When `MaxDuration` reaches the point, it will interrupt the running tool and continue running according to the number of ending rounds on the live ctx (write back the identified content + summary, the final state is timeout), and no longer rely on the worker/planner's respective `maxDur+90s` external hard ctx to directly kill the stuck run. `aborted_tools`. Chat using the same harness automatically obtains the same behavior, and the mainagent has no `MaxDuration` and is not affected.
- Shutdown: When the planner wakes up due to heartbeat/no change and there is no open or running intention in the whole graph, the opening statement is changed to a shutdown alarm, clearly informing that no workers are running and there is no queue direction. This round must produce one or more new intentions that are not repeated (0 intentions are not allowed).

#### Modified functions

- Worker prompt word reconstruction: the original JSON of intent/startup command/anchored assets is moved into the system prompt, which is re-spelled in each round and will never be suppressed by compaction. Continued running does not rely on the retention of the transcript first message; the startup user message is slimmed down to only the global situation overview (can be downgraded, tolerates stale). The price is that the system mixes per-intent data and loses cross-intent cache reuse, which is a deliberate trade-off of "not losing intent".
- Streamlined the default planner/worker text and corrected several practical issues: planner differentiated `recent_done` by status (blocked/exhausted, check trace first and then decide, not a dead end or mindless rerun), negative conclusions were changed to "observation/doubtful, inconclusive" and evidence should be seen before acceptance, the goal has not been achieved and there is no open/running intention that must be produced (hard bottom line), depth takes precedence over coverage; worker negative conclusions only write "observation +" "Experimental reading method", the decision-making power belongs to the planner, cross-intention clues are written into the summary of the fact, and handed over to the planner, do not pursue it yourself. reseed will append the new default as a new version and switch over, and the user-defined/old version will be retained in the history and can be rolled back.
- Convergence of the work agent default toolset and asset writeback tips: The worker is only responsible for the execution and writeback of a single intention. Reading context/cross-work review is a planning responsibility, so `list_facts` / `node_detail` / `list_companies` and cross-work retrieval (`search_all_worker_traces` / `list_worker_traces` / `get_worker_trace`), leaving only `list_findings` (check duplication before reporting a vulnerability) + `add_finding` / `record_fact` + `insert_assets` / `list_assets`; delete the prompt word and `insert_assets` schema Conflicting stale field description (`type=tech`/`on_url`/`props`). The old library will be migrated in one go and the corresponding bindings will be stripped off. The bindings with the same name of planner/main will not be affected.

### Explore the map

#### New features

- **Exploration graph cold node compression (cold-digest)**: Fold old and long-term inactive intentions/facts into digest nodes and display them in `graph_overview`. The original nodes are permanently retained and can be completely restored by ID (lossless storage, compression-only presentation, reversible folding). The hot and cold determination is based on reverse reachability + any live branch is hot, equipped with R=6 rounds of anti-shake and connected component grouping; background compression (minor folds uncovered cold blocks, major returns to the source and re-presses and merges fragments) with activity review and cooling mutual exclusion, never on the hot path and never cover the Easter point. The overview side provides `cold_digests` and `cold_index` indexed by assets, `expand_digest` / `expand_index` are responsible for restoration. The overview of associated/inherited tasks also reuses its own collapsed view, and `expand_digest` supports cross-task read-only restore. `expand_digest` / `expand_index` are only given to planners and main agents, not workers. All are migrated with the old database.
- `graph_overview` Full output `finding_list`: Findings are the highest value product of the task and there are usually not many single tasks. Instead, they are output in full in the overview (unlike facts, which are only given to the latest window). The planner/worker can view all confirmed vulnerabilities at a glance in each round, without the need to adjust `list_findings`. Each item is shortened to `{id, summary, evidence?, from_intent?, assets?}`, where the affected assets are directly given to readable content (url/domain name/ip:port) instead of naked ids.

#### Modified functions

- `graph_overview` is no longer tiled `hosts`: the coverage block removes the host list (each round in a large-scale task carries a maximum of 500 host strings, which has limited value for planning decisions). Only `host_count` is retained. Specific hosts can be checked on demand `list_assets`. A new top-level `done_intents_total` (total number of ended intentions) is added, parallel to `recent_done_intents` that is truncated to ≤15, so that the planner can know whether it has been truncated when deduplicating.

### Tools

#### Modified functions

- The default binding of `add_company_scope` is changed from worker to planner: defining the scope of enterprise assets is the responsibility of Planning/Master/Auto, and worker only performs exploration. The new library seed is bound to mainagent/planner/auto by default, and the old library is migrated in one go.
- Planner binds `list_assets` by default: Now it has both `list_assets` (DSL full database search) and `list_untested_assets` (not tested within the scope). The old database is backfilled once and does not cover user unbinding.

### Task

#### New features

- Added a "Running Worker" column to the task list: counts the number of intent nodes of `state='running'` under this task. The caliber is exactly the same as the "Running Worker" in the overview of the task details page. 0 is displayed when there is no running Worker.

### Network

#### New features

- Added **global exit proxy** configuration: all target traffic can go out of the network through a unified global proxy (http/https/socks5, supports `user:pass`). When traffic capture is turned on, it serves as the upstream of the MITM recording agent (the traffic is recorded and then goes out of the network through the proxy, and both interception and transparent transmission paths go upstream without leaking the source IP); when the capture is turned off, the agent's bash environment and WebFetch are directly injected (`proxyEnv` adds `ALL_PROXY` to support socks5). The configuration is stored in the settings KV table (no migration required). A new "Global Agent" card is added to the front-end system configuration page, which is independent of the network search agent and LLM agent.

### UI

#### Fixed issues

- Corrected the semantic errors of intent status labels: `exhausted` "Exhausted" → "Budget exhausted" (actually the number of steps/time budget was cut off midway and only partial results were written back, not that the direction has been exhausted), `blocked` "Intercepted" → "Execution error" (actually the model/API/network failure retry was exhausted, the intention was basically not detected, not the target/WAF interception), and the previously missing ones were added. `stopped` "Stopped" (work manually stopped by the user).

### Dependencies

#### Modified functions

- Upgrade norma to v0.3.4: MCP tool output adds truncation and disk placement (see `651b961`), and subsequent v0.3.6 will support wall clock in-place completion.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.7] - 2026-08-31

### LLM

#### New features

- A new "output upper limit" is added to the model configuration, and you can choose which request field name to use for the upper limit: the upper limit limits the maximum number of tokens generated in a single reply, which is sent with each request. 0 (default) = does not send this field, determined by the server's default value; it is two different things from the "context window" - the latter is the total capacity of the model, which is only used locally to calculate the compression threshold and does not appear in the request. The field name switch is only meaningful for the `openai` (Chat Completions) format: empty (default) sends `max_tokens`, and most compatible gateways only recognize it; the OpenAI official inference model (o series / GPT-5) in turn only recognizes `max_completion_tokens`, and `max_tokens` will be reported directly to `unsupported_parameter`. Such endpoints need to manually switch to new fields. The two formats have their own field names (`max_tokens` for Anthropic and `max_output_tokens` for Responses API), so the switch is grayed out and cleared when saving in non-openai formats. At the same time, a link that has never been connected before is added: planner / worker / chat / main agent / target disassembly. There is no output limit set in the five parts of the openai system. The openai system does not send at all. Anthropic follows the 8192 of the SDK. Just adding configuration items without connecting this line will not take effect even if the number is filled in. Now the value is the same as "streaming output" and is analyzed in each round according to the configuration. It will take effect in the next round after failover switching. The behavior of the old library is completely unchanged after upgrading (new columns default to 0 and empty field names).
- When clicking "Test" in the background, the status code of each HTTP attempt and the original response body of the gateway are entered into the server log (response size is 4K), which facilitates diagnosis of 401, quota copy, empty frame, return to HTML page, etc. You can no longer only see the ok / err after the UI is folded. This log does not rely on the "LLM logging" switch.

#### Modified functions

- The task LLM configuration chain can be modified in any state, and there is no longer a restriction on "running/paused/configuration chain exhausted": after the task ends (done/failed/timeout), the main Agent dialogue still follows this chain. When there is a problem with the model on the chain, it cannot be changed and the interaction cannot be continued. The back-end HTTP and DB transaction final state interceptions are removed together, and the front-end pop-up window is also open for editing and saving of final state tasks. When the final state task is saved, quota-blocking intentions will no longer be reopened (those intentions will be moved to open, and will neither be executed by workers nor meet the conditions for "re-running intentions"). If you want to continue running, you can still use re-running intentions/new goals, and they will bring the task back to the running state.

### Agent

#### Modified functions

- "Send a message to the running Worker to adjust the direction" was changed to reuse the existing pause/resume + transcript resume mechanism, and the behavior is consistent with the main Agent dialogue: previously, a set of self-built intervention persistence protocols were used, which affected the scheduling barrier, recovery process, and more than ten activity query filters. Now the message is injected through the next round of input, and is intended to run directly in the dedicated goroutine, without being restricted by the 3-slot worker pool, and run as and when it is sent; the front end retains the message box and restores the "Continue Directly" button, and the sending is carried by SSE instead. No DB schema changes. Trade-offs: Messages are only stored in memory, crash recovery is not performed, and when sending messages, the task may instantaneously exceed the number of concurrent workers (acceptable in low-frequency scenarios).

### Task

#### Fixed issues

- Fixed the problem of out-of-memory (OOM) crash when archiving large tasks: the archive is changed to streaming writing snapshot, and the entire task is no longer read into the memory at one time; at the same time, three recovery gaps on the cold archive path are filled - the traffic of modern installations only exists in SQLite. Previously, the crash between PostgreSQL submission and SQLite submission would cause the traffic of archived tasks to stay in the hot storage and cannot be recovered. Now the temporary log will be written regardless of whether there is a historical directory. Missing `journal.json` Also treat as disposable.
- Fixed the problem of system lag when there are many tasks: there is obvious delay between loading dialogue and loading interface. The query of task list, task context and exploration records are optimized together, and the front-end dashboard, conversation page and task details page are synchronized to reduce repeated requests.

### Assets

#### Modified functions

- Remove the "maximum 256 rules" limit for enterprise asset ranges: Enterprises that enter ranges one by one by IP/domain name can easily hit this upper limit. After hitting this limit, they can only be split into multiple companies, but the range itself will not be slower because of the large number of rules. The three upper limits for front-end and demo mock have been removed. A single rule is still limited to 1024 characters. The upper limit of 2 MiB for the request body is retained for cover (approximately 40,000 to 50,000 rules).

### Skills

#### Fixed issues

- Fixed the problem of "Upload failed: zip: unsupported compression" reported when uploading skill compressed packages: Go standard library only has built-in Store / Deflate decompressors, and bzip2 and Zstandard packages written by compression software in non-default files cannot be decrypted. Now add these two decompressors (pure Go implementation, no new external dependencies); Deflate64 / LZMA / XZ / PPMd and other methods that cannot be decompressed, as well as encrypted compressed packages. Instead, a Chinese prompt is reported before decompression, directly naming which file uses which compression method and how to repackage it, and no longer throws the underlying English error to the user.
- Fixed an issue where the file name check in the skill judged the Chinese file name as illegal: the path check was originally the ASCII whitelist of `[A-Za-z0-9-_./]`. As long as there is a Chinese-named file in the compressed package (`参考/说明.md` and the like), the entire package upload will fail with "compression contains illegal path". Changed to Unicode blacklist: allows file names and spaces in various languages, still rejects control characters, illegal UTF-8, zero-width and bidirectional control characters (RLO file name masquerading), `\ % # ? * : " < > |` and `..` / absolute paths / empty path segments, and the behavior of preventing directory traversal remains unchanged. Skill names are also liberalized - ASCII is still limited to lowercase letters, numbers and hyphens (agentskills.io specification). Non-ASCII letters such as Chinese can be used directly as skill names, but spaces, dots and path separators are not accepted.
- Fixed the issue where the file name of the Chinese package produced by Windows compression software is garbled or the whole package is rejected after decompression: this type of zip does not set the UTF-8 flag and uses GBK to write the file name. Now it uses GBK to decode and then perform path verification. At the same time, frontmatter with quotes such as `name: "中文技能"` can also get the skill name correctly.

### UI

#### New features

- The "All Discovered" tiled view is added to the discovery page and set as the default. It can be switched with the original "Group by Task" view through the header tab: the grouped view has to be expanded task by task to see the vulnerabilities, but it is a detour when you just want to scan the list across tasks. The tile view is a large cross-task table (10/20/50/100 items per page), and the behavior is exactly the same as the grouped view - check export, expand within the row to see evidence and detailed reports, change the name/category/severity, change the disposition status, drill down, and delete within the row. The two views share the statistics card and filter bar, and the filter will not be lost when switching. The current view and filter items are recorded locally; polling only displays the current view, and in-line changes will be written to both caches simultaneously, so you will not see expired data when switching to the past.
- A new "by asset" view is added to the discovery page: the asset tree on the left (enterprise → root domain name/IP → subdomain name → service → interface, the enterprise layer only appears when the asset is indeed owned), and the right side is the discovery under the entire subtree of the selected node, sharing the same table and the same batch of filtering conditions with the other two views. The tree only accepts discovered assets, and the ancestor chain is completed on demand (when the discovery is only hung on the deepest interface, the upper layers can still be spelled out). The count on the node is aggregated by the subtree and deduplicated according to the discovery; the discovered assets that have been deleted or have no associated assets are put into the "unassociated assets" bucket. Unlike the other two views, the asset view does not poll - it is queried only when entering the view, changing filters, discovering additions, deletions and changes on the page, or clicking the refresh button on the tree. The left tree is a navigation structure, and there is no need to recalculate every 5 seconds. Each layer in the tree only displays the increment relative to the previous layer (the subdomain name removes the root domain name suffix, the service displays `https :443`, and the interface only displays the path), and the complete value is in the hover prompt and breadcrumbs; when there are too many nodes, the entire layer discards the interface/service level and gives a prompt, and the count is still included in the upper layer.

#### Fixed issues

- Fixed the issue where the session records in the session details on the mobile phone are squeezed and cannot be seen completely: the session list is collapsed in narrow screens and the width is given to the records themselves.

### Dependencies

#### Modified functions

- Upgrade norma v0.3.2 → v0.3.3: Add `Config.MaxTokensField`, so that the output upper limit of OpenAI Chat Completions can be changed to `max_completion_tokens` (the inference model only recognizes this key). The two keys are mutually exclusive, only one of them is sent, and the default is still `max_tokens`.
- Upgrade `golang.org/x/mod` v0.37.0 → v0.40.0, fix two dependency security warnings.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)
- [@begininvoke](https://github.com/begininvoke)

## [0.3.6] - 2026-08-27

### LLM

#### Fixed issues

- Fixed the issue where the "non-streaming" switch changes back to "streaming" after being saved and reopened (#69): the DTO of the configuration list interface misses the `streaming` field, and the response never returns it. After the front end reads `undefined`, it always falls back to the default streaming; there is no problem with the actual writing and DB storage, but it cannot be read back. DTO adds this field (`omitempty` is not added, `false` must also appear in the response).
- The test connection is changed to verify that the model has a reply, and the test is based on the real sending and receiving mode of the configuration (#65): Previously, it only judged whether the HTTP was successful, and the request was passed but the model had zero reply (thinking about burning the budget/the text was swallowed by the security policy/the compatibility layer was lost `content`) and also reported "connection successful", which is separated from the performance of "no reply" in the session; and when testing constant flow, the non-streaming configuration actually tests another channel. Now an empty reply directly determines failure, and when successful, the model reply is displayed in the prompt, and the current streaming switch is also brought into the test, so that "test passed" and "session ran successfully" are consistent.

### Agent

#### Modified functions

- `list_facts` is changed to paging + keyword filtering to avoid overwhelming the context with a single call when there are many facts (#74): the latest 20 items are returned by default, supporting `limit` (upper limit 100)/`before` cursor/`q` summary keyword, returning `{facts, total, has_more, next_before}`; single summary If it is too long, it will be truncated according to the number of characters (the full text will still be `node_detail`). The worker / planner prompt words are synchronized to paging semantics. The old library flushes the new parameter schema into the tool catalog table through a one-time migration (`SeedTool` is first inserted only, otherwise the tool management page displays "no parameters").

### Tools

#### New features

- Added statistics on the number of tool calls on the "Tool Execution" page (#72): The "Statistics" button on the toolbar opens a pop-up window to display the number of calls, proportions, and number of failures by tool (in descending order); the task/keyword filtering of the list is used to count the entire result set instead of the current page, and the pop-up window is only pulled when the pop-up window is opened.

### Dependencies

#### Modified functions

- Upgrade norma v0.3.1 → v0.3.2: Fix the silently discarded reasoning/refusal (Responses `reasoning_text`) in the OpenAI system response, deduplication of three reasoning fields, bounded retry of empty responses (zero content blocks), and gateway 400 caused by compression boundary cutting tool pairing (also fix the existing orphans baked into the transcript).

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.5] - 2026-08-25

### LLM

#### New features

- Each LLM configuration supports streaming/non-streaming switching (default streaming): enable streaming SSE; turn off true/non-streaming (`stream:false`, return complete JSON at once), which can bypass the poor SSE implementation of some gateways (empty frames, lost frames in the thinking field), at the cost of losing real-time progress and real-time Token counting during operation. worker / planner / mainagent / chat / goals all dynamically take values ​​according to the currently activated configuration; `llmpool` / `llmrec` / three-layer Provider packaging are compatible with non-streaming when the task is running; `llm_profiles` adds `streaming` column and `ALTER` complements the old library (default `true`, old configuration is not affected).
- Support LLM configuration of OpenAI Responses API format: each configuration adds a third format `openai-responses` (named `POST /v1/responses`), parallel to Chat Completions / Anthropic; `BaseURL` normalization, default model `gpt-5`; `format` constraint of `llm_profiles` is added `openai-responses` and idempotent migration to supplement the old library; add "OpenAI (Responses API)" to the front-end format drop-down. Depends on upgrading norma v0.3.1 (including `reasoning_content` postback fix).
- Record LLM request/response HTTP Original text: Capture the real wire body at the HTTP transport layer, retain the tool schema, `tool_use` blocks and original SSE frames that cannot be seen in the normalized view; multiple attempts of norma internal retries are retained one by one; `llm_records` New `raw_request` / `raw_response` two columns are merged `ALTER` Supplement the old library; add "original" view switching and request/response copy buttons to the recording page details panel (compatible with the `execCommand` fallback of non-security context).

### Dialogue

#### Modified functions

- Multi-select in the session list is changed to "Multi-select" mode switch: the default list no longer has check boxes in each row (the look and feel is cleaner), and the header is changed to "N total + multi-select" button; click "Multi-select" to enter the selection mode (check box, select all, batch delete), "Finish" to exit and clear the selection, and automatically exit after all batch deletions are successful (if there is a failure, stay in the selection mode for easy retry); single-line rename/top/delete still go through the per-row ⋯ menu.

### UI

#### Modified functions

- Enhanced task management, session operation and traffic viewing experience (#57): Task list column sorting preferences can be persisted in memory, in-line renaming can be triggered directly by icons (no longer through the menu), drawer (sheet) interaction and traffic viewing details are optimized.

#### Fixed issues

- Worker asset labels only display domain names and IPs, and correctly handle empty label scenarios.

### Agent

#### New features

- A new "quantitative acceptance check" is added when the planner judges the target: when the target contains quantifiable acceptance conditions (asset test coverage reaches `prove_goal`, if the goal is to make up for the gap instead, it is not allowed to bid met in advance on the grounds that "the main part has been completed/generally achieved". Fixed an issue where the target required coverage of 100% but the actual measurement was only 40% but was deemed completed.

#### Modified functions

- Rewrite the basis for determining "0 intention in this round" in the planner prompt: The original text describes 0 intention as "the most common and important principle", which will cause planners to stop prematurely when the goal is not achieved and there are still untested areas within the scope. Now it is changed to produce 0 intentions only in two specific situations - ① The direction that is thought of has been covered by the open/running/recent_done intention; ② The next step depends on the output of the currently running work, and the output has not yet appeared (it should wait until it is finished and the next wake-up after the graph is updated before planning). And add reverse constraints: When there is a new direction that is not covered and does not depend on running work, or the goal has not been achieved and there are still untested areas, don't stop because "0 intentions are common".
- Strengthen the "evidence threshold for negative conclusions" in worker prompts: For negative conclusions that may cause planners to abandon an entire direction, such as "cannot be injected/port is closed/no login entrance", it is required to exhaust all reasonable means within the intention (change coding/parameters/paths/methods) before drawing a conclusion; if the means are not exhausted or the evidence is weak, mark `confidence=inferred` to avoid using the rash `observed` Negation welds the entire route to death (wrong negation early in the task, especially, can lead to a biased direction and is difficult to heal).
- The above planner/worker default prompt word changes are added to the new version through one-time migration (`reseedPlannerPrompt` / `reseedWorkerPrompt`, each with settings flag guard) and switched to the current version; the old version is retained in the version history, and users who have customized prompt words can restore it in the version record.

### Operation and maintenance

#### New features

- Added `reset-password.sh` to reset the administrator (user name is fixed `ARTEX`) password: supports local / docker deployment, the connection information can be explicitly specified or automatically read from `--dsn`/`$ARTEX_PG_DSN`/`config.json`; use `pgcrypto` in the library to generate a bcrypt hash compatible with back-end login and write it back `settings.auth.password_hash`, there is no need to restart the service after resetting. The password is passed in through the environment variable, does not enter the process argv, and is escaped to prevent injection.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

## [0.3.4] - 2026-08-24

### UI

#### New features

- Categories can be modified in batches after multiple tasks are checked in the task list. The target category supports "Unclassified" to move tasks out of the current category; the entire batch writing is completed in one transaction, and failed items will only be tasks that have been deleted after being checked.

#### Modified functions

- The "Task Category" of the new task has been changed from a drop-down selection to a searchable input box: typing will filter the existing categories. If the name is not in the library, press Enter (or click "Create" in the drop-down list) to instantly create a new one and select it. The selected items will be displayed with a removable tag; it is still limited to a single category.

### Dialogue

#### Fixed issues

- Fixed the issue where the message sent was intercepted by "LLM is not configured and cannot talk" when the LLM configuration is selected in the session but there is no global active configuration. The check before sending originally only looked at the global activation configuration, but the actual execution will give priority to the configuration selected by the session. The parsing logic of the two places is inconsistent; now it is unified into the same set (session/Agent binding takes priority, global coverage is used).
- The prompts when unable to communicate are broken down by status: there is no configuration prompt "Add configuration", there is configuration but not activated prompt "Activate or specify a configuration for this session", "Not configured" is no longer always displayed, making it easier to locate.

### Agent

#### New features

- For tasks whose goals have been fully achieved, the intention issued by the main Agent through `add_intent` can be directly received and executed by the Worker, and it will be executed as soon as it is completed: at this time, the Planner will no longer run (the task will not enter the planning if it has no open target), to avoid re-judging the goal to be achieved and canceling the intention just issued; after the frontier is drained, the task will return to Completed. Before sending the intention, the main Agent will ask the user whether to register it as a formal target based on the intention content. After registration, the task will return to regular autonomous planning.

#### Modified functions

- When the `step_ids` of `get_worker_trace` / `get_task_worker_trace` exceeds the upper limit (5), an error will no longer be reported directly. Instead, the complete content of the first 5 steps will be returned, and `returned_step_ids` / `omitted_step_ids` / `notice` will be passed in the result. Tell them what has been taken this time and what has not yet been taken, and prompt "If there is enough, there is no need to take more." Incoming duplicate and illegal IDs will be deduplicated and eliminated before counting.

#### Fixed issues

- The task list is changed to descending order by task id (newest created first), replacing the original ordering by creation time. Multiple tasks created at the same time have the same timestamp and unstable sorting. Combined with polling every 10 seconds and random traversal of the memory map, they will frequently exchange positions in the list.

### Assets

#### Fixed issues

- Fixed an issue where enterprise attribution recalculation was blocked by a single row of dirty data: `assets.ip` When the host name (Agent or asset API writing) is saved, `a.ip::inet` will be thrown `22P02`, causing all new/modified enterprise asset ranges and enterprise deletions to fail and be rolled back. Use the safe cast `try_inet()` instead, and illegal values ​​are skipped instead of breaking the entire statement.
- `ip` that cannot be parsed is no longer silently skipped: after saving in the enterprise scope, it will be clearly prompted how many assets whose `ip` are not legal IPs (including specific ids and values), reminding these assets that they will not be attributed by IP/CIDR rules; the same information is also written to the server log, covering paths without front-end response (deleting enterprises, scopesentry synchronization, Agent writing).
- The IP/CIDR matching in the task test range is synchronized to use `try_inet()`, replacing the original regular guard that only checks the character set (host names with all hex letters such as `abc.def` were still missed and triggered the same error).

#### Modified functions

- The `ip` field of `ip`, `service`, and `endpoint` assets no longer accepts host names: `insert_assets` and the asset API will return errors with `index` one by one and directly provide correction methods (use `type=subdomain` instead `domain`, or parse the A/AAAA record first), the Agent can correct it accordingly and re-insert. Other assets in the same batch will not be affected and will be put into storage as usual.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.3] - 2026-08-23

### Worker

#### New features

- A running Worker supports single-item suspension, resumption and cancellation; suspension retains the intention, facts and vulnerabilities, and cancellation will transactionally clean up the current intention and its direct products after the execution exits.
- Added `paused` intent state, execution fence and named termination reason to prevent late blackboard writes after suspension, cancellation or task deletion.
- Increase the upper limit of optional task concurrency; new creation, recovery, vulnerability depth and queue replenishment are unified through the persistent FIFO admission path.

#### Modified functions

- The default duration of a single run of the Worker wall clock is adjusted from 600 seconds to 1200 seconds; after the timeout task is resurrected, the timer will be re-timed the next time it is actually executed.
- The real execution status of Worker, Planner and main Agent is unified to drive the task status logo, and the problem of "Idle" still being displayed when Worker is running is fixed.
- The Worker session retains individual controls and the current session Token summary, and the model name is moved to the right side of the current session title.

#### Removed functions

- Removed Worker multi-select, select all, and batch pause/resume interfaces, and also removed Worker batch control API and Mock contract.
- Remove the Token logo and Tooltip in the Worker list row, and the underlying complete Token ledger and task aggregation interface will remain.

### LLM

#### New features

- The task supports an ordered LLM configuration chain; it automatically switches to the next configuration when the supplier's quota is insufficient, and persists the current configuration, depletion status and error summary.
- Running and suspended tasks support editing the complete configuration chain, adjusting the order and manually switching the current configuration, which will take effect from the next LLM call.
- Automatic switching, manual switching and full-chain exhaustion are written into structured system activities, and deduplication reminders pop up through the task activity flow.
- Added a task role model parsing interface to uniformly parse the configuration and model used for the next call of Main Agent, Planner and Worker.
- Added optional global LLM Pool to support configuring calling order, specified model failure margin, health status, cooling recovery and manual reset.
- Add an always-on LLM usage ledger that aggregates input, output, and cache tokens by task, session, model, and configuration.

#### Modified functions

- Target disassembly, Planner, Worker and main Agent share task-level LLM runtime (see below for parsing priority).
- Failover only responds to clear credit, balance or billing errors; ordinary current limiting, authentication, network, server and context errors will not cause incorrect switching.
- LLM configuration page changed to configure card list and right drawer editing, and provide Pool polling order, priority, exclusions, health status and recovery operations.
- Configuration chain exhaustion information supports narrow screen line wrapping; the current model is changed from the session list icon to a text label and complete Tooltip next to the current session title.
- The order of parsing LLM for each role within a task is adjusted to "Agent Binding → Task Configuration Chain → Global": roles that are explicitly bound to a model always run on the model, and only unbound roles fall to the task chain, and then fall to the global when the task chain is empty.
- Leave the outbound proxy blank to connect directly, and no longer fall back to `HTTP_PROXY`/`HTTPS_PROXY` environment variables (explicit `ARTEX_LLM_PROXY` is not affected); proxy input supports `socks5://user:pass@host:port` with account password.

#### Fixed issues

- Transient streaming failures before submission (no output has been produced) are safely backed off and retried on the same provider, which significantly reduces the situation of "interruption after running only one or two tools, no summary, and final state `model_error`"; quota exhaustion, context too long and 4xx deterministic errors are not retried and are still handled by failover and compression recovery respectively.

#### Removed functions

- Removed the LLM icon-style identifier in the session list to avoid confusion with the Worker status icon.

### UI

#### New features

- Tasks support the creation, renaming, deletion and filtering of global categories; you can directly select categories for new tasks.
- Added global task template CRUD and right management drawer; new tasks can load preset descriptions and goals, and the current content can also be saved as a template.
- New tasks support association of multiple source tasks and multiple enterprise asset scopes; the enterprise scope automatically identifies domain name, URL, IP, CIDR, ICP and enterprise keywords through a single multi-line text box.
- Task test assets support real-time addition and deletion, recording sources such as manual work, enterprise, inheritance or Agent discovery; the current test assets and source summary are displayed next to the Worker session title.
- The discovery page is grouped by tasks and provides independent paging within the group. It supports filling in descriptions for vulnerabilities and creating high-priority in-depth intentions.
- Conversations support renaming, pinning, unpinning and deletion; the task list supports batch pause and continuation of the current page.
- Traffic details and tool execution details are changed to the right drawer; HTTP messages are completed with `Host` and the request line, status code, Header, JSON and markup language body are highlighted.
- "Details" and "Pause/Continue" buttons are added to the task list operation column; the session sending key can be configured in the system settings (`Enter` / `Cmd+Enter`, etc.), and a new agent input box is added to the Web search area.
- The logo on the right side of the session title is changed to display the LLM configuration name, the model ID is moved to the hover Tooltip; an optional name can be set for the task, and a name column and search are added to the task list (if empty, it will fall back to the description).
- The skill page adds usage count, recent usage time and missing dependency statistics to facilitate locating skills that are ineffective or not installed.

#### Modified functions

- The task title is changed to a focusable detail link; task assets, enterprise assets, discovery task groups and vulnerabilities within the group use server-side real paging, stable sorting and accurate totals.
- Newly added companies use the right drawer instead, and range entry is unified into multi-line text and provides real-time recognition, verification and type preview.
- The main Agent input box supports automatic growth, `Enter` sending, `Shift+Enter` line wrapping, and avoids incorrect sending of Chinese input method combination states.
- Agent preview, task report and related details are uniformly rendered using shared Markdown; deletion confirmation, long error and mobile drawer width are uniformly fixed.
- The task template selection box is displayed and searched by the template name, and submitted by the template ID; the original font size of the system is restored, and the application version is uniformly displayed as `0.3.3`.
- Task total and current session token only highlight input, cache read and output values; the send button uses a simpler up arrow icon.
- The test scope of the task overview shows the company name instead of the ID.

#### Fixed issues

- Ordinary text containing dots will no longer be misjudged as ICP registration numbers.
- Fixed the problem of name collision with global runtime variables (such as `{{.Now}}`) in the variable directory, causing the Agent editor variable list to render duplicate keys.

#### Removed functions

- Delete the independent "Enter" button for task cards and click the task title to enter details.
- Removed the "New Task" button on the dashboard, the new company's Logo URL input, and the vulnerability number Badge at the top of the discovery page and in the task group.
- Undo the global 10% font increase style, and remove the Worker selection box, model icon and row-level Token statistics in the session list.

### Agent

#### New features

- New tasks can be directly associated with multiple existing tasks, and real-time read-only inheritance of the goals, facts, vulnerabilities, completed intentions, asset scope and blackboard context of the direct source tasks.
- The blackboard reading tool supports on-demand query of source task nodes, facts, vulnerabilities and execution traces; inherited nodes are marked with source tags and all writing tools refuse to modify them.
- The enterprise-wide tool supports domain name, URL, IP, CIDR, ICP and enterprise keywords; keywords are only used as Agent-wide prompts and do not participate in automatic asset attribution.
- The vulnerability drill-down operation will create a high-priority manual Worker intent with an asset anchor and `derived_from` edge in the original task.
- A new "Goal Management" card has been added to the overview, which supports manual viewing, addition, modification and deletion of goals; new additions or modifications will notify the Planner and resurrect the task, and delete hard-deleted target nodes (cascade deletion of edges and anchor points) but will not resurrect them.
- The main Agent obtains the `steer_work` tool by default, which can inject corrective instructions to the running Worker in real time without interrupting or losing progress (still verifying that the intention belongs to this task).

#### Modified functions

- The source task intent does not enter the new task frontier, and the new task continues to use independent exploration, execution queue, working directory and conversation history.
- Deleting a running intent no longer destroys data: instead, it stops at `stopped`, requires filling in the reason for deletion, attaches the reason to the intent as a fact and writes it into the payload. The planner presses `cancelled` to trigger perception (the intent content and reason are retained).
- The main Agent conversation is changed to server-side activity flow driver, and input can be restored if the transmission fails; the bottom is automatically posted when the conversation is opened and the last reply is kept visible after the details are lazy loaded.
- Task suspension will terminate the current Main Agent, Planner and Worker calls, but does not prevent users from actively initiating new Main Agent orchestration sessions during the suspension period.
- Cancellation, shutdown, and streaming interruptions uniformly retain the true termination reason, generated content, running rounds, time consumption, tokens, and unreturned tool calls.

#### Removed functions

- Removed optimistic message echoing on the main Agent client to avoid duplicate messages and cross-session streaming under pauses, failures, or concurrent activities.

### Constraints

#### New features

- In the target disassembly phase, the operational constraints (allow/deny) are first extracted and then the target is dismantled, which can be added when the main Agent is running; the constraints are injected into the Planner and Worker with the highest priority and the system prompts. After debiasing, diversity and extension exploration explicitly obey the constraints.
- The "Constraint Management" card and the addition, deletion and modification interface are added to the overview; the constraint injection range can be switched separately according to Planner / Worker (both are enabled by default, read in each round, and take effect immediately). The new table `task_constraints` uses `CREATE TABLE IF NOT EXISTS`, and the old database is automatically rebuilt.

### Interception

#### New features

- Command interception adds model approval in addition to regular/string rules: when none of the rules are hit, the model will make a semantic judgment of `ALLOW`/`ASK`/`DENY`. Failure fallback actions and approval timeout actions are configurable; the interception page is split into two tabs "Interception Rules/Model Configuration", and the model judgment results are marked with the `[模型]` prefix and reason.

#### Fixed issues

- Enhanced referee output analysis to prevent the correct `DENY` decision from being mistaken for a release.

### Traffic

#### Modified functions

- Traffic recording is changed to the entire exchange into SQLite (large text overflows into a blob bucket with hash deduplication). A trigram full-text index is built for the text text, which supports arbitrary substrings and Chinese searches; deletion is reduced to a single SQL transaction, from hours to milliseconds, and recording is no longer stopped during the period. Added large body streaming download and `traffic_search` full text parameters; old file tree data does not need to be migrated and can still be read, searched, and deleted.

### Tasks and Assets

#### New features

- Added "Asset Coverage Function" switch to create tasks (on by default): when turned off, coverage will not be calculated and displayed, only assets will be displayed on the situation map, `task_scope` will not be automatically accumulated, and related tools will be removed from the Planner/Main Agent; enterprise associations will not be affected by this switch.
- `insert_assets` Add `related` tag to each asset (default `true`): it only takes effect when coverage is turned on. `false` is only included in the shared asset library and is not included in the coverage of this task (such as side stations or irrelevant assets discovered incidentally).
- Enterprise-wide and task test assets uniformly support domain name, URL, IP, CIDR, ICP and keyword text recognition; domain name/IP can create or reuse global assets, and CIDR/ICP/keywords are retained as task-wide context.

### Build

- `build.sh --release` supports a single build for Linux amd64/arm64, macOS amd64/arm64 and Windows amd64 and generates a zip release package with `skills/`, configuration examples and README.
- Use Go linker to remove debugging information and generate a zip release package; UPX is changed to be explicitly optional to prevent its self-extracting ELF from segfaulting when starting in some Linux environments.
- Release Workflow launches smoke testing on Linux amd64 binary execution and provides `SHA256SUMS` with the release package.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.2] - 2026-08-20

### Added

- New tasks support the association of multiple existing tasks, and the facts, vulnerabilities, completed intentions, asset scope and blackboard context of the direct source task are inherited in real-time and read-only; new tasks still use independent execution queues, working directories and session history.
- Tasks support ordered LLM configuration chains. When a supplier's quota is insufficient, it is automatically switched to the next configuration, and the current configuration, depletion status and structured audit activities are persisted.
- Running and suspended tasks support editing the LLM configuration chain, adjusting the order, and manually switching the current configuration; automatic switching, manual switching, and full chain exhaustion will be prompted on the task details page.
- Running Workers support pausing, resuming and canceling. Pause preserves blackboard data, and cancellation transactionally cleans up the facts, bugs, and execution records of the intent and its direct output after the worker stops writing.
- When deleting a task, you can choose to clean associated assets, traffic, vulnerabilities, and task landing files at the same time, and add deletion barriers, concurrency protection, and auditable deletion statistics.
- Increase the optional task concurrency upper limit; new tasks that reach the upper limit are queued according to FIFO and automatically started after the running slot is released.
- Added task asset paging, company asset paging, task classification statistics and paging intention Mock contract.
- Added `build.sh` single binary build script to support static front-end export, resource embedding, cross-platform target and build version injection.

### Changed

- The task-level explicit LLM configuration chain coexists with the existing global LLM Pool; the explicit chain continues to use strict quota failover semantics, and the Agent binding and global configuration rules are used when there is no explicit chain.
- The main Agent input box supports automatically growing multi-line input, `Enter` is sent, `Shift+Enter` is newline, and avoids incorrect sending of Chinese input method combinations.
- Planner, Worker and main Agent share the task-level LLM runtime; the task chain uses the smallest context window in the candidate model as the safe compression threshold.
- The task details page displays the running, idle and paused status according to the real LLM call status; the facts, vulnerabilities, intentions, asset references and graph nodes of the source task are uniformly marked as sources and remain read-only.
- Agent preview, task report and related detail views uniformly use shared Markdown rendering components.
- LLM model configuration uses card and drawer interaction, and the model list supports independent scrolling in the drawer.
- Task suspension no longer intercepts the main Agent conversation: the main Agent orchestration session is independent of the task suspension, and new messages can still be sent during the suspension (the suspension only terminates the currently ongoing round).
- The default wall clock duration of a single Worker run is adjusted from 600 seconds to 1200 seconds.

### Fixed

- Removed the optimistic echo of the main Agent console and replaced it with pure server-side data rendering. This fixed the problem of confusing messages and stringing together other session content in scenarios such as pause or failure to send.
- When opening the main Agent session, the default is to scroll to the bottom and display the last reply in full: the complete content of the last reply is lazy-loaded and expanded and automatically stuck to the bottom, no longer pushed out of the screen.
- Fixed the problem that the main Agent session continues to run after the task is paused, and the main Agent is not stopped synchronously when the Orchestration Agent pauses the task.
- Fixed the issue where the task status logo and the action button status are out of sync when the task is completed and the Planner, Worker or main Agent is actually running.
- Fix the problem that late blackboard writing or residual files may occur between Worker cancellation, task deletion and concurrent writing.
- Fixed issues with Markdown preview failure, deletion confirmation long text overflow, mobile width and misalignment of some task details buttons.
- Add named termination reasons for all Agent cancellation paths. Activity details can display the canceler, final state, number of running rounds, time taken, Token usage and unreturned tool calls.
- Fixed a race condition where the parent context may preemptively overwrite the named `shutdown` when the backend is closed, and an issue where Chinese activity summaries are truncated by bytes resulting in garbled characters.
- Fixed the issue where the cancellation event with partial streaming output loses the real termination reason, while retaining the content that was generated before cancellation.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

[Unreleased]: https://github.com/Autumn-27/ARTEX/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/Autumn-27/ARTEX/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/Autumn-27/ARTEX/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/Autumn-27/ARTEX/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/Autumn-27/ARTEX/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Autumn-27/ARTEX/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/Autumn-27/ARTEX/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/Autumn-27/ARTEX/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Autumn-27/ARTEX/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/Autumn-27/ARTEX/compare/v0.3.1...v0.3.2
