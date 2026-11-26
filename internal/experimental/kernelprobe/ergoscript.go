package kernelprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/sketch/syntax"
)

// ErgoScriptDesignNote documents the architecture for a full interpreted-Ergo
// cell engine beyond this Unit 0 spike.
const ErgoScriptDesignNote = `ergoscript Design Note (Unit 0 Spike):
1. Front-end reuse: Parse cell sources via internal/sketch/syntax.Parse (compiler/syntax
   linked into ergo) and type/effect-check cells against a CellTools capability row
   using internal/sketch/check (or compiler/check once self-hosted).
2. Static guarantees over JS/Luau:
   - Role tool denials (e.g., worker calling tools.subagent) fail at check time before
     running a single step.
   - Non-serializable bindings (functions, channels, resources) are refused statically
     at persistent session assignment instead of leaking or failing at cut/restore.
   - Effect rows statically distinguish pure reads, synchronous tools, and handle-yielding
     starts (tools.start.*), making Unfinished-stop (W2b) inspectable before execution.
3. Execution & Persistence:
   - Compiles checked AST into a pure-Go stack/register interpreter (extending
     internal/sketch/eval's frame & scheduler model) with zero cgo, zero C++/.so
     distribution, and deterministic step/byte metering.
   - Persistence across cells stores typed JSON/CBOR session values in SnapshotBlob.Bindings
     plus cut.calls / cut.maps journals (no raw C stack frames to serialize).`

var (
	errReturnSignal   = errors.New("return")
	errBreakSignal    = errors.New("break")
	errContinueSignal = errors.New("continue")
	errCutStarted     = errors.New("cut_started")
	errCutRequested   = errors.New("cut_requested")
	errKilledSignal   = errors.New("killed")
)

type ergoReturnVal struct {
	val any
}

func (e ergoReturnVal) Error() string { return "return" }

type ergoEvalErr struct {
	kind string
	msg  string
}

func (e ergoEvalErr) Error() string { return e.kind + ": " + e.msg }

// ErgoScriptEngine is the smallest-possible Unit 0 spike for interpreted Ergo
// using Ergo's real syntax.Parse front-end and an AST evaluator over JSON values.
type ErgoScriptEngine struct {
	tools    *ToolRegistry
	slots    *SlotPool
	mu       sync.Mutex
	closed   bool
	bindings map[string]any
	lastCut  *CutInfo
	lastLost []string
	active   *ergoExec
}

type ergoExec struct {
	mu      sync.Mutex
	cutReq  bool
	cutWhy  CutReason
	killReq bool
}

// NewErgoScriptEngine constructs the ergoscript design spike KernelEngine.
func NewErgoScriptEngine(tools *ToolRegistry, slots *SlotPool) *ErgoScriptEngine {
	if tools == nil {
		tools = NewToolRegistry(nil)
	}
	if slots == nil {
		slots = NewSlotPool(MaxRoleLiveSlots)
	}
	return &ErgoScriptEngine{
		tools:    tools,
		slots:    slots,
		bindings: make(map[string]any),
	}
}

func (e *ErgoScriptEngine) Name() string { return "ergoscript" }

func (e *ErgoScriptEngine) BindTool(name string, spec ToolSpec, handler ToolHandler) error {
	return e.tools.Bind(name, spec, handler)
}

func (e *ErgoScriptEngine) OnAsyncSettle(cb AsyncSettleFunc) {
	e.tools.OnAsyncSettle(cb)
}

func (e *ErgoScriptEngine) Snapshot(ctx context.Context) (SnapshotBlob, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return SnapshotBlob{}, ErrEngineClosed
	}
	cp := cloneAnyMap(e.bindings)
	return SnapshotBlob{
		Format:       2,
		Engine:       "ergoscript",
		BuildID:      "ergoscript-spike-v0",
		Bindings:     cp,
		Cut:          e.lastCut,
		LostBindings: append([]string(nil), e.lastLost...),
		Notice:       FormatLostNotice(e.lastLost),
	}, nil
}

func (e *ErgoScriptEngine) Restore(ctx context.Context, snap SnapshotBlob) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrEngineClosed
	}
	if snap.Bindings != nil {
		e.bindings = cloneAnyMap(snap.Bindings)
	} else {
		e.bindings = make(map[string]any)
	}
	e.lastCut = snap.Cut
	e.lastLost = append([]string(nil), snap.LostBindings...)
	return nil
}

func (e *ErgoScriptEngine) Cut(ctx context.Context, reason CutReason) (CutInfo, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return CutInfo{}, ErrEngineClosed
	}
	act := e.active
	e.mu.Unlock()
	if act != nil {
		act.mu.Lock()
		act.cutReq = true
		act.cutWhy = reason
		act.mu.Unlock()
	}
	return CutInfo{Reason: reason}, nil
}

func (e *ErgoScriptEngine) Kill(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ErrEngineClosed
	}
	act := e.active
	e.mu.Unlock()
	if act != nil {
		act.mu.Lock()
		act.killReq = true
		act.mu.Unlock()
	}
	return nil
}

func (e *ErgoScriptEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return nil
}

func (e *ErgoScriptEngine) Eval(ctx context.Context, req EvalRequest) (EvalResult, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return EvalResult{}, ErrEngineClosed
	}
	baseBindings := cloneAnyMap(e.bindings)
	lastCut := e.lastCut
	e.mu.Unlock()

	if req.Bindings != nil {
		maps.Copy(baseBindings, req.Bindings)
	}

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

	tCompile := time.Now()
	stmts, compileErr := parseAndCheckErgoCell(req.Code, req.Role, e.tools)
	compileDur := time.Since(tCompile)
	if compileErr != nil {
		return EvalResult{
			Status:  EvalFailed,
			Error:   compileErr,
			Metrics: EvalMetrics{CompileDuration: compileDur, TotalDuration: compileDur},
		}, nil
	}

	cs := e.tools.NewCallSession(req)
	limits := req.Limits
	if limits.ComputeTimeout <= 0 {
		limits.ComputeTimeout = DefaultLimits().ComputeTimeout
	}

	act := &ergoExec{cutWhy: CutWaiting}
	e.mu.Lock()
	e.active = act
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.active == act {
			e.active = nil
		}
		e.mu.Unlock()
	}()

	env := &ergoEnv{
		parent: nil,
		vars:   cloneAnyMap(baseBindings),
		consts: make(map[string]bool),
	}
	var cutObj any
	if lastCut != nil {
		b, _ := json.Marshal(lastCut)
		_ = json.Unmarshal(b, &cutObj)
	}
	env.vars["kernel"] = map[string]any{
		"cut":      cutObj,
		"bindings": sortedKeys(baseBindings),
	}
	env.vars["session"] = baseBindings

	vm := &ergoVM{
		ctx:       ctx,
		cs:        cs,
		exec:      act,
		deadline:  time.Now().Add(limits.ComputeTimeout),
		maxSteps:  5_000_000,
		persisted: baseBindings,
	}

	tCompute := time.Now()
	val, runErr := vm.execStmts(stmts, env)
	computeDur := time.Since(tCompute)

	// Persist any updated serializable top-level / session variables.
	nextBindings := extractErgoBindings(env, baseBindings)

	e.mu.Lock()
	e.bindings = nextBindings
	e.mu.Unlock()

	if runErr != nil {
		var ret ergoReturnVal
		if errors.As(runErr, &ret) {
			return EvalResult{
				Status: EvalCompleted,
				Value:  ret.val,
				Logs:   vm.logs,
				Metrics: EvalMetrics{
					CompileDuration: compileDur,
					ComputeDuration: computeDur,
					TotalDuration:   compileDur + computeDur,
				},
			}, nil
		}
		if errors.Is(runErr, errCutStarted) || errors.Is(runErr, errCutRequested) {
			reason := CutStarted
			if errors.Is(runErr, errCutRequested) {
				act.mu.Lock()
				reason = act.cutWhy
				act.mu.Unlock()
			}
			ci := cs.BuildCutInfo(reason, nil)
			e.mu.Lock()
			e.lastCut = &ci
			e.mu.Unlock()
			return EvalResult{
				Status: EvalCut,
				Logs:   vm.logs,
				Cut:    &ci,
				Metrics: EvalMetrics{
					CompileDuration: compileDur,
					ComputeDuration: computeDur,
					TotalDuration:   compileDur + computeDur,
				},
			}, nil
		}
		if errors.Is(runErr, errKilledSignal) {
			ci := cs.BuildCutInfo(CutKilled, nil)
			return EvalResult{
				Status: EvalKilled,
				Logs:   vm.logs,
				Error:  &EvalError{Kind: "TimeoutExceeded", Message: "compute budget exceeded or killed"},
				Cut:    &ci,
				Metrics: EvalMetrics{
					CompileDuration: compileDur,
					ComputeDuration: computeDur,
					TotalDuration:   compileDur + computeDur,
				},
			}, nil
		}
		var ee ergoEvalErr
		if errors.As(runErr, &ee) {
			return EvalResult{
				Status: EvalFailed,
				Logs:   vm.logs,
				Error:  &EvalError{Kind: ee.kind, Message: ee.msg},
				Metrics: EvalMetrics{
					CompileDuration: compileDur,
					ComputeDuration: computeDur,
					TotalDuration:   compileDur + computeDur,
				},
			}, nil
		}
		return EvalResult{
			Status: EvalFailed,
			Logs:   vm.logs,
			Error:  &EvalError{Kind: "ExecutionFailure", Message: runErr.Error()},
			Metrics: EvalMetrics{
				CompileDuration: compileDur,
				ComputeDuration: computeDur,
				TotalDuration:   compileDur + computeDur,
			},
		}, nil
	}

	return EvalResult{
		Status: EvalCompleted,
		Value:  val,
		Logs:   vm.logs,
		Metrics: EvalMetrics{
			CompileDuration: compileDur,
			ComputeDuration: computeDur,
			TotalDuration:   compileDur + computeDur,
		},
	}, nil
}

func parseAndCheckErgoCell(code string, role Role, tools *ToolRegistry) ([]syntax.Stmt, *EvalError) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, &EvalError{Kind: "ParseError", Message: "empty cell"}
	}
	wrapped := "func cell() {\n" + code + "\n}\n"
	file, err := syntax.Parse(wrapped)
	if err != nil {
		// Also try raw file containing func cell() / top-level decls directly.
		if file2, err2 := syntax.Parse(code); err2 == nil {
			file = file2
			err = nil
		}
	}
	if err != nil {
		return nil, &EvalError{Kind: "ParseError", Message: err.Error()}
	}
	if len(file.Decls) == 0 {
		return nil, &EvalError{Kind: "ParseError", Message: "no declarations found"}
	}
	var body *syntax.BlockStmt
	var preDecls []syntax.Stmt
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *syntax.ImportDecl:
			return nil, &EvalError{Kind: "StaticCapabilityError", Message: "ambient imports are forbidden in ergoscript cells"}
		case *syntax.VarDecl:
			preDecls = append(preDecls, &syntax.DeclStmt{Decl: decl})
		case *syntax.FuncDecl:
			if decl.Name != nil && (decl.Name.Name == "cell" || decl.Name.Name == "main") && decl.Body != nil {
				body = decl.Body
			}
		}
	}
	if body == nil {
		return nil, &EvalError{Kind: "ParseError", Message: "expected cell statements or func cell()"}
	}
	stmts := append(preDecls, body.List...)

	// Static pass: verify no FuncLit enters persistent session state and no denied tool is called.
	var staticErr *EvalError
	for _, st := range stmts {
		syntax.Inspect(st, func(n syntax.Node) bool {
			if staticErr != nil {
				return false
			}
			switch node := n.(type) {
			case *syntax.CallExpr:
				if toolName, ok := extractToolCallName(node.Fun); ok && !strings.HasPrefix(toolName, "handles.") && toolName != "map" && toolName != "Map" && toolName != "all" {
					base := strings.TrimPrefix(toolName, "start.")
					tools.mu.RLock()
					reg, bound := tools.tools[base]
					tools.mu.RUnlock()
					if bound && !reg.spec.Allows(role) {
						staticErr = &EvalError{
							Kind:    "StaticRoleDenied",
							Message: fmt.Sprintf("static check: role %s cannot call tools.%s", role, base),
						}
						return false
					}
				}
			case *syntax.AssignStmt:
				for _, lhs := range node.Lhs {
					if isSessionAssign(lhs) {
						for _, rhs := range node.Rhs {
							if _, isFn := rhs.(*syntax.FuncLit); isFn {
								staticErr = &EvalError{
									Kind:    "NonSerializableBinding",
									Message: "static check: cannot store function/closure in persistent session state",
								}
								return false
							}
						}
					}
				}
			}
			return true
		})
	}
	if staticErr != nil {
		return nil, staticErr
	}
	return stmts, nil
}

func extractToolCallName(fun syntax.Expr) (string, bool) {
	sel, ok := fun.(*syntax.SelectorExpr)
	if !ok {
		return "", false
	}
	if id, ok := sel.X.(*syntax.Ident); ok && id.Name == "tools" {
		return sel.Sel.Name, true
	}
	if sub, ok := sel.X.(*syntax.SelectorExpr); ok {
		if id, ok := sub.X.(*syntax.Ident); ok && id.Name == "tools" {
			return sub.Sel.Name + "." + sel.Sel.Name, true
		}
	}
	return "", false
}

func isSessionAssign(lhs syntax.Expr) bool {
	switch x := lhs.(type) {
	case *syntax.SelectorExpr:
		if id, ok := x.X.(*syntax.Ident); ok && id.Name == "session" {
			return true
		}
	case *syntax.IndexExpr:
		if id, ok := x.X.(*syntax.Ident); ok && id.Name == "session" {
			return true
		}
	}
	return false
}

type ergoEnv struct {
	parent *ergoEnv
	vars   map[string]any
	consts map[string]bool
}

func (e *ergoEnv) get(name string) (any, bool) {
	for cur := e; cur != nil; cur = cur.parent {
		if v, ok := cur.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (e *ergoEnv) setExisting(name string, v any) bool {
	for cur := e; cur != nil; cur = cur.parent {
		if _, ok := cur.vars[name]; ok {
			cur.vars[name] = v
			return true
		}
	}
	return false
}

type ergoVM struct {
	ctx       context.Context
	cs        *CallSession
	exec      *ergoExec
	deadline  time.Time
	steps     int
	maxSteps  int
	logs      []string
	persisted map[string]any
}

func (v *ergoVM) checkBudget() error {
	v.steps++
	if v.steps > v.maxSteps || time.Now().After(v.deadline) {
		return errKilledSignal
	}
	select {
	case <-v.ctx.Done():
		return errKilledSignal
	default:
	}
	v.exec.mu.Lock()
	kill := v.exec.killReq
	cut := v.exec.cutReq
	v.exec.mu.Unlock()
	if kill {
		return errKilledSignal
	}
	if cut {
		return errCutRequested
	}
	if v.cs.hasDeadline && v.cs.RemainingWait() == 0 {
		return errCutRequested
	}
	return nil
}

func (v *ergoVM) execStmts(stmts []syntax.Stmt, env *ergoEnv) (any, error) {
	var last any
	for _, st := range stmts {
		if err := v.checkBudget(); err != nil {
			return nil, err
		}
		val, err := v.execStmt(st, env)
		if err != nil {
			return nil, err
		}
		last = val
	}
	return last, nil
}

func (v *ergoVM) execStmt(st syntax.Stmt, env *ergoEnv) (any, error) {
	switch s := st.(type) {
	case *syntax.DeclStmt:
		for _, spec := range s.Decl.Specs {
			for i, ident := range spec.Names {
				var val any
				if i < len(spec.Values) {
					var err error
					val, err = v.evalExpr(spec.Values[i], env)
					if err != nil {
						return nil, err
					}
				}
				if _, isFn := val.(*syntax.FuncLit); isFn {
					// Top-level closures cannot enter persistent state.
					return nil, ergoEvalErr{kind: "NonSerializableBinding", msg: "cannot bind closure to persistent variable " + ident.Name}
				}
				env.vars[ident.Name] = val
				if s.Decl.Mut == syntax.NoPos {
					env.consts[ident.Name] = true
				}
			}
		}
		return nil, nil
	case *syntax.ExprStmt:
		return v.evalExpr(s.X, env)
	case *syntax.AssignStmt:
		for i, lhs := range s.Lhs {
			var rhsVal any
			if i < len(s.Rhs) {
				var err error
				rhsVal, err = v.evalExpr(s.Rhs[i], env)
				if err != nil {
					return nil, err
				}
			}
			switch target := lhs.(type) {
			case *syntax.Ident:
				if s.Tok == syntax.ADD_ASSIGN {
					prev, _ := env.get(target.Name)
					rhsVal = addValues(prev, rhsVal)
				}
				if !env.setExisting(target.Name, rhsVal) {
					env.vars[target.Name] = rhsVal
				}
			case *syntax.IndexExpr:
				container, err := v.evalExpr(target.X, env)
				if err != nil {
					return nil, err
				}
				key, err := v.evalExpr(target.Indices[0], env)
				if err != nil {
					return nil, err
				}
				switch m := container.(type) {
				case map[string]any:
					m[fmt.Sprint(key)] = rhsVal
				case []any:
					idx := int(toFloat(key))
					if idx >= 0 && idx < len(m) {
						m[idx] = rhsVal
					}
				}
			case *syntax.SelectorExpr:
				container, err := v.evalExpr(target.X, env)
				if err != nil {
					return nil, err
				}
				if m, ok := container.(map[string]any); ok {
					m[target.Sel.Name] = rhsVal
				}
			}
		}
		return nil, nil
	case *syntax.ReturnStmt:
		if len(s.Results) == 0 {
			return nil, ergoReturnVal{val: nil}
		}
		val, err := v.evalExpr(s.Results[0], env)
		if err != nil {
			return nil, err
		}
		return nil, ergoReturnVal{val: val}
	case *syntax.BranchStmt:
		if s.Tok == syntax.BREAK {
			return nil, errBreakSignal
		}
		return nil, errContinueSignal
	case *syntax.BlockStmt:
		child := &ergoEnv{parent: env, vars: map[string]any{}, consts: map[string]bool{}}
		return v.execStmts(s.List, child)
	case *syntax.IfStmt:
		cond, err := v.evalExpr(s.Cond, env)
		if err != nil {
			return nil, err
		}
		if truthy(cond) {
			return v.execStmt(s.Body, env)
		} else if s.Else != nil {
			return v.execStmt(s.Else, env)
		}
		return nil, nil
	case *syntax.ForStmt:
		for {
			if err := v.checkBudget(); err != nil {
				return nil, err
			}
			if s.Cond != nil {
				c, err := v.evalExpr(s.Cond, env)
				if err != nil {
					return nil, err
				}
				if !truthy(c) {
					break
				}
			}
			child := &ergoEnv{parent: env, vars: map[string]any{}, consts: map[string]bool{}}
			_, err := v.execStmts(s.Body.List, child)
			if err == errBreakSignal {
				break
			}
			if err == errContinueSignal {
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		return nil, nil
	case *syntax.RangeStmt:
		seqVal, err := v.evalExpr(s.X, env)
		if err != nil {
			return nil, err
		}
		items, _ := seqVal.([]any)
		for i, item := range items {
			if err := v.checkBudget(); err != nil {
				return nil, err
			}
			child := &ergoEnv{parent: env, vars: map[string]any{}, consts: map[string]bool{}}
			if s.Key != nil {
				if id, ok := s.Key.(*syntax.Ident); ok && id.Name != "_" {
					if s.Value == nil {
						child.vars[id.Name] = item
					} else {
						child.vars[id.Name] = i
					}
				}
			}
			if s.Value != nil {
				if id, ok := s.Value.(*syntax.Ident); ok && id.Name != "_" {
					child.vars[id.Name] = item
				}
			}
			_, err := v.execStmts(s.Body.List, child)
			if err == errBreakSignal {
				break
			}
			if err == errContinueSignal {
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		return nil, nil
	case *syntax.SwitchStmt:
		tag, err := v.evalExpr(s.Tag, env)
		if err != nil {
			return nil, err
		}
		for _, cl := range s.Clauses {
			if len(cl.List) == 0 {
				return v.execStmts(cl.Body, env)
			}
			for _, pat := range cl.List {
				pv, err := v.evalExpr(pat, env)
				if err != nil {
					return nil, err
				}
				if fmt.Sprint(tag) == fmt.Sprint(pv) {
					return v.execStmts(cl.Body, env)
				}
			}
		}
		return nil, nil
	}
	return nil, nil
}

func (v *ergoVM) evalExpr(ex syntax.Expr, env *ergoEnv) (any, error) {
	if err := v.checkBudget(); err != nil {
		return nil, err
	}
	switch e := ex.(type) {
	case *syntax.Ident:
		switch e.Name {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "nil":
			return nil, nil
		}
		if val, ok := env.get(e.Name); ok {
			return val, nil
		}
		return nil, ergoEvalErr{kind: "ReferenceError", msg: "undefined variable " + e.Name}
	case *syntax.BasicLit:
		switch e.Kind {
		case syntax.INT:
			n, err := strconv.ParseInt(strings.ReplaceAll(e.Value, "_", ""), 0, 64)
			if err != nil {
				return nil, err
			}
			return int(n), nil
		case syntax.FLOAT:
			f, err := strconv.ParseFloat(strings.ReplaceAll(e.Value, "_", ""), 64)
			if err != nil {
				return nil, err
			}
			return f, nil
		case syntax.STRING:
			return evalStringLiteral(e.Value, v, env)
		}
		return e.Value, nil
	case *syntax.ParenExpr:
		return v.evalExpr(e.X, env)
	case *syntax.TryExpr:
		return v.evalExpr(e.X, env)
	case *syntax.InterpolatedString:
		var out strings.Builder
		for i, lit := range e.Texts {
			out.WriteString(unquoteInterpSegment(lit.Value, i == 0, i == len(e.Texts)-1))
			if i < len(e.Exprs) {
				val, err := v.evalExpr(e.Exprs[i], env)
				if err != nil {
					return nil, err
				}
				out.WriteString(fmt.Sprint(val))
			}
		}
		return out.String(), nil
	case *syntax.FuncLit:
		return e, nil
	case *syntax.UnaryExpr:
		val, err := v.evalExpr(e.X, env)
		if err != nil {
			return nil, err
		}
		switch e.Op {
		case syntax.NOT:
			return !truthy(val), nil
		case syntax.SUB:
			return -toFloat(val), nil
		}
		return val, nil
	case *syntax.BinaryExpr:
		if e.Op == syntax.LAND {
			l, err := v.evalExpr(e.X, env)
			if err != nil || !truthy(l) {
				return false, err
			}
			r, err := v.evalExpr(e.Y, env)
			return truthy(r), err
		}
		if e.Op == syntax.LOR {
			l, err := v.evalExpr(e.X, env)
			if err != nil || truthy(l) {
				return true, err
			}
			r, err := v.evalExpr(e.Y, env)
			return truthy(r), err
		}
		l, err := v.evalExpr(e.X, env)
		if err != nil {
			return nil, err
		}
		r, err := v.evalExpr(e.Y, env)
		if err != nil {
			return nil, err
		}
		return evalBinary(e.Op, l, r), nil
	case *syntax.CompositeLit:
		if len(e.Elts) == 0 {
			if _, isArr := e.Type.(*syntax.ArrayType); isArr {
				return []any{}, nil
			}
			return map[string]any{}, nil
		}
		if _, isKV := e.Elts[0].(*syntax.KeyValueExpr); isKV {
			m := make(map[string]any, len(e.Elts))
			for _, elt := range e.Elts {
				kv, ok := elt.(*syntax.KeyValueExpr)
				if !ok {
					continue
				}
				var k string
				if id, ok := kv.Key.(*syntax.Ident); ok {
					k = id.Name
				} else {
					kvv, err := v.evalExpr(kv.Key, env)
					if err != nil {
						return nil, err
					}
					k = fmt.Sprint(kvv)
				}
				val, err := v.evalExpr(kv.Value, env)
				if err != nil {
					return nil, err
				}
				m[k] = val
			}
			return m, nil
		}
		arr := make([]any, 0, len(e.Elts))
		for _, elt := range e.Elts {
			val, err := v.evalExpr(elt, env)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		return arr, nil
	case *syntax.SelectorExpr:
		obj, err := v.evalExpr(e.X, env)
		if err != nil {
			return nil, err
		}
		if m, ok := obj.(map[string]any); ok {
			return m[e.Sel.Name], nil
		}
		return nil, nil
	case *syntax.IndexExpr:
		obj, err := v.evalExpr(e.X, env)
		if err != nil {
			return nil, err
		}
		idxVal, err := v.evalExpr(e.Indices[0], env)
		if err != nil {
			return nil, err
		}
		switch col := obj.(type) {
		case []any:
			i := int(toFloat(idxVal))
			if i >= 0 && i < len(col) {
				return col[i], nil
			}
		case map[string]any:
			return col[fmt.Sprint(idxVal)], nil
		}
		return nil, nil
	case *syntax.CallExpr:
		return v.evalCall(e, env)
	}
	return nil, nil
}

func (v *ergoVM) evalCall(c *syntax.CallExpr, env *ergoEnv) (any, error) {
	if id, ok := c.Fun.(*syntax.Ident); ok {
		switch id.Name {
		case "len":
			if len(c.Args) != 1 {
				return 0, nil
			}
			val, err := v.evalExpr(c.Args[0], env)
			if err != nil {
				return nil, err
			}
			switch x := val.(type) {
			case []any:
				return len(x), nil
			case map[string]any:
				return len(x), nil
			case string:
				return len(x), nil
			}
			return 0, nil
		case "append":
			if len(c.Args) == 0 {
				return []any{}, nil
			}
			base, err := v.evalExpr(c.Args[0], env)
			if err != nil {
				return nil, err
			}
			slice, _ := base.([]any)
			out := append([]any(nil), slice...)
			for _, a := range c.Args[1:] {
				av, err := v.evalExpr(a, env)
				if err != nil {
					return nil, err
				}
				out = append(out, av)
			}
			return out, nil
		case "log":
			var parts []string
			for _, a := range c.Args {
				av, err := v.evalExpr(a, env)
				if err != nil {
					return nil, err
				}
				parts = append(parts, fmt.Sprint(av))
			}
			v.logs = append(v.logs, strings.Join(parts, " "))
			return nil, nil
		}
	}

	if toolName, ok := extractToolCallName(c.Fun); ok {
		if (toolName == "map" || toolName == "Map") && len(c.Args) >= 2 {
			itemsRaw, err := v.evalExpr(c.Args[0], env)
			if err != nil {
				return nil, err
			}
			items, _ := itemsRaw.([]any)
			fnLit, isFn := c.Args[1].(*syntax.FuncLit)
			if !isFn {
				return nil, ergoEvalErr{kind: "TypeError", msg: "tools.map requires func literal"}
			}
			rec := MapCutRecord{Items: append([]any(nil), items...)}
			out := make([]any, 0, len(items))
			for idx, item := range items {
				child := &ergoEnv{parent: env, vars: map[string]any{}, consts: map[string]bool{}}
				if fnLit.Type != nil && fnLit.Type.Params != nil && len(fnLit.Type.Params.List) > 0 && len(fnLit.Type.Params.List[0].Names) > 0 {
					child.vars[fnLit.Type.Params.List[0].Names[0].Name] = item
				}
				res, err := v.execStmts(fnLit.Body.List, child)
				if err != nil {
					var ret ergoReturnVal
					if errors.As(err, &ret) {
						rec.Finished = append(rec.Finished, MapItemResult{Index: idx, Value: ret.val})
						out = append(out, ret.val)
						continue
					}
					rec.Pending = append(rec.Pending, idx)
					rec.Remaining = append(rec.Remaining, item)
					for remIdx := idx + 1; remIdx < len(items); remIdx++ {
						rec.NotStarted = append(rec.NotStarted, remIdx)
						rec.Remaining = append(rec.Remaining, items[remIdx])
					}
					v.cs.RecordMapCut(rec)
					return nil, err
				}
				rec.Finished = append(rec.Finished, MapItemResult{Index: idx, Value: res})
				out = append(out, res)
			}
			return out, nil
		}

		argsMap := map[string]any{}
		if len(c.Args) == 1 {
			val, err := v.evalExpr(c.Args[0], env)
			if err != nil {
				return nil, err
			}
			if m, ok := val.(map[string]any); ok {
				argsMap = m
			} else {
				argsMap["0"] = val
			}
		} else if len(c.Args) > 1 {
			for i, a := range c.Args {
				val, err := v.evalExpr(a, env)
				if err != nil {
					return nil, err
				}
				argsMap[fmt.Sprintf("%d", i)] = val
			}
		}

		startAsync := false
		actualName := toolName
		if strings.HasPrefix(toolName, "start.") {
			startAsync = true
			actualName = strings.TrimPrefix(toolName, "start.")
		}
		res, cutStarted, err := v.cs.Invoke(v.ctx, actualName, argsMap, startAsync)
		if err != nil {
			return nil, err
		}
		if res.ErrKind != "" || res.ErrMessage != "" {
			return nil, ergoEvalErr{kind: res.ErrKind, msg: res.ErrMessage}
		}
		if cutStarted {
			return nil, errCutStarted
		}
		return normalizeErgoVal(res.Value), nil
	}

	return nil, nil
}

func unquoteInterpSegment(raw string, first, last bool) string {
	start := 1
	end := len(raw) - 2
	if last {
		end = len(raw) - 1
	}
	if start > end || start < 0 || end > len(raw) {
		return ""
	}
	core := raw[start:end]
	if u, err := strconv.Unquote(`"` + core + `"`); err == nil {
		return u
	}
	return core
}

func evalStringLiteral(raw string, vm *ergoVM, env *ergoEnv) (string, error) {
	if strings.HasPrefix(raw, "`") && strings.HasSuffix(raw, "`") {
		return raw[1 : len(raw)-1], nil
	}
	if !strings.Contains(raw, `\(`) {
		u, err := strconv.Unquote(raw)
		if err != nil {
			return strings.Trim(raw, `"`), nil
		}
		return u, nil
	}
	// Basic interpolation evaluation for `"prefix \(expr) suffix"`.
	inner := raw[1 : len(raw)-1]
	var out strings.Builder
	for i := 0; i < len(inner); {
		if i+1 < len(inner) && inner[i] == '\\' && inner[i+1] == '(' {
			depth := 1
			j := i + 2
			for j < len(inner) && depth > 0 {
				if inner[j] == '(' {
					depth++
				} else if inner[j] == ')' {
					depth--
				}
				j++
			}
			exprSrc := inner[i+2 : j-1]
			if val, ok := env.get(strings.TrimSpace(exprSrc)); ok {
				out.WriteString(fmt.Sprint(val))
			} else if f, err := syntax.Parse("func cell() {\nreturn " + exprSrc + "\n}\n"); err == nil && len(f.Decls) == 1 {
				if fn, ok := f.Decls[0].(*syntax.FuncDecl); ok && fn.Body != nil && len(fn.Body.List) == 1 {
					if ret, ok := fn.Body.List[0].(*syntax.ReturnStmt); ok && len(ret.Results) == 1 {
						v, evalErr := vm.evalExpr(ret.Results[0], env)
						if evalErr != nil {
							return "", evalErr
						}
						out.WriteString(fmt.Sprint(v))
					}
				}
			}
			i = j
			continue
		}
		if inner[i] == '\\' && i+1 < len(inner) {
			switch inner[i+1] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'r':
				out.WriteByte('\r')
			case '\\':
				out.WriteByte('\\')
			case '"':
				out.WriteByte('"')
			case '\'':
				out.WriteByte('\'')
			case '0':
				out.WriteByte(0)
			default:
				out.WriteByte('\\')
				out.WriteByte(inner[i+1])
			}
			i += 2
			continue
		}
		out.WriteByte(inner[i])
		i++
	}
	return out.String(), nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func addValues(a, b any) any {
	if sa, ok := a.(string); ok {
		return sa + fmt.Sprint(b)
	}
	if ia, ok := a.(int); ok {
		if ib, ok := b.(int); ok {
			return ia + ib
		}
	}
	return toFloat(a) + toFloat(b)
}

func evalBinary(op syntax.Token, l, r any) any {
	switch op {
	case syntax.ADD:
		return addValues(l, r)
	case syntax.SUB:
		if ia, ok := l.(int); ok {
			if ib, ok := r.(int); ok {
				return ia - ib
			}
		}
		return toFloat(l) - toFloat(r)
	case syntax.MUL:
		return toFloat(l) * toFloat(r)
	case syntax.QUO:
		if toFloat(r) == 0 {
			return 0.0
		}
		return toFloat(l) / toFloat(r)
	case syntax.EQL:
		return fmt.Sprint(l) == fmt.Sprint(r)
	case syntax.NEQ:
		return fmt.Sprint(l) != fmt.Sprint(r)
	case syntax.LSS:
		return toFloat(l) < toFloat(r)
	case syntax.LEQ:
		return toFloat(l) <= toFloat(r)
	case syntax.GTR:
		return toFloat(l) > toFloat(r)
	case syntax.GEQ:
		return toFloat(l) >= toFloat(r)
	}
	return nil
}

func extractErgoBindings(env *ergoEnv, prev map[string]any) map[string]any {
	out := cloneAnyMap(prev)
	for k, v := range env.vars {
		if k == "_" || k == "kernel" || k == "session" {
			continue
		}
		if _, isFn := v.(*syntax.FuncLit); isFn {
			continue
		}
		out[k] = v
	}
	if sess, ok := env.vars["session"].(map[string]any); ok {
		for k, v := range sess {
			out[k] = v
		}
	}
	return out
}

func normalizeErgoVal(v any) any {
	switch x := v.(type) {
	case nil, bool, int, float64, string, map[string]any, []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, m := range x {
			out[i] = m
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return v
		}
		var out any
		if err := json.Unmarshal(b, &out); err != nil {
			return v
		}
		return out
	}
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return make(map[string]any)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return maps.Clone(in)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return maps.Clone(in)
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
