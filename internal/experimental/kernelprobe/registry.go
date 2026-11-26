package kernelprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxLostNoticeBytes = 160
	MaxWakeLetterBytes = 2000
	MaxRoleLiveSlots   = 32
)

// HandleRegistry enforces atlas's DELIVERS exclusivity and single-wake invariants
// across cuts, settles, and owner reloads (docs/specs/atlas/models/kernel/kernel.ergo).
type HandleRegistry struct {
	mu        sync.Mutex
	nextID    atomic.Uint64
	handles   map[string]*WakeHandle
	letters   map[string]int
	listeners []AsyncSettleFunc
}

// NewHandleRegistry creates an empty wake handle registry.
func NewHandleRegistry() *HandleRegistry {
	return &HandleRegistry{
		handles: make(map[string]*WakeHandle),
		letters: make(map[string]int),
	}
}

// OnAsyncSettle registers a callback fired at most once when an open handle settles.
func (r *HandleRegistry) OnAsyncSettle(cb AsyncSettleFunc) {
	if cb == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, cb)
}

// OpenRegistryHandle registers a call or land handle owned by the registry.
// Attempting to open any non-registry kind fails with ErrDelivererCollision.
func (r *HandleRegistry) OpenRegistryHandle(kind HandleKind, owner, sessionID, summary string) (WakeHandle, error) {
	del, err := DeliversOf(kind)
	if err != nil {
		return WakeHandle{}, err
	}
	if del != DelivererRegistry {
		return WakeHandle{}, fmt.Errorf("%w: %s belongs to %s", ErrDelivererCollision, kind, del)
	}
	return r.openWithDeliverer(kind, del, owner, sessionID, summary), nil
}

// OpenHostHandle registers a non-registry handle (shell, job, subagent, timer, question)
// owned by its sole DELIVERS deliverer so handles.list/get can inspect all seven kinds.
func (r *HandleRegistry) OpenHostHandle(kind HandleKind, owner, sessionID, summary string) (WakeHandle, error) {
	del, err := DeliversOf(kind)
	if err != nil {
		return WakeHandle{}, err
	}
	return r.openWithDeliverer(kind, del, owner, sessionID, summary), nil
}

func (r *HandleRegistry) openWithDeliverer(kind HandleKind, del Deliverer, owner, sessionID, summary string) WakeHandle {
	n := r.nextID.Add(1)
	h := WakeHandle{
		ID:          fmt.Sprintf("h-%s-%d", kind, n),
		Kind:        kind,
		Owner:       owner,
		SessionID:   sessionID,
		Deliverer:   del,
		Disposition: DispOpen,
		Summary:     summary,
	}
	r.mu.Lock()
	r.handles[h.ID] = &h
	r.mu.Unlock()
	cp := h
	return cp
}

// MarkHanded marks a registry call handle as terminal Handed when its detached
// call answered Unfinished and spawned a target handle (HandedNeverDoubleWakes).
func (r *HandleRegistry) MarkHanded(callID, spawnedID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.handles[callID]
	if !ok {
		return fmt.Errorf("unknown handle %q", callID)
	}
	if h.Kind != KindCall {
		return fmt.Errorf("only call handles can transition to handed, got %s", h.Kind)
	}
	if h.Disposition != DispOpen {
		return nil
	}
	h.Disposition = DispHanded
	h.StartedID = spawnedID
	return nil
}

// Settle transitions an open handle to a terminal disposition once (first settle wins)
// and notifies registered listeners with at most one wake event unless Handed.
func (r *HandleRegistry) Settle(handleID string, disp HandleDisposition, val any) (bool, error) {
	r.mu.Lock()
	h, ok := r.handles[handleID]
	if !ok {
		r.mu.Unlock()
		return false, fmt.Errorf("unknown handle %q", handleID)
	}
	if h.Disposition != DispOpen {
		r.mu.Unlock()
		return false, nil
	}
	if disp == DispOpen || disp == DispHanded {
		r.mu.Unlock()
		return false, fmt.Errorf("invalid terminal settle disposition %q", disp)
	}
	h.Disposition = disp
	h.Value = val
	if r.letters[handleID] > 0 {
		r.mu.Unlock()
		return false, nil
	}
	r.letters[handleID] = 1
	cp := *h
	listeners := append([]AsyncSettleFunc(nil), r.listeners...)
	r.mu.Unlock()

	ev := AsyncSettleEvent{
		Handle:    cp,
		Letter:    formatWakeLetter(cp),
		SettledAt: time.Now().UTC(),
	}
	for _, cb := range listeners {
		cb(ev)
	}
	return true, nil
}

// RetireOwner settles only ownerID's open call handles as DispLost, keeping live
// calls belonging to other owners untouched (OtherOwnerReloadKeepsLiveCalls).
func (r *HandleRegistry) RetireOwner(ownerID string) []WakeHandle {
	r.mu.Lock()
	var toRetire []string
	for id, h := range r.handles {
		if h.Owner == ownerID && h.Kind == KindCall && h.Disposition == DispOpen {
			toRetire = append(toRetire, id)
		}
	}
	slices.Sort(toRetire)
	r.mu.Unlock()

	var retired []WakeHandle
	for _, id := range toRetire {
		ok, err := r.Settle(id, DispLost, map[string]any{"status": "lost", "reason": "owner_retired"})
		if err == nil && ok {
			if h, exists := r.Get(id); exists {
				retired = append(retired, h)
			}
		}
	}
	return retired
}

// Get returns a copy of the handle record by ID.
func (r *HandleRegistry) Get(handleID string) (WakeHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.handles[handleID]
	if !ok {
		return WakeHandle{}, false
	}
	return *h, true
}

// List returns all handles belonging to sessionID in deterministic ID order.
func (r *HandleRegistry) List(sessionID string) []WakeHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []WakeHandle
	for _, h := range r.handles {
		if sessionID == "" || h.SessionID == sessionID {
			out = append(out, *h)
		}
	}
	slices.SortFunc(out, func(a, b WakeHandle) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// LetterCount returns how many wake letters were emitted for handleID.
func (r *HandleRegistry) LetterCount(handleID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.letters[handleID]
}

func formatWakeLetter(h WakeHandle) string {
	line := fmt.Sprintf("wake handle=%s kind=%s status=%s", h.ID, h.Kind, h.Disposition)
	if h.Summary != "" {
		line += " summary=" + h.Summary
	}
	if len(line) > MaxWakeLetterBytes {
		line = line[:MaxWakeLetterBytes]
	}
	return line
}

// FormatLostNotice formats the kernel notice strictly when bindings were lost
// (NoticeOnlyWhenBindingsLost, <= 160 chars). Returns "" when nothing was lost.
func FormatLostNotice(lost []string) string {
	if len(lost) == 0 {
		return ""
	}
	shown := lost
	more := 0
	if len(shown) > 3 {
		more = len(shown) - 3
		shown = shown[:3]
	}
	msg := fmt.Sprintf("[new kernel; lost %d bindings: %s", len(lost), strings.Join(shown, ", "))
	if more > 0 {
		msg += fmt.Sprintf(" (+%d more)", more)
	}
	msg += "; see kernel.bindings()]"
	if len(msg) > MaxLostNoticeBytes {
		msg = msg[:MaxLostNoticeBytes-3] + "...]"
	}
	return msg
}

// SlotPool enforces W5e: roots never queue; role workers share MaxRoleLiveSlots.
type SlotPool struct {
	mu      sync.Mutex
	maxLive int
	live    int
	waiters []chan struct{}
}

// NewSlotPool constructs a role slot pool with cap maxLive (default 32).
func NewSlotPool(maxLive int) *SlotPool {
	if maxLive <= 0 {
		maxLive = MaxRoleLiveSlots
	}
	return &SlotPool{maxLive: maxLive}
}

// Acquire grants an execution slot. RoleRoot always enters immediately without queuing.
func (p *SlotPool) Acquire(ctx context.Context, role Role) (queued bool, release func(), err error) {
	if role == RoleRoot || role == RoleAutomation {
		return false, func() {}, nil
	}
	p.mu.Lock()
	if p.live < p.maxLive {
		p.live++
		p.mu.Unlock()
		return false, p.releaseSlot, nil
	}
	ch := make(chan struct{})
	p.waiters = append(p.waiters, ch)
	p.mu.Unlock()

	select {
	case <-ch:
		return true, p.releaseSlot, nil
	case <-ctx.Done():
		p.mu.Lock()
		for i, w := range p.waiters {
			if w == ch {
				p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		return true, nil, ctx.Err()
	}
}

func (p *SlotPool) releaseSlot() {
	p.mu.Lock()
	if len(p.waiters) > 0 {
		w := p.waiters[0]
		p.waiters = p.waiters[1:]
		close(w)
		p.mu.Unlock()
		return
	}
	if p.live > 0 {
		p.live--
	}
	p.mu.Unlock()
}

// WaitersLen returns the number of currently queued non-root slot requests.
func (p *SlotPool) WaitersLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.waiters)
}

// ToolRegistry holds bound host tools and handles role-filtered dispatch, call
// journaling, and built-in handles.* / map execution for engines.
type ToolRegistry struct {
	mu      sync.RWMutex
	tools   map[string]registeredTool
	handles *HandleRegistry
}

type registeredTool struct {
	spec    ToolSpec
	handler ToolHandler
}

// NewToolRegistry creates a tool registry wired to handles (allocating one if nil).
func NewToolRegistry(handles *HandleRegistry) *ToolRegistry {
	if handles == nil {
		handles = NewHandleRegistry()
	}
	return &ToolRegistry{
		tools:   make(map[string]registeredTool),
		handles: handles,
	}
}

// Handles returns the underlying HandleRegistry.
func (tr *ToolRegistry) Handles() *HandleRegistry {
	return tr.handles
}

// Bind registers or updates a tool binding.
func (tr *ToolRegistry) Bind(name string, spec ToolSpec, handler ToolHandler) error {
	if name == "" || handler == nil {
		return fmt.Errorf("invalid tool binding %q", name)
	}
	spec.Name = name
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.tools[name] = registeredTool{spec: spec, handler: handler}
	return nil
}

// OnAsyncSettle forwards settlement subscriptions to the handle registry.
func (tr *ToolRegistry) OnAsyncSettle(cb AsyncSettleFunc) {
	tr.handles.OnAsyncSettle(cb)
}

// CallSession tracks per-cell call journal, started handles, and remaining wait budget.
type CallSession struct {
	mu           sync.Mutex
	sessionID    string
	ownerID      string
	role         Role
	waitDeadline time.Time
	hasDeadline  bool
	registry     *ToolRegistry
	calls        []CallJournalEntry
	started      []WakeHandle
	maps         []MapCutRecord
	sleeps       int
	nextCallSeq  atomic.Uint64
}

// NewCallSession starts tracking tool calls for one cell execution.
func (tr *ToolRegistry) NewCallSession(req EvalRequest) *CallSession {
	wait := req.RemainingWait
	if wait <= 0 {
		wait = req.Limits.WaitTimeout
	}
	cs := &CallSession{
		sessionID: req.SessionID,
		ownerID:   req.OwnerID,
		role:      req.Role,
		registry:  tr,
	}
	if wait > 0 {
		cs.waitDeadline = time.Now().Add(wait)
		cs.hasDeadline = true
	}
	return cs
}

// RemainingWait returns the cell's remaining wait budget.
func (cs *CallSession) RemainingWait() time.Duration {
	if !cs.hasDeadline {
		return DefaultLimits().WaitTimeout
	}
	rem := time.Until(cs.waitDeadline)
	if rem < 0 {
		return 0
	}
	return rem
}

// Invoke dispatches a tool call (including built-in handles.get / handles.list / handles.cancel)
// and updates cut.calls / cut.started. Returns cutStarted=true if an ordinary awaited call
// returned Unfinished and the cell must stop immediately at that await.
func (cs *CallSession) Invoke(ctx context.Context, toolName string, args map[string]any, startAsync bool) (out ToolOutcome, cutStarted bool, err error) {
	if args == nil {
		args = map[string]any{}
	}
	if strings.HasPrefix(toolName, "handles.") {
		return cs.invokeHandles(strings.TrimPrefix(toolName, "handles."), args)
	}
	if toolName == "sleep" {
		cs.mu.Lock()
		cs.sleeps++
		cs.mu.Unlock()
	}

	cs.registry.mu.RLock()
	reg, ok := cs.registry.tools[toolName]
	cs.registry.mu.RUnlock()
	if !ok {
		return ToolOutcome{
			ErrKind:    "MissingTool",
			ErrMessage: fmt.Sprintf("tools.%s is not bound", toolName),
		}, false, nil
	}
	if !reg.spec.Allows(cs.role) {
		return ToolOutcome{
			ErrKind:    "RoleDenied",
			ErrMessage: fmt.Sprintf("role %s cannot call tools.%s", cs.role, toolName),
		}, false, nil
	}

	rem := cs.RemainingWait()
	callCtx := ctx
	var cancel context.CancelFunc
	if cs.hasDeadline {
		callCtx, cancel = context.WithDeadline(ctx, cs.waitDeadline)
		defer cancel()
	}

	call := ToolCall{
		CallID:        fmt.Sprintf("%s:c%d", cs.sessionID, cs.nextCallSeq.Add(1)),
		SessionID:     cs.sessionID,
		OwnerID:       cs.ownerID,
		Role:          cs.role,
		ToolName:      toolName,
		Args:          args,
		RemainingWait: rem,
		StartAsync:    startAsync || reg.spec.Mode == ToolAsyncStart,
	}

	if bg, ok := args["background"].(bool); ok && bg {
		call.StartAsync = true
	}

	res, callErr := reg.handler(callCtx, call)
	if callErr != nil {
		if ctx.Err() != nil || callCtx.Err() == context.DeadlineExceeded {
			h, openErr := cs.registry.handles.OpenRegistryHandle(KindCall, cs.ownerID, cs.sessionID, toolName)
			if openErr == nil {
				cs.mu.Lock()
				cs.calls = append(cs.calls, CallJournalEntry{
					Index:    len(cs.calls),
					Tool:     toolName,
					Input:    args,
					Status:   CallPending,
					HandleID: h.ID,
				})
				cs.started = append(cs.started, h)
				cs.mu.Unlock()
				return ToolOutcome{
					Unfinished: true,
					Handle:     &h,
				}, true, nil
			}
		}
		return ToolOutcome{
			ErrKind:    "ToolError",
			ErrMessage: callErr.Error(),
		}, false, nil
	}

	if res.ErrKind != "" || res.ErrMessage != "" {
		return res, false, nil
	}

	if res.Unfinished {
		if res.Handle == nil {
			h, err := cs.registry.handles.OpenRegistryHandle(KindCall, cs.ownerID, cs.sessionID, toolName)
			if err != nil {
				return ToolOutcome{}, false, err
			}
			res.Handle = &h
		}
		cs.mu.Lock()
		idx := len(cs.calls)
		if call.StartAsync {
			cs.calls = append(cs.calls, CallJournalEntry{
				Index:    idx,
				Tool:     toolName,
				Input:    args,
				Status:   CallStarted,
				HandleID: res.Handle.ID,
				Output:   handleValueMap(*res.Handle),
			})
			cs.mu.Unlock()
			return ToolOutcome{
				Done:   true,
				Value:  handleValueMap(*res.Handle),
				Handle: res.Handle,
			}, false, nil
		}
		cs.calls = append(cs.calls, CallJournalEntry{
			Index:    idx,
			Tool:     toolName,
			Input:    args,
			Status:   CallStarted,
			HandleID: res.Handle.ID,
		})
		cs.started = append(cs.started, *res.Handle)
		cs.mu.Unlock()
		return res, true, nil
	}

	cs.mu.Lock()
	cs.calls = append(cs.calls, CallJournalEntry{
		Index:  len(cs.calls),
		Tool:   toolName,
		Input:  args,
		Status: CallFinished,
		Output: res.Value,
	})
	cs.mu.Unlock()
	res.Done = true
	return res, false, nil
}

func (cs *CallSession) invokeHandles(action string, args map[string]any) (ToolOutcome, bool, error) {
	switch action {
	case "get":
		id, _ := args["id"].(string)
		if id == "" {
			if raw, ok := args["0"].(string); ok {
				id = raw
			}
		}
		h, ok := cs.registry.handles.Get(id)
		if !ok {
			return ToolOutcome{
				Done: true,
				Value: map[string]any{
					"id":          id,
					"state":       "missing",
					"disposition": "missing",
				},
			}, false, nil
		}
		return ToolOutcome{Done: true, Value: handleValueMap(h)}, false, nil
	case "list":
		list := cs.registry.handles.List(cs.sessionID)
		items := make([]any, len(list))
		for i, h := range list {
			items[i] = handleValueMap(h)
		}
		return ToolOutcome{Done: true, Value: items}, false, nil
	case "cancel":
		id, _ := args["id"].(string)
		if id == "" {
			if raw, ok := args["0"].(string); ok {
				id = raw
			}
		}
		ok, err := cs.registry.handles.Settle(id, DispCancelled, nil)
		return ToolOutcome{Done: true, Value: map[string]any{"id": id, "cancelled": ok, "error": errString(err)}}, false, nil
	default:
		return ToolOutcome{ErrKind: "MissingTool", ErrMessage: "unknown tools.handles." + action}, false, nil
	}
}

func handleValueMap(h WakeHandle) map[string]any {
	state := "open"
	if h.Disposition != DispOpen {
		state = "settled"
	}
	m := map[string]any{
		"id":          h.ID,
		"kind":        string(h.Kind),
		"deliverer":   string(h.Deliverer),
		"state":       state,
		"disposition": string(h.Disposition),
	}
	if h.Value != nil {
		m["value"] = h.Value
	}
	if h.StartedID != "" {
		m["started_id"] = h.StartedID
	}
	if h.Summary != "" {
		m["summary"] = h.Summary
	}
	return m
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// RecordMapCut records a tools.map partial-cut descriptor into cut.maps.
func (cs *CallSession) RecordMapCut(rec MapCutRecord) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.maps = append(cs.maps, rec)
}

// BuildCutInfo snapshots the current call journal and started handles into CutInfo.
func (cs *CallSession) BuildCutInfo(reason CutReason, lost []string) CutInfo {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	calls := append([]CallJournalEntry(nil), cs.calls...)
	started := append([]WakeHandle(nil), cs.started...)
	maps := append([]MapCutRecord(nil), cs.maps...)
	notice := FormatLostNotice(lost)
	return CutInfo{
		Reason:       reason,
		Started:      started,
		Calls:        calls,
		Maps:         maps,
		Sleeps:       cs.sleeps,
		LostBindings: append([]string(nil), lost...),
		Notice:       notice,
	}
}

// DecodeArgsJSON normalizes positional or named JSON arguments into map[string]any.
func DecodeArgsJSON(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]any{}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil && obj != nil {
		return obj
	}
	var arr []any
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		if len(arr) == 1 {
			if m, ok := arr[0].(map[string]any); ok {
				return m
			}
		}
		m := make(map[string]any, len(arr))
		for i, v := range arr {
			m[fmt.Sprintf("%d", i)] = v
		}
		return m
	}
	var scalar any
	if err := json.Unmarshal([]byte(raw), &scalar); err == nil {
		return map[string]any{"0": scalar}
	}
	return map[string]any{"raw": raw}
}
