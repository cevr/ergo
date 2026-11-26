package kernelprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// EvalTask describes one of the 10 real-workflow benchmark tasks from
// docs/specs/atlas/eval-from-sessions.md §4.
type EvalTask struct {
	ID          int
	Slug        string
	Title       string
	Evidence    string
	Prompt      string
	CellsByLang map[string][]string // "psychoscript", "luau", "ergoscript"
}

// EvalTaskReport records the result of verifying one task against an engine.
type EvalTaskReport struct {
	TaskID int    `json:"task_id"`
	Slug   string `json:"slug"`
	Engine string `json:"engine"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// EvalEnv holds the stateful mock tools and handle registry for running tasks 1..10.
type EvalEnv struct {
	mu           sync.Mutex
	Handles      *HandleRegistry
	Tools        *ToolRegistry
	Files        map[string]string
	Queue        []map[string]any
	Messages     []map[string]any
	Answers      []map[string]any
	Questions    []map[string]any
	Jobs         []map[string]any
	Spawned      []map[string]any
	ShellCalls   []string
	MapCalls     []string
	AfterCutHits atomic.Int32
	SlowHandleID string
	SlowPhase2   atomic.Bool
	MapPhase2    atomic.Bool
}

// NewEvalEnv builds a fresh deterministic workspace mock environment wired to
// atlas's HandleRegistry and ToolRegistry.
func NewEvalEnv() *EvalEnv {
	handles := NewHandleRegistry()
	tools := NewToolRegistry(handles)
	env := &EvalEnv{
		Handles: handles,
		Tools:   tools,
		Files: map[string]string{
			"src/config.ergo": "mode = \"oldMode\"\nworkers = 4\n",
		},
		Queue: []map[string]any{
			{"unit": "u1", "priority": 1},
			{"unit": "u2", "priority": 2},
			{"unit": "u1", "priority": 3},
			{"unit": "u3", "priority": 4},
			{"unit": "u2", "priority": 5},
		},
	}

	units := []map[string]any{
		{"id": "u01", "status": "ready", "deps": 0},
		{"id": "u02", "status": "blocked", "deps": 2},
		{"id": "u03", "status": "ready", "deps": 0},
		{"id": "u04", "status": "ready", "deps": 0},
		{"id": "u05", "status": "blocked", "deps": 1},
		{"id": "u06", "status": "ready", "deps": 0},
		{"id": "u07", "status": "blocked", "deps": 1},
		{"id": "u08", "status": "ready", "deps": 0},
		{"id": "u09", "status": "ready", "deps": 0},
		{"id": "u10", "status": "blocked", "deps": 3},
		{"id": "u11", "status": "ready", "deps": 0},
		{"id": "u12", "status": "blocked", "deps": 1},
	}
	unitMap := make(map[string]map[string]any, len(units))
	for _, u := range units {
		unitMap[u["id"].(string)] = u
	}

	_ = tools.Bind("subagents", ToolSpec{Mode: ToolReadOnly}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		if id, ok := call.Args["id"].(string); ok && id != "" {
			if u, found := unitMap[id]; found {
				return ToolOutcome{Done: true, Value: u}, nil
			}
			return ToolOutcome{ErrKind: "NotFound", ErrMessage: "unknown unit " + id}, nil
		}
		return ToolOutcome{Done: true, Value: units}, nil
	})

	_ = tools.Bind("read", ToolSpec{Mode: ToolReadOnly}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		path, _ := call.Args["path"].(string)
		if path == "" {
			path, _ = call.Args["0"].(string)
		}
		env.mu.Lock()
		content, ok := env.Files[path]
		env.mu.Unlock()
		if !ok {
			if u, found := unitMap[path]; found {
				return ToolOutcome{Done: true, Value: u}, nil
			}
			return ToolOutcome{ErrKind: "NotFound", ErrMessage: "no file " + path}, nil
		}
		return ToolOutcome{Done: true, Value: map[string]any{"path": path, "content": content}}, nil
	})

	_ = tools.Bind("edit", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		path, _ := call.Args["path"].(string)
		oldStr, _ := call.Args["old"].(string)
		newStr, _ := call.Args["new"].(string)
		env.mu.Lock()
		defer env.mu.Unlock()
		content, ok := env.Files[path]
		if !ok {
			return ToolOutcome{ErrKind: "NotFound", ErrMessage: "no file " + path}, nil
		}
		if !strings.Contains(content, oldStr) {
			return ToolOutcome{ErrKind: "EditMismatch", ErrMessage: "old text not found"}, nil
		}
		env.Files[path] = strings.Replace(content, oldStr, newStr, 1)
		return ToolOutcome{Done: true, Value: map[string]any{"updated": true, "path": path}}, nil
	})

	_ = tools.Bind("write", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		path, _ := call.Args["path"].(string)
		if path == "after_cut.marker" {
			env.AfterCutHits.Add(1)
		}
		content, _ := call.Args["content"].(string)
		env.mu.Lock()
		env.Files[path] = content
		env.mu.Unlock()
		return ToolOutcome{Done: true, Value: map[string]any{"written": true, "path": path}}, nil
	})

	_ = tools.Bind("shell", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		cmd, _ := call.Args["command"].(string)
		if cmd == "" {
			cmd, _ = call.Args["0"].(string)
		}
		lane, _ := call.Args["lane"].(string)
		env.mu.Lock()
		env.ShellCalls = append(env.ShellCalls, cmd+":"+lane)
		env.mu.Unlock()

		if call.StartAsync {
			h, err := handles.OpenHostHandle(KindShell, call.OwnerID, call.SessionID, cmd+":"+lane)
			if err != nil {
				return ToolOutcome{}, err
			}
			return ToolOutcome{Unfinished: true, Handle: &h}, nil
		}

		if strings.Contains(cmd, "slow") && !env.SlowPhase2.Load() {
			h, err := handles.OpenHostHandle(KindShell, call.OwnerID, call.SessionID, cmd)
			if err != nil {
				return ToolOutcome{}, err
			}
			env.mu.Lock()
			env.SlowHandleID = h.ID
			env.mu.Unlock()
			return ToolOutcome{Unfinished: true, Handle: &h}, nil
		}
		return ToolOutcome{Done: true, Value: map[string]any{"exit": 0, "stdout": "ok:" + cmd}}, nil
	})

	_ = tools.Bind("subagent", ToolSpec{
		Mode:         ToolSync,
		AllowedRoles: []Role{RoleRoot},
	}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		h, err := handles.OpenHostHandle(KindSubagent, call.OwnerID, call.SessionID, fmt.Sprint(call.Args["role"]))
		if err != nil {
			return ToolOutcome{}, err
		}
		env.mu.Lock()
		env.Spawned = append(env.Spawned, call.Args)
		env.mu.Unlock()
		if call.StartAsync {
			return ToolOutcome{Unfinished: true, Handle: &h}, nil
		}
		return ToolOutcome{Done: true, Value: handleValueMap(h)}, nil
	})

	_ = tools.Bind("message", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		env.mu.Lock()
		env.Messages = append(env.Messages, call.Args)
		env.mu.Unlock()
		return ToolOutcome{Done: true, Value: map[string]any{"sent": true}}, nil
	})

	_ = tools.Bind("answer_subagent", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		env.mu.Lock()
		env.Answers = append(env.Answers, call.Args)
		env.mu.Unlock()
		return ToolOutcome{Done: true, Value: map[string]any{"answered": true}}, nil
	})

	_ = tools.Bind("queue", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		action, _ := call.Args["action"].(string)
		env.mu.Lock()
		defer env.mu.Unlock()
		switch action {
		case "list":
			cp := make([]any, len(env.Queue))
			for i, it := range env.Queue {
				cp[i] = it
			}
			return ToolOutcome{Done: true, Value: cp}, nil
		case "replace":
			raw, _ := call.Args["items"].([]any)
			next := make([]map[string]any, 0, len(raw))
			for _, it := range raw {
				if m, ok := it.(map[string]any); ok {
					next = append(next, m)
				}
			}
			env.Queue = next
			return ToolOutcome{Done: true, Value: map[string]any{"count": len(next)}}, nil
		default:
			return ToolOutcome{ErrKind: "BadQueueAction", ErrMessage: "unknown queue action " + action}, nil
		}
	})

	_ = tools.Bind("checkLane", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		lane, _ := call.Args["lane"].(string)
		if lane == "" {
			lane, _ = call.Args["0"].(string)
		}
		env.mu.Lock()
		env.MapCalls = append(env.MapCalls, lane)
		env.mu.Unlock()
		if !env.MapPhase2.Load() && lane == "L4" {
			h, err := handles.OpenHostHandle(KindShell, call.OwnerID, call.SessionID, "check:"+lane)
			if err != nil {
				return ToolOutcome{}, err
			}
			return ToolOutcome{Unfinished: true, Handle: &h}, nil
		}
		return ToolOutcome{Done: true, Value: map[string]any{"lane": lane, "ok": true}}, nil
	})

	_ = tools.Bind("ask_user_async", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		header, _ := call.Args["header"].(string)
		if len(header) > 30 {
			return ToolOutcome{
				ErrKind:    "SchemaValidationError",
				ErrMessage: fmt.Sprintf("ask_user_async header exceeds 30 chars (%d)", len(header)),
			}, nil
		}
		defVal, _ := call.Args["default"].(string)
		if defVal == "" {
			return ToolOutcome{ErrKind: "SchemaValidationError", ErrMessage: "ask_user_async requires default"}, nil
		}
		h, err := handles.OpenHostHandle(KindQuestion, call.OwnerID, call.SessionID, header)
		if err != nil {
			return ToolOutcome{}, err
		}
		env.mu.Lock()
		env.Questions = append(env.Questions, call.Args)
		env.mu.Unlock()
		return ToolOutcome{Done: true, Value: handleValueMap(h)}, nil
	})

	_ = tools.Bind("job", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		cmd, _ := call.Args["command"].(string)
		h, err := handles.OpenHostHandle(KindJob, call.OwnerID, call.SessionID, cmd)
		if err != nil {
			return ToolOutcome{}, err
		}
		env.mu.Lock()
		env.Jobs = append(env.Jobs, call.Args)
		env.mu.Unlock()
		if call.StartAsync {
			return ToolOutcome{Unfinished: true, Handle: &h}, nil
		}
		return ToolOutcome{Done: true, Value: handleValueMap(h)}, nil
	})

	_ = tools.Bind("sleep", ToolSpec{Mode: ToolSync}, func(ctx context.Context, call ToolCall) (ToolOutcome, error) {
		return ToolOutcome{Done: true, Value: map[string]any{"slept": true}}, nil
	})

	return env
}

// EvalTasks returns the 10 canonical benchmark & Hydrogen-eval tasks from eval-from-sessions.md §4.
func EvalTasks() []EvalTask {
	return []EvalTask{
		{
			ID:       1,
			Slug:     "parallel-status-sweep",
			Title:    "Parallel status sweep across 12 workspace units",
			Evidence: "for loops (4,600 workbox) + tools.all (408) + subagents (701)",
			Prompt:   `Fetch all 12 units via tools.subagents({}) or batched tools.read({path:id}) with tools.all, partition ready vs blocked IDs, format a compact table, and return {ready, blocked, table}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const list = await tools.subagents({});
const details = await tools.all(list.map((u) => tools.read({ path: u.id })));
const ready = [];
const blocked = [];
const rows = [];
for (const u of details) {
  if (u.status === "ready") ready.push(u.id);
  else blocked.push(u.id);
  rows.push(u.id + ":" + u.status);
}
const table = "ready=" + ready.length + " blocked=" + blocked.length + "\n" + rows.join("\n");
return { ready, blocked, table };`,
				},
				"luau": {
					`local list = tools.subagents({})
local calls = {}
for i, u in ipairs(list) do
  calls[i] = function() return tools.read({ path = u.id }) end
end
local details = tools.all(calls)
local ready = {}
local blocked = {}
local rows = {}
for _, u in ipairs(details) do
  if u.status == "ready" then
    table.insert(ready, u.id)
  else
    table.insert(blocked, u.id)
  end
  table.insert(rows, u.id .. ":" .. u.status)
end
local tbl = "ready=" .. tostring(#ready) .. " blocked=" .. tostring(#blocked) .. "\n" .. table.concat(rows, "\n")
return { ready = ready, blocked = blocked, table = tbl }`,
				},
				"ergoscript": {
					`let list = try tools.subagents(Args{})
let mut ready = []any{}
let mut blocked = []any{}
let mut table = ""
for u in list {
  let d = try tools.read(Args{path: u.id})
  if d.status == "ready" {
    ready = append(ready, d.id)
  } else {
    blocked = append(blocked, d.id)
  }
  table = table + "\(d.id):\(d.status)\n"
}
return Args{ready: ready, blocked: blocked, table: "ready=\(len(ready)) blocked=\(len(blocked))\n" + table}`,
				},
			},
		},
		{
			ID:       2,
			Slug:     "fanout-start-handles",
			Title:    "Fan-out build/check starts over 6 lanes returning handles immediately",
			Evidence: "tools.map (84) / non-blocking tools.start.shell",
			Prompt:   `Start 6 shell checks across lanes lane-1..lane-6 using tools.start.shell({lane, command: "go test ./..."}) without waiting, and return {handles}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const lanes = ["lane-1", "lane-2", "lane-3", "lane-4", "lane-5", "lane-6"];
const handles = [];
for (const lane of lanes) {
  handles.push(tools.start.shell({ lane, command: "go test ./..." }));
}
return { handles };`,
				},
				"luau": {
					`local lanes = { "lane-1", "lane-2", "lane-3", "lane-4", "lane-5", "lane-6" }
local handles = {}
for i, lane in ipairs(lanes) do
  handles[i] = tools.start.shell({ lane = lane, command = "go test ./..." })
end
return { handles = handles }`,
				},
				"ergoscript": {
					`let lanes = []any{"lane-1", "lane-2", "lane-3", "lane-4", "lane-5", "lane-6"}
let mut handles = []any{}
for lane in lanes {
  let h = try tools.start.shell(Args{lane: lane, command: "go test ./..."})
  handles = append(handles, h)
}
return Args{handles: handles}`,
				},
			},
		},
		{
			ID:       3,
			Slug:     "cut-and-wake-resumption",
			Title:    "Cut & wake resumption across two cells",
			Evidence: "190 >2m Mac cells & 51 workbox cells >=120s; W2b NoCodeAfterUnfinished",
			Prompt:   `Cell 1 binds T0="/workspaces/ergo", runs await tools.shell({command:"slow-gate"}), and must cut cleanly before tools.write({path:"after_cut.marker"}). Cell 2 reads tools.handles.get({id: handleID}) and T0 and returns {workspace: T0, status, passed}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const T0 = "/workspaces/ergo";
const res = await tools.shell({ command: "slow-gate" });
await tools.write({ path: "after_cut.marker", content: "bad" });
return res;`,
					`const h = await tools.handles.get({ id: kernel.cut.started[0].id });
return { workspace: T0, status: h.state, passed: h.value.passed };`,
				},
				"luau": {
					`T0 = "/workspaces/ergo"
local res = tools.shell({ command = "slow-gate" })
tools.write({ path = "after_cut.marker", content = "bad" })
return res`,
					`local h = tools.handles.get({ id = kernel.cut.started[1].id })
return { workspace = T0, status = h.state, passed = h.value.passed }`,
				},
				"ergoscript": {
					`let T0 = "/workspaces/ergo"
let res = try tools.shell(Args{command: "slow-gate"})
let _ = try tools.write(Args{path: "after_cut.marker", content: "bad"})
return res`,
					`let h = try tools.handles.get(Args{id: handleID})
return Args{workspace: T0, status: h.state, passed: h.value.passed}`,
				},
			},
		},
		{
			ID:       4,
			Slug:     "role-subagent-routing",
			Title:    "Role subagent routing (dispatch 3 workers + message + answer)",
			Evidence: "subagent (1,014) + message (3,262) + answer_subagent (1,246) + W8 role gate",
			Prompt:   `As root, spawn planner, implementor, and verifier via tools.start.subagent, call tools.message and tools.answer_subagent, and return {spawned: 3, messaged: true, answered: true}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const roles = ["planner", "implementor", "verifier"];
const spawned = [];
for (const role of roles) {
  spawned.push(tools.start.subagent({ role, task: "work-" + role }));
}
const m = await tools.message({ to: "implementor", text: "proceed" });
const a = await tools.answer_subagent({ id: "q1", answer: "use IR" });
return { spawned: spawned.length, messaged: m.sent, answered: a.answered };`,
				},
				"luau": {
					`local roles = { "planner", "implementor", "verifier" }
local spawned = {}
for i, role in ipairs(roles) do
  spawned[i] = tools.start.subagent({ role = role, task = "work-" .. role })
end
local m = tools.message({ to = "implementor", text = "proceed" })
local a = tools.answer_subagent({ id = "q1", answer = "use IR" })
return { spawned = #spawned, messaged = m.sent, answered = a.answered }`,
				},
				"ergoscript": {
					`let roles = []any{"planner", "implementor", "verifier"}
let mut count = 0
for role in roles {
  let _ = try tools.start.subagent(Args{role: role, task: "work-" + role})
  count = count + 1
}
let m = try tools.message(Args{to: "implementor", text: "proceed"})
let a = try tools.answer_subagent(Args{id: "q1", answer: "use IR"})
return Args{spawned: count, messaged: m.sent, answered: a.answered}`,
				},
			},
		},
		{
			ID:       5,
			Slug:     "persistent-queue-drain",
			Title:    "Persistent inbox/queue drain with deduplication",
			Evidence: "tools.queue (1,220 workbox)",
			Prompt:   `List pending items via tools.queue({action:"list"}), deduplicate by item.unit preserving order, dispatch top 2 via tools.start.subagent, update remaining queue via tools.queue({action:"replace", items}), and return {dispatched, remaining}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const items = await tools.queue({ action: "list" });
const seen = new Set();
const unique = [];
for (const it of items) {
  if (!seen.has(it.unit)) {
    seen.add(it.unit);
    unique.push(it);
  }
}
const top = unique.slice(0, 2);
const rest = unique.slice(2);
for (const it of top) {
  tools.start.subagent({ role: "implementor", unit: it.unit });
}
await tools.queue({ action: "replace", items: rest });
return { dispatched: top.map((x) => x.unit), remaining: rest.map((x) => x.unit) };`,
				},
				"luau": {
					`local items = tools.queue({ action = "list" })
local seen = {}
local unique = {}
for _, it in ipairs(items) do
  if not seen[it.unit] then
    seen[it.unit] = true
    table.insert(unique, it)
  end
end
local dispatched = {}
local rest = {}
for i, it in ipairs(unique) do
  if i <= 2 then
    tools.start.subagent({ role = "implementor", unit = it.unit })
    table.insert(dispatched, it.unit)
  else
    table.insert(rest, it)
  end
end
tools.queue({ action = "replace", items = rest })
local remUnits = {}
for _, it in ipairs(rest) do table.insert(remUnits, it.unit) end
return { dispatched = dispatched, remaining = remUnits }`,
				},
				"ergoscript": {
					`let items = try tools.queue(Args{action: "list"})
let mut seen = Args{}
let mut unique = []any{}
for it in items {
  let u = it.unit
  if seen[u] != true {
    seen[u] = true
    unique = append(unique, it)
  }
}
let mut dispatched = []any{}
let mut rest = []any{}
let mut restUnits = []any{}
for i, it in unique {
  if i < 2 {
    let _ = try tools.start.subagent(Args{role: "implementor", unit: it.unit})
    dispatched = append(dispatched, it.unit)
  } else {
    rest = append(rest, it)
    restUnits = append(restUnits, it.unit)
  }
}
let _ = try tools.queue(Args{action: "replace", items: rest})
return Args{dispatched: dispatched, remaining: restUnits}`,
				},
			},
		},
		{
			ID:       6,
			Slug:     "code-only-read-edit",
			Title:    "Code-only file inspection & edit without OS spawn",
			Evidence: "Replaces Bun.file/Bun.spawn (0.026%) & 4.1x patch error rate",
			Prompt:   `Read "src/config.ergo" via tools.read, replace "oldMode" with "newMode" via tools.edit, re-read to confirm, and return {updated: true, path: "src/config.ergo"}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const before = await tools.read({ path: "src/config.ergo" });
if (!before.content.includes("oldMode")) return { updated: false };
await tools.edit({ path: "src/config.ergo", old: "oldMode", new: "newMode" });
const after = await tools.read({ path: "src/config.ergo" });
return { updated: after.content.includes("newMode"), path: "src/config.ergo" };`,
				},
				"luau": {
					`local before = tools.read({ path = "src/config.ergo" })
if not string.find(before.content, "oldMode", 1, true) then
  return { updated = false }
end
tools.edit({ path = "src/config.ergo", old = "oldMode", new = "newMode" })
local after = tools.read({ path = "src/config.ergo" })
return { updated = string.find(after.content, "newMode", 1, true) ~= nil, path = "src/config.ergo" }`,
				},
				"ergoscript": {
					`let _ = try tools.read(Args{path: "src/config.ergo"})
let _ = try tools.edit(Args{path: "src/config.ergo", old: "oldMode", new: "newMode"})
let _ = try tools.read(Args{path: "src/config.ergo"})
return Args{updated: true, path: "src/config.ergo"}`,
				},
			},
		},
		{
			ID:       7,
			Slug:     "session-state-and-closure-reject",
			Title:    "Session constant & capped live-state carry across cells",
			Evidence: "91% static bindings + 77 Mac / 9 workbox closure-drop notices",
			Prompt:   `Cell 1 defines T0="/workspaces/ergo", results={passed:4, failed:0}, and attempts to bind a closure helper. Engine must reject the closure cleanly (<=160 char notice or static error) while keeping T0 and results for Cell 2 after Snapshot/Restore.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const T0 = "/workspaces/ergo";
const results = { passed: 4, failed: 0 };
const helper = () => 1;
return { T0, results };`,
					`return { T0, passed: results.passed };`,
				},
				"luau": {
					`T0 = "/workspaces/ergo"
results = { passed = 4, failed = 0 }
helper = function() return 1 end
return { T0 = T0, results = results }`,
					`return { T0 = T0, passed = results.passed }`,
				},
				"ergoscript": {
					`let T0 = "/workspaces/ergo"
let results = Args{passed: 4, failed: 0}
return Args{T0: T0, results: results}`,
					`return Args{T0: T0, passed: results.passed}`,
				},
			},
		},
		{
			ID:       8,
			Slug:     "map-partial-cut-resume",
			Title:    "tools.map partial-cut resume without recomputing completed items",
			Evidence: "kernel.cut.maps recovery (W9) without 100k-item memory crashes",
			Prompt:   `Cell 1 runs tools.map over ["L1".."L8"] calling tools.checkLane({lane}); L1..L3 finish, L4 cuts Unfinished, L5..L8 never start. Cell 2 resumes tools.map(kernel.cut.maps[0].remaining, ...) and returns the resumed count (5).`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const items = ["L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8"];
return await tools.map(items, (lane) => tools.checkLane({ lane }), { concurrency: 1 });`,
					`const rem = kernel.cut.maps[0].remaining;
const done = await tools.map(rem, (lane) => tools.checkLane({ lane }), { concurrency: 1 });
return { resumed: done.length };`,
				},
				"luau": {
					`local items = { "L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8" }
return tools.map(items, function(lane) return tools.checkLane({ lane = lane }) end, { concurrency = 1 })`,
					`local rem = kernel.cut.maps[1].remaining
local done = tools.map(rem, function(lane) return tools.checkLane({ lane = lane }) end, { concurrency = 1 })
return { resumed = #done }`,
				},
				"ergoscript": {
					`let items = []any{"L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8"}
return try tools.Map(items, func(lane string) any { return try tools.checkLane(Args{lane: lane}) })`,
					`let rem = kernel.cut.maps[0].remaining
let done = try tools.Map(rem, func(lane string) any { return try tools.checkLane(Args{lane: lane}) })
return Args{resumed: len(done)}`,
				},
			},
		},
		{
			ID:       9,
			Slug:     "nonblocking-ask-user-async",
			Title:    "Non-blocking owner escalation via ask_user_async",
			Evidence: "ask_user_async (171) + fixing 35m blocking question + 33 header rejects",
			Prompt:   `Emit tools.ask_user_async with <=30-char header, options, and working default, then immediately dispatch tools.start.job({command:"prewarm"}) and return {question_id, job_id, continued: true}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const q = await tools.ask_user_async({
  header: "Approve cutover?",
  question: "Switch live server socket now?",
  options: ["yes", "no"],
  default: "yes",
});
const j = tools.start.job({ command: "prewarm" });
return { question_id: q.id, job_id: j.id, continued: true };`,
				},
				"luau": {
					`local q = tools.ask_user_async({
  header = "Approve cutover?",
  question = "Switch live server socket now?",
  options = { "yes", "no" },
  default = "yes",
})
local j = tools.start.job({ command = "prewarm" })
return { question_id = q.id, job_id = j.id, continued = true }`,
				},
				"ergoscript": {
					`let q = try tools.ask_user_async(Args{header: "Approve cutover?", question: "Switch live server socket now?", options: []any{"yes", "no"}, "default": "yes"})
let j = try tools.start.job(Args{command: "prewarm"})
return Args{question_id: q.id, job_id: j.id, continued: true}`,
				},
			},
		},
		{
			ID:       10,
			Slug:     "declarative-job-monitor",
			Title:    "Declarative job monitor replacing sleep poll",
			Evidence: "Eliminates 199 workbox sleep cells & 275m Mac sleep loop",
			Prompt:   `Register a declarative monitor via tools.start.job({command:"watch-ci", until:"green"}) without calling tools.sleep, returning {monitor_id, kind: "job", sleeps: 0}.`,
			CellsByLang: map[string][]string{
				"psychoscript": {
					`const j = tools.start.job({ command: "watch-ci", until: "green" });
return { monitor_id: j.id, kind: j.kind, sleeps: 0 };`,
				},
				"luau": {
					`local j = tools.start.job({ command = "watch-ci", ["until"] = "green" })
return { monitor_id = j.id, kind = j.kind, sleeps = 0 }`,
				},
				"ergoscript": {
					`let j = try tools.start.job(Args{command: "watch-ci", until: "green"})
return Args{monitor_id: j.id, kind: j.kind, sleeps: 0}`,
				},
			},
		},
	}
}

// VerifyTask executes and checks a task's candidate cell(s) against a KernelEngine
// constructed via factory(env).
func VerifyTask(ctx context.Context, task EvalTask, lang string, factory func(*EvalEnv) (KernelEngine, error), cells []string) EvalTaskReport {
	rep := EvalTaskReport{
		TaskID: task.ID,
		Slug:   task.Slug,
		Engine: lang,
	}
	fail := func(msg string) EvalTaskReport {
		rep.Passed = false
		rep.Detail = msg
		return rep
	}

	env := NewEvalEnv()
	eng, err := factory(env)
	if err != nil {
		return fail("create engine: " + err.Error())
	}
	defer eng.Close()

	baseReq := func(cellID string, role Role, code string) EvalRequest {
		return EvalRequest{
			CellID:    cellID,
			SessionID: fmt.Sprintf("sess-t%d", task.ID),
			OwnerID:   "owner-A/0",
			Role:      role,
			Code:      code,
			Limits:    DefaultLimits(),
		}
	}

	switch task.ID {
	case 1:
		res, err := eng.Eval(ctx, baseReq("t1", RoleRoot, cells[0]))
		if err != nil || res.Status != EvalCompleted {
			return fail(fmt.Sprintf("eval failed: err=%v status=%s errobj=%+v", err, res.Status, res.Error))
		}
		m, _ := res.Value.(map[string]any)
		ready, _ := m["ready"].([]any)
		blocked, _ := m["blocked"].([]any)
		tbl, _ := m["table"].(string)
		if len(ready) != 7 || len(blocked) != 5 || !strings.Contains(tbl, "u01") {
			return fail(fmt.Sprintf("unexpected counts: ready=%d blocked=%d tbl=%q", len(ready), len(blocked), tbl))
		}
	case 2:
		res, err := eng.Eval(ctx, baseReq("t2", RoleRoot, cells[0]))
		if err != nil || res.Status != EvalCompleted {
			return fail(fmt.Sprintf("eval failed: err=%v status=%s errobj=%+v", err, res.Status, res.Error))
		}
		m, _ := res.Value.(map[string]any)
		hs, _ := m["handles"].([]any)
		if len(hs) != 6 || len(env.Handles.List("sess-t2")) != 6 {
			return fail(fmt.Sprintf("expected 6 handles, got %d (registry=%d)", len(hs), len(env.Handles.List("sess-t2"))))
		}
	case 3:
		if len(cells) < 2 {
			return fail("task 3 requires 2 cells")
		}
		settleCh := make(chan AsyncSettleEvent, 4)
		eng.OnAsyncSettle(func(ev AsyncSettleEvent) {
			settleCh <- ev
		})
		res1, err := eng.Eval(ctx, baseReq("t3-c1", RoleRoot, cells[0]))
		if err != nil || res1.Status != EvalCut || res1.Cut == nil || res1.Cut.Reason != CutStarted {
			return fail(fmt.Sprintf("cell 1 expected CutStarted, got status=%s cut=%+v err=%v errobj=%+v", res1.Status, res1.Cut, err, res1.Error))
		}
		if env.AfterCutHits.Load() != 0 {
			return fail("NoCodeAfterUnfinished violated: code after await executed")
		}
		if len(res1.Cut.Started) == 0 {
			return fail("expected started handle in cut.started")
		}
		snap, err := eng.Snapshot(ctx)
		if err != nil {
			return fail("snapshot failed: " + err.Error())
		}
		snap.Cut = res1.Cut

		// Settle the slow handle asynchronously after cell return.
		env.SlowPhase2.Store(true)
		ok, err := env.Handles.Settle(env.SlowHandleID, DispSucceeded, map[string]any{"passed": true, "sha": "abc123"})
		if err != nil || !ok {
			return fail(fmt.Sprintf("settle failed: ok=%v err=%v", ok, err))
		}
		select {
		case <-settleCh:
		default:
			return fail("OnAsyncSettle did not fire synchronously/immediately upon handle settle")
		}

		// Restore into fresh engine instance (simulating retired worker).
		eng2, err := factory(env)
		if err != nil {
			return fail("create engine2: " + err.Error())
		}
		defer eng2.Close()
		if err := eng2.Restore(ctx, snap); err != nil {
			return fail("restore failed: " + err.Error())
		}
		req2 := baseReq("t3-c2", RoleRoot, cells[1])
		req2.Bindings = map[string]any{"handleID": env.SlowHandleID}
		res2, err := eng2.Eval(ctx, req2)
		if err != nil || res2.Status != EvalCompleted {
			return fail(fmt.Sprintf("cell 2 failed: err=%v status=%s errobj=%+v", err, res2.Status, res2.Error))
		}
		m, _ := res2.Value.(map[string]any)
		if m["workspace"] != "/workspaces/ergo" || m["status"] != "settled" || m["passed"] != true {
			return fail(fmt.Sprintf("unexpected resume result: %+v", m))
		}
	case 4:
		res, err := eng.Eval(ctx, baseReq("t4", RoleRoot, cells[0]))
		if err != nil || res.Status != EvalCompleted {
			return fail(fmt.Sprintf("eval failed: err=%v status=%s errobj=%+v", err, res.Status, res.Error))
		}
		m, _ := res.Value.(map[string]any)
		if int(toFloat(m["spawned"])) != 3 || m["messaged"] != true || m["answered"] != true {
			return fail(fmt.Sprintf("unexpected subagent routing result: %+v", m))
		}
		// Negative role check (W8): RoleWorker cannot reach subagent.
		deniedCode := `return await tools.subagent({ role: "worker" });`
		if lang == "luau" {
			deniedCode = `return tools.subagent({ role = "worker" })`
		} else if lang == "ergoscript" {
			deniedCode = `return try tools.subagent(Args{role: "worker"})`
		}
		wRes, _ := eng.Eval(ctx, baseReq("t4-worker", RoleWorker, deniedCode))
		if wRes.Status == EvalCompleted {
			return fail("W8 violation: worker role was allowed to call tools.subagent")
		}
	case 5:
		res, err := eng.Eval(ctx, baseReq("t5", RoleRoot, cells[0]))
		if err != nil || res.Status != EvalCompleted {
			return fail(fmt.Sprintf("eval failed: err=%v status=%s errobj=%+v", err, res.Status, res.Error))
		}
		raw, _ := json.Marshal(res.Value)
		var out struct {
			Dispatched []string `json:"dispatched"`
			Remaining  []string `json:"remaining"`
		}
		_ = json.Unmarshal(raw, &out)
		if !slices.Equal(out.Dispatched, []string{"u1", "u2"}) || !slices.Equal(out.Remaining, []string{"u3"}) {
			return fail(fmt.Sprintf("unexpected queue result: %s", string(raw)))
		}
	case 6:
		res, err := eng.Eval(ctx, baseReq("t6", RoleRoot, cells[0]))
		if err != nil || res.Status != EvalCompleted {
			return fail(fmt.Sprintf("eval failed: err=%v status=%s errobj=%+v", err, res.Status, res.Error))
		}
		m, _ := res.Value.(map[string]any)
		if m["updated"] != true || !strings.Contains(env.Files["src/config.ergo"], "newMode") {
			return fail(fmt.Sprintf("file edit failed: %+v content=%q", m, env.Files["src/config.ergo"]))
		}
	case 7:
		if len(cells) < 2 {
			return fail("task 7 requires 2 cells")
		}
		res1, err := eng.Eval(ctx, baseReq("t7-c1", RoleRoot, cells[0]))
		if err != nil || res1.Status != EvalCompleted {
			return fail(fmt.Sprintf("cell 1 failed: err=%v status=%s errobj=%+v", err, res1.Status, res1.Error))
		}
		snap, err := eng.Snapshot(ctx)
		if err != nil {
			return fail("snapshot failed: " + err.Error())
		}
		if lang != "ergoscript" {
			if len(res1.LostBindings) == 0 && len(snap.LostBindings) == 0 {
				return fail("expected closure binding 'helper' to be reported in LostBindings")
			}
			if len(snap.Notice) == 0 || len(snap.Notice) > MaxLostNoticeBytes {
				return fail(fmt.Sprintf("invalid lost notice length %d: %q", len(snap.Notice), snap.Notice))
			}
		} else {
			// For ergoscript, verify closure assignment into session fails statically!
			badRes, _ := eng.Eval(ctx, baseReq("t7-bad", RoleRoot, `session.helper = func() { return 1 }`))
			if badRes.Status == EvalCompleted || badRes.Error == nil || badRes.Error.Kind != "NonSerializableBinding" {
				return fail(fmt.Sprintf("ergoscript must refuse closure binding statically, got %+v", badRes))
			}
		}

		eng2, err := factory(env)
		if err != nil {
			return fail("create engine2: " + err.Error())
		}
		defer eng2.Close()
		if err := eng2.Restore(ctx, snap); err != nil {
			return fail("restore failed: " + err.Error())
		}
		res2, err := eng2.Eval(ctx, baseReq("t7-c2", RoleRoot, cells[1]))
		if err != nil || res2.Status != EvalCompleted {
			return fail(fmt.Sprintf("cell 2 failed: err=%v status=%s errobj=%+v", err, res2.Status, res2.Error))
		}
		m, _ := res2.Value.(map[string]any)
		if m["T0"] != "/workspaces/ergo" || int(toFloat(m["passed"])) != 4 {
			return fail(fmt.Sprintf("persisted state mismatch: %+v", m))
		}
	case 8:
		if len(cells) < 2 {
			return fail("task 8 requires 2 cells")
		}
		res1, err := eng.Eval(ctx, baseReq("t8-c1", RoleRoot, cells[0]))
		if err != nil || res1.Status != EvalCut || res1.Cut == nil || len(res1.Cut.Maps) == 0 {
			return fail(fmt.Sprintf("cell 1 expected Cut with maps, got status=%s cut=%+v err=%v errobj=%+v", res1.Status, res1.Cut, err, res1.Error))
		}
		mc := res1.Cut.Maps[0]
		if len(mc.Finished) != 3 || len(mc.Remaining) != 5 {
			return fail(fmt.Sprintf("expected 3 finished and 5 remaining, got finished=%d remaining=%d (%+v)", len(mc.Finished), len(mc.Remaining), mc))
		}
		snap, err := eng.Snapshot(ctx)
		if err != nil {
			return fail("snapshot failed: " + err.Error())
		}
		snap.Cut = res1.Cut
		env.MapPhase2.Store(true)

		eng2, err := factory(env)
		if err != nil {
			return fail("create engine2: " + err.Error())
		}
		defer eng2.Close()
		if err := eng2.Restore(ctx, snap); err != nil {
			return fail("restore failed: " + err.Error())
		}
		res2, err := eng2.Eval(ctx, baseReq("t8-c2", RoleRoot, cells[1]))
		if err != nil || res2.Status != EvalCompleted {
			return fail(fmt.Sprintf("cell 2 failed: err=%v status=%s errobj=%+v", err, res2.Status, res2.Error))
		}
		m, _ := res2.Value.(map[string]any)
		if int(toFloat(m["resumed"])) != 5 {
			return fail(fmt.Sprintf("expected 5 resumed items, got %+v", m))
		}
		// L1..L3 ran once in cell 1, L4 ran in cell 1 (cut) + cell 2, L5..L8 ran once in cell 2 => 4 + 5 = 9 total calls!
		env.mu.Lock()
		totalMapCalls := len(env.MapCalls)
		env.mu.Unlock()
		if totalMapCalls != 9 {
			return fail(fmt.Sprintf("expected 9 total checkLane calls (zero recompute of L1..L3), got %d: %v", totalMapCalls, env.MapCalls))
		}
	case 9:
		res, err := eng.Eval(ctx, baseReq("t9", RoleRoot, cells[0]))
		if err != nil || res.Status != EvalCompleted {
			return fail(fmt.Sprintf("eval failed: err=%v status=%s errobj=%+v", err, res.Status, res.Error))
		}
		m, _ := res.Value.(map[string]any)
		if m["continued"] != true || m["question_id"] == "" || m["job_id"] == "" {
			return fail(fmt.Sprintf("unexpected ask_user_async result: %+v", m))
		}
	case 10:
		res, err := eng.Eval(ctx, baseReq("t10", RoleRoot, cells[0]))
		if err != nil || res.Status != EvalCompleted {
			return fail(fmt.Sprintf("eval failed: err=%v status=%s errobj=%+v", err, res.Status, res.Error))
		}
		m, _ := res.Value.(map[string]any)
		if m["kind"] != "job" || int(toFloat(m["sleeps"])) != 0 || len(env.Jobs) != 1 {
			return fail(fmt.Sprintf("unexpected monitor result: %+v jobs=%d", m, len(env.Jobs)))
		}
	default:
		return fail("unknown task ID")
	}

	rep.Passed = true
	rep.Detail = "ok"
	return rep
}
