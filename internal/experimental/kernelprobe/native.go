package kernelprobe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type nativeWire interface {
	EvalJSON(reqJSON string) (string, error)
	RestoreJSON(snapJSON string) (string, error)
	SnapshotJSON() (string, error)
	CutJSON(reason string) (string, error)
	Kill()
	Close()
}

type nativeEngine struct {
	name     string
	libPath  string
	tools    *ToolRegistry
	slots    *SlotPool
	mu       sync.Mutex
	closed   bool
	wire     nativeWire
	lastSnap SnapshotBlob
	bridge   *evalBridge
	bridgeID uintptr
	bindings map[string]any
	lastCut  *CutInfo
	lastLost []string
}

type evalBridge struct {
	mu      sync.Mutex
	engine  *nativeEngine
	session *CallSession
	ctx     context.Context
	maps    map[string]*mapTracker
	nextMap atomic.Uint64
	activeM string
	activeI int
}

type mapTracker struct {
	items   []any
	states  []string // "", "pending", "started", "finished"
	results []any
	handles []string
}

func (b *evalBridge) handleTool(toolName, argsJSON string, startAsync bool) (int, string) {
	if toolName == "__map_begin" || toolName == "__map_start" || toolName == "__map_finish" {
		return b.handleMapHook(toolName, argsJSON)
	}
	b.mu.Lock()
	ctx := b.ctx
	cs := b.session
	b.mu.Unlock()
	if ctx == nil || cs == nil {
		out, _ := json.Marshal(map[string]string{"kind": "NoSession", "message": "no active eval session"})
		return -1, string(out)
	}
	args := DecodeArgsJSON(argsJSON)
	out, cutStarted, err := cs.Invoke(ctx, toolName, args, startAsync)
	if err != nil {
		b.mu.Lock()
		if b.activeM != "" {
			if mt := b.maps[b.activeM]; mt != nil && b.activeI >= 0 && b.activeI < len(mt.states) && mt.states[b.activeI] == "pending" {
				mt.states[b.activeI] = "pending"
			}
		}
		b.mu.Unlock()
		res, _ := json.Marshal(map[string]string{"kind": "ToolError", "message": err.Error()})
		return -1, string(res)
	}
	if cutStarted {
		b.mu.Lock()
		if b.activeM != "" && out.Handle != nil {
			if mt := b.maps[b.activeM]; mt != nil && b.activeI >= 0 && b.activeI < len(mt.states) {
				mt.states[b.activeI] = "pending"
				mt.handles[b.activeI] = out.Handle.ID
			}
		}
		b.mu.Unlock()
		res, _ := json.Marshal(map[string]any{"handle": out.Handle})
		return 1, string(res)
	}
	if out.ErrKind != "" || out.ErrMessage != "" {
		res, _ := json.Marshal(map[string]string{"kind": out.ErrKind, "message": out.ErrMessage})
		return -1, string(res)
	}
	if out.Unfinished && out.Handle != nil {
		res, _ := json.Marshal(map[string]any{"handle": handleValueMap(*out.Handle)})
		return 1, string(res)
	}
	res, _ := json.Marshal(map[string]any{"value": out.Value})
	return 0, string(res)
}

func (b *evalBridge) handleMapHook(op, argsJSON string) (int, string) {
	args := DecodeArgsJSON(argsJSON)
	b.mu.Lock()
	defer b.mu.Unlock()
	switch op {
	case "__map_begin":
		var items []any
		if raw, ok := args["items"].([]any); ok {
			items = raw
		}
		id := fmt.Sprintf("map-%d", b.nextMap.Add(1))
		b.maps[id] = &mapTracker{
			items:   items,
			states:  make([]string, len(items)),
			results: make([]any, len(items)),
			handles: make([]string, len(items)),
		}
		res, _ := json.Marshal(map[string]any{"value": id})
		return 0, string(res)
	case "__map_start":
		id, _ := args["map_id"].(string)
		idx := int(toFloat(args["index"]))
		if mt := b.maps[id]; mt != nil && idx >= 0 && idx < len(mt.states) {
			mt.states[idx] = "pending"
			b.activeM = id
			b.activeI = idx
		}
		return 0, `{"value":true}`
	case "__map_finish":
		id, _ := args["map_id"].(string)
		idx := int(toFloat(args["index"]))
		if mt := b.maps[id]; mt != nil && idx >= 0 && idx < len(mt.states) {
			mt.states[idx] = "finished"
			mt.results[idx] = args["value"]
			if b.activeM == id && b.activeI == idx {
				b.activeM = ""
				b.activeI = -1
			}
		}
		return 0, `{"value":true}`
	default:
		return 0, `{"value":null}`
	}
}

func (b *evalBridge) buildMapRecords() []MapCutRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	for id := range b.maps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]MapCutRecord, 0, len(ids))
	for _, id := range ids {
		mt := b.maps[id]
		rec := MapCutRecord{
			Items: append([]any(nil), mt.items...),
		}
		for i, st := range mt.states {
			switch st {
			case "finished":
				rec.Finished = append(rec.Finished, MapItemResult{Index: i, Value: mt.results[i]})
			case "started":
				rec.Started = append(rec.Started, MapStartedHandle{Index: i, Handle: mt.handles[i]})
			case "pending":
				rec.Pending = append(rec.Pending, i)
				rec.Remaining = append(rec.Remaining, mt.items[i])
			default:
				rec.NotStarted = append(rec.NotStarted, i)
				rec.Remaining = append(rec.Remaining, mt.items[i])
			}
		}
		out = append(out, rec)
	}
	return out
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return 0
	}
}

func (e *nativeEngine) Name() string { return e.name }

func (e *nativeEngine) BindTool(name string, spec ToolSpec, handler ToolHandler) error {
	return e.tools.Bind(name, spec, handler)
}

func (e *nativeEngine) OnAsyncSettle(cb AsyncSettleFunc) {
	e.tools.OnAsyncSettle(cb)
}

func (e *nativeEngine) toolNames() []string {
	e.tools.mu.RLock()
	defer e.tools.mu.RUnlock()
	out := make([]string, 0, len(e.tools.tools))
	for k := range e.tools.tools {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (e *nativeEngine) Eval(ctx context.Context, req EvalRequest) (EvalResult, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return EvalResult{}, ErrEngineClosed
	}
	wire := e.wire
	e.mu.Unlock()

	if e.slots != nil {
		waitCtx := ctx
		var cancel context.CancelFunc
		if req.RemainingWait > 0 {
			waitCtx, cancel = context.WithTimeout(ctx, req.RemainingWait)
		} else if req.Limits.WaitTimeout > 0 {
			waitCtx, cancel = context.WithTimeout(ctx, req.Limits.WaitTimeout)
		}
		_, rel, err := e.slots.Acquire(waitCtx, req.Role)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			return EvalResult{
				Status: EvalFailed,
				Error:  &EvalError{Kind: "NoKernelSlot", Message: "no kernel free; nothing ran"},
			}, nil
		}
		defer rel()
	}

	cs := e.tools.NewCallSession(req)
	e.bridge.mu.Lock()
	e.bridge.session = cs
	e.bridge.ctx = ctx
	e.bridge.maps = make(map[string]*mapTracker)
	e.bridge.activeM = ""
	e.bridge.activeI = -1
	e.bridge.mu.Unlock()

	limits := req.Limits
	if limits.ComputeTimeout <= 0 {
		limits.ComputeTimeout = DefaultLimits().ComputeTimeout
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = DefaultLimits().MaxOutputBytes
	}

	payload := map[string]any{
		"cell_id":          req.CellID,
		"session_id":       req.SessionID,
		"owner_id":         req.OwnerID,
		"role":             string(req.Role),
		"code":             req.Code,
		"compute_ms":       limits.ComputeTimeout.Milliseconds(),
		"wait_ms":          cs.RemainingWait().Milliseconds(),
		"max_output_bytes": limits.MaxOutputBytes,
		"max_heap_bytes":   limits.MaxHeapBytes,
		"bindings":         req.Bindings,
		"tool_names":       e.toolNames(),
	}
	rawReq, err := json.Marshal(payload)
	if err != nil {
		return EvalResult{}, err
	}

	// Arm wait timer and context cancellation watcher so external Cut/Kill fires cleanly.
	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		var waitTimer <-chan time.Time
		if cs.hasDeadline {
			t := time.NewTimer(cs.RemainingWait())
			defer t.Stop()
			waitTimer = t.C
		}
		select {
		case <-doneCh:
		case <-waitTimer:
			_, _ = wire.CutJSON(string(CutWaiting))
		case <-ctx.Done():
			wire.Kill()
		}
	}()

	totalStart := time.Now()
	rawResp, err := wire.EvalJSON(string(rawReq))
	totalDur := time.Since(totalStart)
	if err != nil {
		return EvalResult{}, err
	}

	var parsed struct {
		Status       EvalStatus     `json:"status"`
		Value        any            `json:"value"`
		Logs         []string       `json:"logs"`
		Error        *EvalError     `json:"error"`
		Bindings     map[string]any `json:"bindings"`
		LostBindings []string       `json:"lost_bindings"`
		CompileNS    float64        `json:"compile_ns"`
		ComputeNS    float64        `json:"compute_ns"`
		SnapBytes    int            `json:"snapshot_bytes"`
		Cut          *struct {
			Reason       CutReason `json:"reason"`
			Snapshot     string    `json:"snapshot"`
			LostBindings []string  `json:"lost_bindings"`
		} `json:"cut"`
	}
	if err := json.Unmarshal([]byte(rawResp), &parsed); err != nil {
		return EvalResult{}, fmt.Errorf("decode %s eval response: %w (%s)", e.name, err, rawResp)
	}

	for _, rec := range e.bridge.buildMapRecords() {
		cs.RecordMapCut(rec)
	}

	e.mu.Lock()
	if parsed.Bindings != nil {
		e.bindings = parsed.Bindings
	}
	e.lastLost = parsed.LostBindings
	var engBytes []byte
	if parsed.Cut != nil && parsed.Cut.Snapshot != "" {
		engBytes, _ = base64.StdEncoding.DecodeString(parsed.Cut.Snapshot)
	}
	if parsed.Status == EvalCut || parsed.Status == EvalKilled {
		reason := CutWaiting
		if parsed.Cut != nil && parsed.Cut.Reason != "" {
			reason = parsed.Cut.Reason
		}
		if parsed.Status == EvalKilled {
			reason = CutKilled
		}
		ci := cs.BuildCutInfo(reason, parsed.LostBindings)
		ci.Snapshot = engBytes
		e.lastCut = &ci
		e.mu.Unlock()
		return EvalResult{
			Status:       parsed.Status,
			Logs:         parsed.Logs,
			Error:        parsed.Error,
			Cut:          &ci,
			LostBindings: parsed.LostBindings,
			Notice:       FormatLostNotice(parsed.LostBindings),
			Metrics: EvalMetrics{
				CompileDuration: time.Duration(parsed.CompileNS),
				ComputeDuration: time.Duration(parsed.ComputeNS),
				TotalDuration:   totalDur,
				SnapshotBytes:   max(parsed.SnapBytes, len(engBytes)),
			},
		}, nil
	}
	e.mu.Unlock()

	return EvalResult{
		Status:       parsed.Status,
		Value:        parsed.Value,
		Logs:         parsed.Logs,
		Error:        parsed.Error,
		LostBindings: parsed.LostBindings,
		Notice:       FormatLostNotice(parsed.LostBindings),
		Metrics: EvalMetrics{
			CompileDuration: time.Duration(parsed.CompileNS),
			ComputeDuration: time.Duration(parsed.ComputeNS),
			TotalDuration:   totalDur,
			SnapshotBytes:   parsed.SnapBytes,
		},
	}, nil
}

func (e *nativeEngine) Snapshot(ctx context.Context) (SnapshotBlob, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return SnapshotBlob{}, ErrEngineClosed
	}
	wire := e.wire
	cut := e.lastCut
	lost := append([]string(nil), e.lastLost...)
	e.mu.Unlock()

	raw, err := wire.SnapshotJSON()
	if err != nil {
		return SnapshotBlob{}, err
	}
	var parsed struct {
		Format   int            `json:"format"`
		Engine   string         `json:"engine"`
		BuildID  string         `json:"build_id"`
		Bindings map[string]any `json:"bindings"`
		BytesB64 string         `json:"engine_bytes_b64"`
		Lost     []string       `json:"lost_bindings"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return SnapshotBlob{}, err
	}
	var engBytes []byte
	if parsed.BytesB64 != "" {
		engBytes, _ = base64.StdEncoding.DecodeString(parsed.BytesB64)
	}
	if len(parsed.Lost) > 0 {
		lost = parsed.Lost
	}
	return SnapshotBlob{
		Format:       parsed.Format,
		Engine:       e.name,
		BuildID:      parsed.BuildID,
		Bindings:     parsed.Bindings,
		EngineBytes:  engBytes,
		Cut:          cut,
		LostBindings: lost,
		Notice:       FormatLostNotice(lost),
	}, nil
}

func (e *nativeEngine) Restore(ctx context.Context, snap SnapshotBlob) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEngineClosed
	}
	wire := e.wire
	e.lastCut = snap.Cut
	e.lastLost = append([]string(nil), snap.LostBindings...)
	if snap.Bindings != nil {
		e.bindings = snap.Bindings
	}
	e.mu.Unlock()

	payload := map[string]any{
		"bindings":         snap.Bindings,
		"cut":              snap.Cut,
		"engine_bytes_b64": base64.StdEncoding.EncodeToString(snap.EngineBytes),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	respRaw, err := wire.RestoreJSON(string(raw))
	if err != nil {
		return err
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(respRaw), &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s restore failed: %s", e.name, resp.Error)
	}
	return nil
}

func (e *nativeEngine) Cut(ctx context.Context, reason CutReason) (CutInfo, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return CutInfo{}, ErrEngineClosed
	}
	wire := e.wire
	e.mu.Unlock()

	raw, err := wire.CutJSON(string(reason))
	if err != nil {
		return CutInfo{}, err
	}
	var resp struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Snapshot string `json:"snapshot"`
	}
	_ = json.Unmarshal([]byte(raw), &resp)
	e.bridge.mu.Lock()
	cs := e.bridge.session
	e.bridge.mu.Unlock()
	if cs != nil {
		ci := cs.BuildCutInfo(reason, e.lastLost)
		if resp.Snapshot != "" {
			ci.Snapshot, _ = base64.StdEncoding.DecodeString(resp.Snapshot)
		}
		return ci, nil
	}
	return CutInfo{Reason: reason}, nil
}

func (e *nativeEngine) Kill(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEngineClosed
	}
	wire := e.wire
	e.mu.Unlock()
	wire.Kill()
	return nil
}

func (e *nativeEngine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	wire := e.wire
	id := e.bridgeID
	e.mu.Unlock()

	unregisterBridge(id)
	if wire != nil {
		wire.Close()
	}
	return nil
}

var (
	bridgeMu     sync.RWMutex
	bridgeMap    = make(map[uintptr]*evalBridge)
	nextBridgeID atomic.Uintptr
)

func registerBridge(b *evalBridge) uintptr {
	id := nextBridgeID.Add(1)
	bridgeMu.Lock()
	bridgeMap[id] = b
	bridgeMu.Unlock()
	return id
}

func unregisterBridge(id uintptr) {
	bridgeMu.Lock()
	delete(bridgeMap, id)
	bridgeMu.Unlock()
}

func lookupBridge(id uintptr) *evalBridge {
	bridgeMu.RLock()
	b := bridgeMap[id]
	bridgeMu.RUnlock()
	return b
}

// Suppress unused warning if strings not referenced directly in native.go.
var _ = strings.TrimSpace
