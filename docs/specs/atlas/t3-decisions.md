# T3 Code's harness decisions, as read

Source: `pingdotgg/t3code` at `55085df2a6c384f34b37901d5dcbc5925ada1600` (commit date 2026-11-24), read-only from upstream commit `55085df2`. Paths below are relative to that checkout. `srv/` stands for `apps/server/src/`, `acp/` for `packages/provider-acp/src/server/`, `core/` for `packages/provider-core/src/server/`.

Each decision says what T3 Code does, where it was read, and what it rules out. A line marked **(inferred)** is my reading of code I did not trace to its end; everything else is stated in the file cited. Nothing here was run: these are facts about the source, not about a running T3 Code.

The two adapters for Claude and Codex are 15,620 and 9,659 lines. I read them by search and by the ranges cited, not whole. A behaviour not listed here was not looked for.

## A. Decisions that hold for every harness

**D1. The server owns the harness process.** Provider processes, terminals, Git and durable state live in the server; clients only control it over typed RPC and never substitute their own files, credentials or machine state.
Read: `docs/internals/01_architecture.md` (Ownership boundaries).
Rules out: a client that starts or talks to a harness itself.

**D2. One adapter per harness; orchestration reads capabilities, never names.** Protocols, account ownership, permissions and capabilities stay behind the adapter boundary.
Read: `docs/internals/03_providers.md` (opening, "Honest capabilities and permissions"); `packages/provider-core/src/server/ProviderAdapter.ts` (903 lines, the boundary; not read whole).
Rules out: `if harness == "claude"` anywhere above an adapter.

**D3. Route by provider instance, not by driver kind.** An instance is one configured provider with its own settings, session state, catalog and credentials; several instances can use one driver. "Strip ambient credentials the instance did not configure."
Read: `docs/internals/03_providers.md` (Identity and integration shape); `docs/internals/00_glossary.md` (Provider instance); `srv/provider/builtInDrivers.ts`; `supportsMultipleInstances: true` in `srv/provider/Drivers/{Antigravity,Claude,Codex}Driver.ts`.
Rules out: two accounts of one harness sharing mutable state; an inherited environment variable deciding which account a session uses.

**D4. Declare only what the harness supports; never emulate.** A capability the harness lacks is turned off so orchestration degrades (for example to a portable context handoff instead of a native fork). "A capability left enabled that fails at runtime is a bug." Permission modes are never faked by auto-answering approvals; a stored mode the harness does not offer falls back to Supervised; native approval and question option ids pass through unchanged and every request can be declined.
Read: `docs/internals/03_providers.md`; capability records `ClaudeProviderCapabilitiesV2` (`srv/orchestration-v2/Adapters/ClaudeAdapterV2.ts:626`), `CodexProviderCapabilitiesV2` (`CodexAdapterV2.ts:349`), `AcpProviderCapabilitiesV2` (`acp/adapter.ts:869`), `AntigravityProviderCapabilitiesV2` (`AntigravityAdapterV2.ts:63`).
Rules out: steering by cancel-and-retype where the harness has no steer; a "full access" mode built by answering yes for the user.
Note: D15 is one place T3 does answer approvals from its own policy for Antigravity, because agy rejects two of its own session modes. T3 states it as the harness's limit, not as an emulated mode.

**D5. Decide, then commit, then act.** Commands are serialized and decided into events without I/O. Events, projections, command receipts and outbox effects commit in one database transaction. An effect worker runs side effects after commit and feeds results back as commands. A command acknowledgement means intent committed. "Effects tied to a lost provider process are retired on recovery rather than replayed."
Read: `docs/internals/01_architecture.md` (Durable intent and side effects); `docs/internals/00_glossary.md`; the files it names (`srv/orchestration-v2/Orchestrator.ts`, `EventSink.ts`, `EffectWorker.ts`, `EffectOutbox.ts`, `CommandReceiptStore.ts`) exist and were not read.
Rules out: I/O in a decision or in the transaction; replaying an effect whose process is gone.

**D6. Thread, session and turn are three things.** The thread is the durable record and survives harness exits. A session is the harness runtime attached to a thread and can be stopped and resumed. A turn ending and its follow-up work settling are separate milestones; a late settle never keeps the client showing the harness as working.
Read: `docs/internals/00_glossary.md`; `docs/internals/01_architecture.md` (Turn completion and checkpoints).
Rules out: losing a conversation when a harness process dies; a "working" light that waits for bookkeeping.

**D7. Every terminal path settles everything open.** Completion, Stop, harness error and process exit each settle open tool calls, subagents and pending requests. Work can outlive a turn (background commands, subagents); it is routed by subagent and reported as pending so idle cleanup does not kill the session.
Read: `docs/internals/03_providers.md` (Cross-provider traps: Terminal settlement, Work outliving a turn).
Rules out: an approval card left open after its turn died.

**D8. Usage limits are a failure class with a reset time.** Quota and rate-limit errors are classified `usage_limit`, with `resetAt` when the harness gives one, never a generic failure. The classes are `usage_limit`, `provider_error`, `transport_error`, `permission_error`, `validation_error`, `unknown`; `resetAt` is kept only on `usage_limit`.
Read: `docs/internals/03_providers.md` (Usage limits); `packages/contracts/src/orchestrationV2.ts:1718-1740`; `core/failure.ts:310-332`.
Rules out: a limited turn shown as a crash.

**D9. Recovery from a usage limit is one armed resume per limited run.** A worker looks at threads whose last run failed `usage_limit` with a reset time. It arms once per (thread, run, reset time), with command id `limit-arm:<thread>:<run>:<resetMs>`; when the reset has passed and auto-resume is on it dispatches one message, "Continue where you left off.", with command id `limit-resume:<identity>:<request>`, started immediately, marked as created by the server. It never arms when: the thread is archived or settled, a runtime request is pending, the reset is not after the run's completion ("An already-expired window reported with a fresh failure cannot start a retry loop"), or the reset is more than 24 hours overdue. Auto-resume is a server setting (`autoResumeLimitedThreads`); without it the worker only arms for a snooze.
Read: `srv/orchestration-v2/UsageLimitRecoveryWorker.ts:1-135`.
Rules out: a retry loop on a limit; a resume typed twice; a resume into a thread that waits on an approval.

**D10. Startup phases and health from real signals; health checks are passive.** A starting turn's phase is reported only when the protocol says so, never from a timer. Background status and model probes never open a session that starts MCP servers, runs hooks or launches a login flow.
Read: `docs/internals/03_providers.md` (Honest capabilities; Process, setup, and security boundaries).
Rules out: "ready after 3 seconds"; a status poll that wakes an agent.

**D11. The server gives each thread its own MCP endpoint credential.** `McpSessionRegistry.issue` makes, per thread and provider instance: a fresh provider-session id (UUID v4), a 32-byte random token (base64url), and a scope naming the thread, the provider session, the instance, whether it is a subagent, and a capability set (`orchestration`, `worktree`, `pull-requests`, `issues`, plus `preview`, `device`, `background-wait` when the thread and harness allow). Only the SHA-256 of the token is kept. The harness gets `{endpoint, authorizationHeader: "Bearer <token>"}` where the endpoint is `http://<listen address or 127.0.0.1>:<port>/mcp` on the server's own HTTP listener. A token unused for 24 hours stops authenticating; the record is kept so that a trusted provider turn (`touch(threadId)`) can revive it, which an MCP request cannot. Tokens are revoked per provider session, per thread, or all. Before each session start the manager reuses the thread's credential only when it still resolves to the same thread, instance and capability set; otherwise it revokes the thread's tokens and issues a new one.
Read: `srv/mcp/McpSessionRegistry.ts:1-230`; `srv/orchestration-v2/ProviderSessionManager.ts:630-700`; `core/mcpSession.ts`; `core/McpProviderSessions.ts` (the per-thread record adapters read).
Rules out: one shared MCP secret for all harnesses; a token that outlives its thread; trusting a harness's claim of which thread it is.

**D12. "Inject the thread's authenticated MCP server ... and keep turns working if that server is unreachable."** The server is named `t3-code` in every harness. The harness's turn must not depend on the MCP endpoint answering.
Read: `docs/internals/03_providers.md` (T3 MCP tools).
Rules out: a turn that fails because the ledger endpoint is down.

**D13. The same `/mcp` route serves two audiences, told apart by the token.** A request is first resolved against the thread-token registry; only if that fails and outside-agent sign-in is configured is it tried as an outside MCP client (PKCE OAuth, a separate audience, read-only or capped by a mode ceiling). Every MCP tool statically declares its access level; thread-only tools refuse outside callers in the handler, because "hiding from tool listings is only discovery".
Read: `srv/mcp/McpHttpServer.ts:340-372`; `docs/internals/04_environment-auth.md` (MCP clients); `docs/user/outside-agents.md`.
Rules out: a tool that is safe only because it is not listed.

**D14. Tool exposure can be deferred.** A client that cannot defer tools itself sends the header `t3-tool-exposure: deferred`; the server then lists a core set plus `tool_search`.
Read: `core/mcpSession.ts` (`MCP_TOOL_EXPOSURE_HEADER`); `srv/mcp/McpToolExposure.ts` (search scoring; core set not enumerated by me).
Rules out: nothing we must copy; noted because our tool list is small.

**D15. Tools T3's MCP server offers (names only).** About 110 tools in these groups: pull requests and CI (`link_pull_request`, `watch_pull_request`, `pull_request_*`, `ci_logs`, `ci_rerun`), issues (`list_issues`, `create_issue`, `link_issue`, ...), orchestration (`orchestrator_capabilities`, `delegate_task`, `task_status`, `task_cancel`, `schedule_task` and its list, update, delete, run-now, `request_secret`, `create_threads`), threads (`t3_thread_start`, `_list`, `_read`, `_send`, `_wait`, `_interrupt`, `_fork`, `_transfer`, `_configure`, `_search`, ...), queue and pending requests (`t3_queue_*`, `t3_pending_request_*`), background wait (`t3_background_wait`), worktrees, projects, environment, theme, attachments, browser preview (`preview_*`), devices (`device_*`), HTML (`html_preview`, `html_render`), `send_to_user`, `t3_feedback`, workflows.
Read: `packages/shared/src/t3McpToolPresentation.ts:118-446` (the key list); `srv/mcp/toolkits/` (directory names).
Rules out: nothing. T3's tool set is its product (PRs, previews, devices); ours is the ledger. What we take is the delegation, thread, queue, pending-request and wait shapes.

**D16. Instructions are short, built per turn or per session, and say what the harness is running in.** `buildRuntimeInstructions` produces a `<runtime_info>` line (which app, which harness, model, effort), the pull-request linking block, an optional feedback line, and for multi-folder threads a `<workspace_folders>` block capped at 3,600 bytes. Tool instructions are added only when the MCP server was injected. A harness with a native system-prompt append carries the session-invariant part once per session instead of per turn.
Read: `core/runtimeInstructions.ts:1-107`; `core/orchestrationInstructions.ts` (exists, 146 lines; not read).
Rules out: replacing a harness's system prompt; instructions about tools the session does not have.

**D17. Accounts: one instance per account, with the harness's own sign-in.** For each of the three harnesses the documented way to use a second account is a second provider instance pointing at a second config directory, signed in on the server's machine with the harness's own login command.
Read: `docs/user/providers-claude.md`, `providers-codex.md`, `providers-antigravity.md` (Accounts and removal).
Rules out: a proxy; rotating accounts inside one harness process.

**D18. Switching account or harness inside a thread is a portable context handoff unless the harness can continue natively.** A handoff is a budgeted selection of saved messages (recent requests and answers, the original request, command outcomes), not an agent-written summary; it carries no reasoning, tool-call state or attachments; the default allowance is 16,000 tokens (clamp 1,024 to 64,000). Which instances can continue a thread natively is decided by a "continuation key": Claude `claude:home:<config dir>`, Codex `codex:home:<shared home>`, Antigravity one key per instance.
Read: `docs/user/portable-handoffs.md`; `srv/provider/Drivers/ClaudeHome.ts` (`makeClaudeContinuationGroupKey`); `srv/provider/Drivers/CodexHomeLayout.ts` (`continuationKey`); `srv/provider/Drivers/AntigravityDriver.ts` (`defaultProviderContinuationIdentity({driverKind, instanceId})`).
Rules out: assuming a conversation started under one account can be resumed natively under another.

**D19. A harness process sits in its own process group and is killed as a group.**
Read: `acp/AcpSessionRuntime.ts:1476-1706` (`ownDetachedProcessGroup`, `signalProcessGroup`, grace 1 second); `srv/orchestration-v2/Adapters/ClaudeAdapterV2.ts:436-460` (spawn through `/bin/sh -c <group watch script>`, `detached: true`, `SIGKILL` to the group on exit).
Rules out: orphaned tool processes after a stop.

## B. Antigravity

**D20. Connection: ACP over stdio to the official CLI.** The server spawns `<agy> acp --gemini-dir <profile> --app-data-dir antigravity-acp` as its own child, with `extendEnv: false` (only the environment T3 builds), and speaks ACP (JSON-RPC over stdin/stdout). One process per session; the documentation calls it "Google's official Antigravity ACP agent". T3 needs Antigravity 1.5 or newer and refuses to start a session on an older runtime, checking the version the agent reports at `initialize`.
Read: `srv/provider/antigravityAuthSupport.ts:546-573` (`buildAntigravityAcpSpawnInput`); `srv/provider/Drivers/AntigravityDriver.ts` (`makeRuntime`, `startCompatible`); `docs/user/providers-antigravity.md`.
Rules out: a proxy, a base-URL override, the print mode loop.

**D21. Client identity sent at `initialize`.** For runtime 1.5.1 and newer: `{name: "t3-code", title: "T3 Code", version: <T3's version>}`. For older runtimes: `{name: "zed", version: "0.0.0"}`, because "older releases offer non-Gemini models only to allowlisted clients such as Zed".
Read: `srv/provider/acp/AntigravityAcpSupport.ts:107-112` (`antigravityClientInfo`), `:74` (`OPEN_MODEL_ACCESS_RELEASE = [1,5,1]`).
Rules out, for us: see spec. Sending another client's name is the one thing in T3's Antigravity path that is a claimed identity; it applies only below 1.5.1.

**D22. Client capabilities declared.** `fs.readTextFile` true when client file reads are on (T3 serves reads only under the session's working folder and its attachments folder), `fs.writeTextFile` false ("advertising writes swaps the agent's diff edits for whole-file client_edit_file"), `terminal` false.
Read: `AntigravityAcpSupport.ts:114-150`; `srv/orchestration-v2/Adapters/AntigravityAdapterV2.ts` (`clientFileSystem`, `antigravityClientFileRoots`).
Rules out: the client running the agent's commands.

**D23. Managed profile per instance.** The profile directory is `<state dir>/providers/antigravity/<sha256 of the instance id>`. It is the agent's `GEMINI_HOME` and `--gemini-dir`; app data is `antigravity-acp` beneath it. The environment the agent gets is the base environment minus `GEMINI_API_KEY`, `GOOGLE_API_KEY`, `GOOGLE_APPLICATION_CREDENTIALS`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, `GOOGLE_CLOUD_QUOTA_PROJECT`, `GOOGLE_GENAI_USE_VERTEXAI`, `GCLOUD_PROJECT`, `CLOUDSDK_CORE_PROJECT`, `AGY_ACP_CCPA_PROJECT`, `AGY_ACP_ENABLE_OAUTH`, `GEMINI_HOME`, `AGY_ACP_FORCE_FILE_STORAGE`, `AGY_CLI_DISABLE_AUTO_UPDATE`, `BROWSER`, `PYTHONUNBUFFERED`, `ELECTRON_RUN_AS_NODE`, `CHROME_CRASHPAD_PIPE_NAME`, plus: `GEMINI_HOME=<profile>`, `AGY_ACP_FORCE_FILE_STORAGE=1`, `AGY_CLI_DISABLE_AUTO_UPDATE=1`, `BROWSER=<a helper that prints the URL instead of opening a browser>`, `PYTHONUNBUFFERED=1`, `ELECTRON_RUN_AS_NODE=1`, `TMPDIR=<a T3-owned temp dir, one per process>` (each launch unpacks about 1 GB), on Linux `SSL_CERT_FILE` when none is set. For a Gemini API key or Agent Platform instance the key is put in as `GEMINI_API_KEY` or `GOOGLE_API_KEY`.
Read: `antigravityAuthSupport.ts:62-82, 186-243` (`resolveAntigravityInstanceDirectories`, `antigravityEnvironment`); `AntigravityDriver.ts` (`prepareSpawn`).
Rules out: the agent picking up an ambient Google credential; two instances sharing a profile.

**D24. Sign-in storage for a personal Google account: a faked SSH session.** "The 1.5 CLI ignores AGY_ACP_FORCE_FILE_STORAGE and keeps Google sign-ins in one OS keyring entry for every profile, unless it believes it runs over SSH." So for the `oauth-personal` method T3 sets `SSH_CONNECTION` to the inherited value or to `127.0.0.1 0 127.0.0.1 0`. The CLI then keeps its token in a file inside the profile, and each profile holds its own account.
Read: `antigravityAuthSupport.ts:230-234`.
Rules out: two profiles overwriting one keyring entry. For us it is also what keeps the macOS keychain dialog away.

**D25. Sign-in flow.** The agy CLI "ignores T3's browser helper during ACP authenticate", so Google sign-in runs the CLI's own `agy acp ... --login` with `DISPLAY` and `WAYLAND_DISPLAY` removed; it prints a `https://accounts.google.com/` link, the person opens it, Google ends on an Antigravity page that shows a code, and the code is written to the CLI's stdin. The redirect URI T3 accepts is `https://antigravity.google/oauth-callback`. ACP `authenticate` is called eagerly at session start with method id `oauth-personal` (or the instance's method).
Read: `AntigravityDriver.ts` (`codeLogin`); `antigravityAuthSupport.ts:576` and `:578-640`; `AntigravityAcpSupport.ts:120-123` (`authMethodId`, `authenticateEagerly: true`); `docs/user/providers-antigravity.md` (Sign in from a remote device).
Rules out: handling the Google password or OAuth exchange ourselves.

**D26. Session start, resume.** New session: ACP `session/new` with the working folder, the MCP servers (D31) and optional `_meta`. Resume: ACP `session/resume` (`resumeMethod: "resume"`), not `session/load`: "Loading history replays tool calls; resume avoids replay." The agent advertises `loadSession`, `sessionCapabilities.list` and `.resume`. Antigravity cannot rewind its conversation, so editing an earlier prompt starts a fresh session with the kept conversation sent as context.
Read: `AntigravityAcpSupport.ts:121` ; `acp/AcpSessionRuntime.ts:2460-2495`; `AntigravityAdapterV2.ts` (comment at `subagentsIdleOnTurnCompletion`); `AntigravityDriver.ts` (`probe`); `docs/user/providers-antigravity.md` (Models and threads).
Rules out: reading or rewriting the agent's own history files to resume.

**D27. Steer and queue.** With agy 1.5.0 and newer, a prompt sent while a turn runs is a steer ("`agy acp` reads a prompt sent mid-turn as a steer; cancel-and-restart killed its subagents"). The 1.4 harness queues it instead. T3 decides from the version in the `initialize` answer.
Read: `AntigravityAcpSupport.ts:76-84` (`PROMPT_STEERING_RELEASE`, `antigravitySteersWithPrompt`); `AntigravityAdapterV2.ts` (`steersWithPrompt`).
Rules out: cancel-and-resend as the steer.

**D28. Interrupt.** ACP `session/cancel`, sent only after the prompt it targets is queued ("an agent drops a cancel that reaches it before the prompt it targets"), then T3 waits for the prompt's own response (`cancelBehavior: "wait-for-prompt"`); if the agent does not finish cancelling within the cancel timeout, its process is stopped and the session retired. `session/cancel` ends the agent's running commands, so once a prompt has ended only an explicit Stop sends it; a new message leaves a background command alone (`preserveRuntimeOnSettledInterrupt`).
Read: `acp/AcpSessionRuntime.ts:2616-2672`; `AntigravityAdapterV2.ts`.
Rules out: killing the process as the normal stop.

**D29. How a turn ends and is classified.** The turn ends with the `session/prompt` response and its stop reason. Antigravity specifics: a reply "ends with a usage update" (`replyEndsWithUsageUpdate`); a prompt left open without updates for 15 minutes is treated as stalled (`promptInactivityTimeout`), paused during long tools and approvals; background commands and subagents can outlive the prompt (post-settle continuation, finalize deferred, a 30-minute hold for a background command whose end the agent may never report). The class of a failed turn comes from the agent's own session log: T3 opens the session's SQLite database under the profile read-only (`busy_timeout = 100`), reads the last step, and matches the error text for `QUOTA_EXHAUSTED`, `quotaResetTimeStamp`, `quotaResetDelay`, `retryDelay` or "Resets in <Go duration>", giving `usage_limit` with a reset time. The path to that database is proved link-free component by component before it is opened.
Read: `AntigravityAdapterV2.ts` (the flavor record); `srv/provider/acp/AntigravityTurnOutcome.ts:1-90` (rest not read); `srv/provider/acp/AntigravityProfilePaths.ts`.
Rules out: declaring a turn done from silence alone; classifying from the pane text.

**D30. Approvals and questions.** ACP `session/request_permission` requests are relayed to the user with the agent's own option ids. Session modes: T3's Full access maps to `yolo`, Auto-accept edits to `auto_edit`, Auto to `auto`, Supervised to `default`; but "the agy CLI rejects `yolo` and `auto_edit`, and sessions without native Auto reject `auto`", so those sessions stay in `default` and T3 answers each permission request from its own runtime policy. T3's own MCP server's tools are allowed unasked in Auto and Auto-accept edits (recognised by the request title `Run mcp_t3-code_<tool>` or `MCP Tool <tool>` with `ServerName == "t3-code"`). A question with fixed choices arrives as a permission request and "still need[s] one of the offered answers, even in Full access".
Read: `AntigravityAcpSupport.ts:156-240`; `AntigravityAdapterV2.ts` (`permissionDisposition`, `approvalOptions`, `extractPermissionQuestion`); `docs/user/providers-antigravity.md`; `docs/user/permission-modes.md`.
Rules out: `--dangerously-skip-permissions` on the ACP path (T3 never passes it; **inferred** from the spawn arguments in D20 being the whole argument list).

**D31. MCP injection: in the ACP session request.** The adapter builds three forms of the same server, named `t3-code`: (a) stdio: `command: <T3's own executable>`, `args: [..., "acp-mcp-bridge"]`, env `T3_ACP_MCP_ENDPOINT`, `T3_ACP_MCP_AUTHORIZATION` (a bridge process that relays JSON-RPC lines to the HTTP endpoint); (b) ACP-native: `{type: "acp", name: "t3-code", serverId: "t3-code"}`; (c) HTTP: `{type: "http", name: "t3-code", url: <endpoint>, headers: [Authorization: Bearer ...]}`, built only for flavors that opt in. The runtime sends, in `mcpServers` of `session/new` and `session/resume`: the ACP form when the agent advertises `mcpCapabilities.acp`, else the HTTP form when it advertises `mcpCapabilities.http` and the flavor opted in, else the stdio bridge ("ACP mandates stdio MCP support; HTTP/SSE support varies"). Antigravity opts in to HTTP (`mcpOverHttp: true`). The thread id, endpoint and authorization also go into the environment of processes the agent starts (`T3CODE_THREAD_ID`, `T3_ACP_MCP_ENDPOINT`, `T3_ACP_MCP_AUTHORIZATION`).
Read: `acp/adapter.ts:1096-1160` (`acpMcpContext`); `acp/AcpSessionRuntime.ts:2374-2388` (`sessionMcpServers`); `AntigravityAdapterV2.ts` (`mcpOverHttp: true`); `srv/mcp/AcpMcpStdioBridge.ts:1-60`.
Rules out: writing the server into the agent's own config files. T3 never edits `mcp_config.json` or settings under the profile for this.

**D32. Instructions and skills.** Session-invariant instructions go in `session/new` as `_meta: {antigravity: {systemPrompt: {append: <text>}}}`, only when the agent advertises `agentCapabilities._meta.antigravity.systemPrompt.append === true`; otherwise, and for the per-turn part, the text is appended as a last text block of the prompt, resent only after a compaction or when it changed. Skills and rules: the private profile isolates credentials, hooks and MCP config, and T3 links the user's global skill folders and `AGENTS.md`, `GEMINI.md`, `config/skills.json` from the real `~/.gemini` into the profile (copies on Windows). Project skills are read from `.gemini/skills`, `.agents/skills`, `.agent/skills`.
Read: `AntigravityAcpSupport.ts:86-104`; `acp/adapter.ts:9285-9335`; `antigravityAuthSupport.ts:256-300` (`linkAntigravityUserConfig`); `srv/provider/Drivers/AntigravitySkills.ts:26`; `docs/user/providers-antigravity.md` (Skills and attachments).
Rules out: a replaced system prompt.

**D33. Usage limits, read two ways.** In band: the per-turn outcome of D29 and the agent's usage updates. Out of band, an active read by the server (`makeAntigravityUsageProbe`), only for Google sign-ins: it reads the CLI's saved token from the profile (`antigravity-acp/acp_token.json`; else `antigravity-acp/antigravity-oauth-token`; else, for Gemini Enterprise only, the OS keyring); for `acp_token.json` it refreshes the access token at `https://oauth2.googleapis.com/token` with the client id, client secret and refresh token stored in that file, and for the CLI token it uses the saved access token only while it has more than 60 seconds left ("It stores no OAuth client, so T3 cannot refresh it"); it calls `https://www.googleapis.com/oauth2/v2/userinfo` to key the account by its Google id; then it POSTs to `https://daily-cloudcode-pa.googleapis.com` and `https://cloudcode-pa.googleapis.com` at `/v1internal:retrieveUserQuotaSummary` (fallback `/v1internal:fetchAvailableModels`) with `Authorization: Bearer <token>` and `User-Agent: antigravity/hub/2.24.0 (aidev_client; os_type=<platform>; arch=<arch>; cl=963137146)`, body `{project: <id>}` or `{}`; 10 seconds per request, 30 for the whole read, redirects not followed. Windows are five-hour and weekly per group. The read is started after a provider status check and after a session start when no limits are known; one read at a time; sign-in and sign-out cancel it. **(inferred)** Cadence: the driver comment says the health check "runs every minute", so the read can run about that often.
Read: `srv/provider/antigravityUsageLimits.ts:20-60, 320-505`; `srv/provider/AntigravityProvider.ts:260-290, 375, 455`; `AntigravityDriver.ts` (comment above `probe`); `docs/user/providers-antigravity.md` (Check access and troubleshoot).
Rules out: nothing by itself. It is the one place T3 uses the agent's token from its own process and sends a user agent naming Antigravity's hub.

**D34. Health check never spawns the agent.** "The probe must not spawn. The agent is a PyInstaller one-file bundle that unpacks about 1 GB per launch, and the health check runs every minute." The status probe answers from the resolved executable and a fixed capability record.
Read: `AntigravityDriver.ts` (`probe`).
Rules out: `agy --version` in a loop.

**D35. Capabilities Antigravity lacks, and how that is shown.** On: model switch in session, runtime-mode switch in session, MCP tools, subagents (read only: "native subagent tools report no child session ids", so children cannot be messaged or controlled). Off (inherited from the generic ACP record): fork, fork from a turn, native resume flagged false at the generic level with resume done through `session/resume`, active steering as a capability (steer is by prompt, D27), several threads per session, dynamic tool callbacks, proposed-plan cards, pending requests surviving a restart. Plan mode is the agent's own `/plan`; compaction is the agent's advertised `/compact`. Auto mode is offered only when the sign-in provides it; a thread set to a mode the agent lacks runs Supervised.
Read: `AntigravityAdapterV2.ts:63-80`; `acp/adapter.ts:869-932`; `docs/user/providers-antigravity.md`; `docs/user/permission-modes.md`.
Rules out: a fork button on an Antigravity thread.

## C. Claude Code

**D36. Connection: the Claude Agent SDK's `query()`, which runs the `claude` CLI as a child.** Protocol tag `claude-agent-sdk.query`. One query (one CLI process) per session, long-lived, fed user messages as a stream (`includePartialMessages: true`). T3 supplies the spawn function so the CLI runs under a group-watch shell in its own process group (D19), and sets `CLAUDE_CODE_ENTRYPOINT=t3-code` in its environment. A binary path can be set per instance.
Read: `srv/orchestration-v2/Adapters/ClaudeAdapterV2.ts:44-72, 265, 436-460, 2640-2735`; `srv/provider/Drivers/ClaudeHome.ts` (`mergeClaudeProcessEnvironment`).
Rules out: a proxy or `ANTHROPIC_BASE_URL`; the print mode with a prompt argument per turn.

**D37. Account = `CLAUDE_CONFIG_DIR`.** An instance with a home path gets `CLAUDE_CONFIG_DIR=<path>` and nothing else changed: "Changing HOME hides macOS Keychain credentials; use CLAUDE_CONFIG_DIR to keep login access." Sign-in is the person running `claude auth login` with that variable on the server's machine; a signed-out instance reports exactly that command. Variables a parent Claude Code session exports (`CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN`, and eight more) are removed so the child is not taken for part of that session. With a custom home the SDK's setting sources are `["project", "local"]` (the "user" source would be the host's home, not the custom one).
Read: `srv/provider/Drivers/ClaudeHome.ts` (whole); `ClaudeAdapterV2.ts:6057`; `docs/user/providers-claude.md`.
Rules out: moving `HOME`; copying credential files between directories.

**D38. Claude conversations do not cross config directories.** "Existing threads can switch only between Claude instances with the same config directory. Separate account directories stay isolated, including their local conversation state. Claude does not have Codex's shared-home and shadow-home arrangement." Switching to an instance with another directory is a portable handoff (D18).
Read: `docs/user/providers-claude.md`; `ClaudeHome.ts` (`makeClaudeContinuationGroupKey`).
Rules out: native resume of a Claude session under another account, as T3 does it.

**D39. Start, resume, fork.** Start: `query()` with model, tools, permission mode, working folder, additional directories, environment, MCP servers, the appended system prompt. Resume: the SDK option `resume: <native session id>` (and `resumeSessionAt` for a point in it). Fork: the SDK's `forkSession`.
Read: `ClaudeAdapterV2.ts:2590, 2640-2735, 15480`.

**D40. Steer and interrupt.** Steer: a user message pushed into the running query's input stream with `priority: "next"` ("`now` makes Claude Code cancel the tool calls in flight and report them as a user refusal"). Interrupt: the query's `interrupt()`. Capabilities: active steering yes, steering does not interrupt tools, queued messages yes.
Read: `ClaudeAdapterV2.ts:14635-14665, 2180, 644-655`.

**D41. Turn end and classification.** A turn ends with the SDK's result message. `usage_limit` when the result carries API status 429, or the stream said the account is limited, or the result text is a gateway 503 that wraps a 429 with "(reset after ...)" (then the reset time is computed from it). `rate_limit_event` frames in the stream carry window updates.
Read: `ClaudeAdapterV2.ts:5084, 5140-5175, 10532, 11141, 12598-12631`.

**D42. Approvals and questions.** The SDK's `canUseTool` callback is installed in every mode, on purpose: it carries tool approvals and the `AskUserQuestion` tool's questions to the user and returns the answer. Approval callbacks are live only (they do not survive a restart). MCP elicitations and user dialogs have their own callbacks (`onElicitation`, `onUserDialog`).
Read: `ClaudeAdapterV2.ts:1602-1603, 2667, 12887-12943, 13153, 672-680`.

**D43. MCP injection: an SDK option.** `mcpServers: {"t3-code": {type: "http", url: <endpoint>, headers: {Authorization: "Bearer ..."}, timeout: <ms>}}` plus `allowedTools` extended with the wildcard for that server (in a read-only sandbox, only the annotated read-only tools). If the machine has a `managed-mcp.json`, `--strict-mcp-config` is not passed, because the managed file "excludes other MCP config and forbids" it.
Read: `ClaudeAdapterV2.ts:2852-2880` (`claudeMcpQueryOverrides`); `ClaudeHome.ts` (`hasClaudeManagedMcpConfig`).
Rules out: editing `~/.claude.json` or `.mcp.json` to add the server.

**D44. Instructions and skills.** `systemPrompt: {type: "preset", preset: "claude_code", append: <runtime instructions, and tool instructions when the MCP server is injected>}`: Claude Code's own prompt with text appended. Skills are discovered from the user and project skill roots and shown to the user; a skill is invoked by rewriting the user's mention to `/name` at the start of the last text block, since "Claude expands one skill per message only when `/name` starts the text block". Plugins are passed as local plugin directories.
Read: `ClaudeAdapterV2.ts:2695-2716`; `srv/provider/Drivers/ClaudeSkillDispatch.ts:1-40`; `srv/provider/Drivers/ClaudeSkills.ts` (903 lines; head only).

**D45. Usage limits.** Read through the CLI, never an HTTP call of T3's own: a probe query that sends no prompt ("never yields, so no prompt reaches the API") calls the SDK's `usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET({skipBehaviors: true})` and gets `rate_limits` (`five_hour`, `seven_day`, model-scoped `seven_day_*`, extra usage); live sessions add `rate_limit_event` frames. An API-key instance has no windows.
Read: `srv/provider/ClaudeProvider.ts:323-324, 520-580, 871-879`; `srv/provider/claudeUsageLimits.ts:25-50, 167-235`.
Rules out: reading Claude's credential to call a usage endpoint.

**D46. Capabilities.** On: model switch in session, rollback, fork, fork from a turn, interrupt, native resume, active steering, queued messages, MCP tools, dynamic tool callbacks, background wait, command, file-read and file-change approvals, structured questions, proposed plans, todo lists. Off: several threads per session, runtime-mode switch in session, thread snapshot read, pending requests surviving a restart, approvals from subagents.
Read: `ClaudeAdapterV2.ts:626-690`.

## D. Codex

**D47. Connection: `codex app-server` over stdio, JSON-RPC.** The server spawns `<codex> app-server -c thread_unload_delay_secs=0 <launch args>` as its child. One app-server per instance serves every thread of that instance ("One app-server serves every Codex thread on an instance, so each frame is routed to the..." thread). `initialize` is sent once ("Codex rejects a second initialize on the shared app-server"); a request with no answer within a deadline retires the session so the next turn spawns a fresh app-server.
Read: `srv/provider/codexLaunchArgs.ts:15-21`; `srv/orchestration-v2/Adapters/CodexAdapterV2.ts:90-92, 624-641, 1826, 1965-1995, 2202-2269`.
Rules out: `codex exec` per turn; a proxy.

**D48. Account = `CODEX_HOME`, with a shadow home to share conversations.** Direct mode: `CODEX_HOME=<home>`. Auth-overlay mode: a shared home holds sessions and configuration; a shadow home per extra account holds its own real `auth.json`, `auth.lock` and `models_cache.json`, and every other entry of the shared home (known: `sessions`, `archived_sessions`, `sqlite`, `shell_snapshots`, `worktrees`, `skills`, `plugins`, `cache`, `logs`, `mcp-oauth-locks`, `thread-writer-locks`, and whatever else is there) is a symlink into the shared home; `log`, `memories`, `tmp` stay local. The shadow's `auth.json` must be a real file, never a link. Both accounts have the continuation key `codex:home:<shared home>`, so "compatible accounts can continue the same thread". Sign-in is the person running `codex login` with `CODEX_HOME` set; the shadow home must not be populated by copying the whole home.
Read: `srv/provider/Drivers/CodexHomeLayout.ts:1-93, 366-468`; `srv/provider/CodexManagedHome.ts`; `docs/user/providers-codex.md:64-107`; `srv/provider/CodexProvider.ts:1004-1005`.
Rules out: copying credentials; a per-account copy of the session store.

**D49. T3 also has a "managed" Codex mode that signs in to ChatGPT through T3 and hands tokens to Codex through a private socket.** Not read beyond its existence and scope name (`chatgpt.tokens.use.direct`).
Read: `srv/provider/Drivers/CodexDriver.ts:171-178`; `srv/provider/CodexManagedTokenBroker.ts` (head); `srv/provider/Drivers/CodexManagedProvider.ts:86-123`.
Rules out, for us: see spec; we do not take it.

**D50. Start, resume, steer, interrupt, fork.** `thread/start`, `thread/resume`, `turn/start`, `turn/steer` (with `expectedTurnId`), `turn/interrupt`, `thread/fork`. Each under a request deadline.
Read: `CodexAdapterV2.ts:3442, 3907, 7992, 8069, 8450-8514, 8690, 9552`.

**D51. Turn end and classification.** `usage_limit` when the error code is `usageLimitExceeded` or `rateLimitExceeded`, or the error's HTTP status is 429. The reset time comes from the rate-limit snapshot; when the first request of a run fails 429 before any snapshot was pushed, the adapter reads `account/rateLimits/read` (10 seconds) before the turn's terminal is published.
Read: `CodexAdapterV2.ts:1360-1382, 5334-5380`.

**D52. Approvals and questions.** Server-to-client requests: `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/permissions/requestApproval`, `item/tool/requestUserInput`, and `mcpServer/elicitation/request` (an unsupported elicitation is cancelled, never left open).
Read: `CodexAdapterV2.ts:6300, 6383, 6456, 6518-6567, 6745`.

**D53. MCP injection: per-thread config overrides in the protocol call.** In the `config` of `thread/start` and `thread/resume`: `"mcp_servers.t3-code": {url, tool_timeout_sec, http_headers: {Authorization: "Bearer ..."}, disabled_tools: [...]}`; a second registration of the same endpoint under another name with only the blocking wait tools enabled and `omit_tools_from: ["code_mode", "deferred"]`; and `shell_environment_policy.set.<thread env var>` for the thread id ("One app-server serves many threads, so the id goes in per-thread shell config, not its env"). "Override only T3's servers so launch-argument MCP servers survive."
Read: `CodexAdapterV2.ts:1700-1752`.
Rules out: writing `config.toml`.

**D54. Instructions.** As `additionalContext` entries of `turn/start`, kind `application`, split into numbered sections "to fit per-entry caps" ("Codex truncates context entries over 4,000 bytes"); kept out of `developer_instructions` because "model-catalog mode text overrides client developer_instructions". `turn/steer` takes no additional context, so unsent notes go as quoted user input.
Read: `srv/provider/CodexDeveloperInstructions.ts:42-62`; `core/runtimeInstructions.ts:58`; `CodexAdapterV2.ts:8684-8700`.

**D55. Usage limits.** From the protocol: the notification `account/rateLimits/updated` and the request `account/rateLimits/read`; the plan from `account/read`.
Read: `CodexAdapterV2.ts:5334-5372`; `srv/provider/CodexProvider.ts:206, 759`.

**D56. Capabilities.** On: several threads per session, model and runtime-mode switch in session, thread snapshot read, rollback, fork (also from a turn, while running, from a subagent thread), interrupt, native resume, active steering, steering by interrupt-restart, queued messages, MCP tools, dynamic tool callbacks. Off: pending requests surviving a restart.
Read: `CodexAdapterV2.ts:349-392`.

## E. Others, one line each

- **OpenCode**: T3 starts `opencode serve --hostname --port` and drives it with `@opencode-ai/sdk/v2`; its MCP server is added by the SDK call `mcp.add`. Read: `packages/provider-opencode/src/server/OpenCodeRuntime.ts:575, 613`; `adapter.ts:1102`.
- **Generic ACP agents** (the ACP Registry or a local ACP command): strict ACP, no vendor extensions or agent-id checks; MCP by the stdio bridge unless the agent advertises ACP or HTTP MCP; "codex-acp 1.2.0 and pi-acp drop injected HTTP servers, so HTTP is opt-in per flavor". Read: `docs/internals/03_providers.md`; `acp/adapter.ts:1105`; `docs/user/providers-acp.md`.
- **Kiro, fx, Grok**: dedicated flavors over the same ACP package (**inferred** from imports of `provider-acp` in `packages/provider-{kiro,fx,grok}/src/server`).
- **Cursor, Pi, Muse**: their own packages (`packages/provider-{cursor,pi,muse}`); not read.

## F. Environments: how T3 links and hosts several machines

Read for this section: `docs/internals/04_environment-auth.md`, `05_remote.md`, `06_t3-connect.md`, `09_connection-runtime.md`, `docs/user/remote-access.md:418-530`, `docs/user/outside-agents.md`, and the heads and comments of `srv/peer/PeerLinks.ts`, `PeerForwarding.ts`, `PeerThreadRelay.ts`, `RemoteDelegation.ts`, `srv/environment/ServerEnvironment.ts`, `packages/tailscale/src/tailscale.ts`. `infra/relay/` was listed, not read: what is said of the relay comes from `06_t3-connect.md`.

**D57. An environment is one server and what it owns; there is no shared server.** "One running server and the machine, credentials, workspace access, and state it owns." Each connection joins a client to one environment over HTTP and WebSocket. Direct access, Tailscale, SSH and T3 Connect "change how the client reaches the server; they do not introduce a different execution model."
Read: `00_glossary.md`; `05_remote.md` (opening).
Rules out: a central server that runs other machines' work; state replicated between machines.

**D58. An environment has an id of its own, independent of how it is reached.** The id is written once to a file, published atomically, with recovery state kept so concurrent initializers pick one winner; it survives restarts and address changes. "Work never silently crosses environments."
Read: `05_remote.md` (Identity is independent of the route); `srv/environment/ServerEnvironment.ts:92-151`.
Rules out: naming a machine by its address or hostname.

**D59. Routes are untrusted hints.** Only the connecting side can prove reachability; a route is verified to answer as the expected environment before any credential is sent; never fall back to loopback when a shareable address is missing; a token paired over HTTPS never downgrades to HTTP. For a peer link, plain HTTP is accepted only "where the network already encrypts: this machine, or a Tailscale address".
Read: `05_remote.md`; `srv/peer/PeerLinks.ts:172-176`.
Rules out: sending a token to whatever answers at an address.

**D60. The client holds one connection per environment and shows them together.** A shared client runtime has one connection owner per environment, a registry scoped by environment, and one retry owner (jittered, capped backoff; a probe before replacing a socket). An involuntary disconnect keeps the registration and the cached projections; cached data stays readable offline but never claims to be live and never overwrites newer data. A missing row never proves a thread is deleted. Reconnection never replays a mutation by itself: "retry and idempotency belong to each operation."
Read: `09_connection-runtime.md`.
Rules out: a hub that merges environments server-side for the client; competing reconnect loops.

**D61. A peer link lets one environment's agents work in another, and is an ordinary MCP sign-in.** Linking signs environment A in to environment B's `/mcp` "as an outside agent with a pairing code from the peer, the same MCP OAuth flow an outside agent uses, without a browser" (client `t3code-peer-link`). B lists and revokes it like any session and owns its expiry and renewal; A keeps B's token in its secret store. A link is one-directional and reaches one machine: it "does not carry on through that machine's own links"; two machines linked both ways are two links. The feature is off by default (a beta) and must be on at both ends.
Read: `05_remote.md` (Peer links); `srv/peer/PeerLinks.ts:93-97`; `docs/user/remote-access.md:418-530`.
Rules out: a second protocol between servers; transitive trust.

**D62. Access over a link only narrows.** A link has an access level chosen when it is made; "an agent also never gets more there than its own mode here": forwarded calls carry the calling agent's modes in a header (`T3-Mode-Limit`) that the receiving side can only intersect downward with the link's level. The tool's own declaration checks the agent on the calling side first; the receiving side then holds the call to the link's access.
Read: `05_remote.md`; `srv/peer/PeerForwarding.ts:38-42`.
Rules out: a link that grants more than either side would grant alone.

**D63. Work a link starts is fenced by its origin.** Threads launched through a link, and their forks, subagents and spawned threads, carry an immutable `linkOrigin`. Below full access, linked work can change only threads with the same origin and cannot touch projects, settings, scheduled tasks or further links. A full-access link is not fenced "because their agents already run as the receiving OS user". Such threads are shown as "From <machine>".
Read: `05_remote.md`; `srv/mcp/linkOrigin.ts` (named there; not read).
Rules out: a remote agent rewriting another machine's own threads through a limited link.

**D64. Idempotency keys are namespaced before forwarding.** All callers on the linking side share one session on the receiving side, so each key is hashed with the caller's namespace.
Read: `05_remote.md`.
Rules out: two agents' retries colliding on the other machine.

**D65. A linked thread is read as the same stream.** A client of A reads a thread on B through A's link as B's own thread subscription "passed on item by item; nothing is held here once the client stops listening"; B sends a heartbeat every 20 seconds, so a quiet stream is known to be dead. A task delegated to a linked machine runs there as an ordinary thread and wakes the delegating thread with its result; it "keeps following the task across restarts", and fails with the reason if the link is revoked.
Read: `srv/peer/PeerThreadRelay.ts:32-41`; `docs/user/remote-access.md` (end of the section); `srv/peer/RemoteDelegation.ts` (imports only).
Rules out: a second transcript format for remote threads.

**D66. Access and reachability are separate; the environment authenticates its own callers.** "Tailscale supplies an endpoint for normal pairing; auth remains the environment's responsibility." The environment issues its own scoped sessions; pairing delegates scopes and can only narrow; every RPC declares and checks its scope, and a successful socket handshake grants nothing further. Guest links and MCP clients are separate audiences.
Read: `05_remote.md` (Access and process ownership are separate); `04_environment-auth.md`.
Rules out, in T3: treating network position as identity. Our spec deviates here on purpose (the owner: the tailnet is the authentication) and says how.

**D67. The public relay (T3 Connect) is a broker, not a data path.** It manages cloud identity, environment links, bootstrap credentials and tunnel allocation; regular traffic goes from the client straight to the environment's tunnel hostname. Its one forwarded path is automation webhooks.
Read: `06_t3-connect.md`.
Rules out: nothing for us; we take none of it (no public endpoint).

**D68. Servers outlive clients; features are negotiated.** "Clients negotiate features via advertised capabilities rather than assuming version parity." A server found already running is not stopped by a client that did not start it.
Read: `05_remote.md`.
Rules out: a client and a server that must be upgraded together.

## G. What T3 does that this list deliberately leaves to the spec

Checkpoints as hidden Git refs, worktrees, the relay and T3 Connect, guest links, the web, desktop and mobile clients, the in-app browser and device tools, workflows, pull-request sync, the model manifest, managed installation and update of harness binaries. [spec.md](spec.md) already says which of these `atlas` does not take.
