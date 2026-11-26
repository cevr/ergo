// Package kernelprobe defines atlas's KernelEngine contract and hosts the
// UNIT 0 (KERNEL-ENGINE-PROBE) evaluation harness, registry invariants, and
// candidate adapters (psychoscript, Luau, and the ergoscript spike).
package kernelprobe

import (
	"context"
	"errors"
	"time"
)

// Role identifies the session role governing tool admission and slot policy.
type Role string

const (
	RoleRoot       Role = "root"
	RoleWorker     Role = "worker"
	RoleAutomation Role = "automation"
)

// EngineLimits bounds cell compute, tool waiting, cut grace, output, and heap.
type EngineLimits struct {
	WaitTimeout      time.Duration `json:"wait_timeout"`
	ComputeTimeout   time.Duration `json:"compute_timeout"`
	CutGrace         time.Duration `json:"cut_grace"`
	MaxOutputBytes   int           `json:"max_output_bytes"`
	MaxSnapshotBytes int           `json:"max_snapshot_bytes"`
	MaxHeapBytes     uint64        `json:"max_heap_bytes"`
}

// DefaultLimits returns atlas's canonical kernel bounds (120s wait, 30s compute,
// 1s cut grace, 64 KiB output cap, 16 MiB snapshot cap).
func DefaultLimits() EngineLimits {
	return EngineLimits{
		WaitTimeout:      120 * time.Second,
		ComputeTimeout:   30 * time.Second,
		CutGrace:         1 * time.Second,
		MaxOutputBytes:   64 * 1024,
		MaxSnapshotBytes: 16 * 1024 * 1024,
		MaxHeapBytes:     64 * 1024 * 1024,
	}
}

// HandleKind names the seven handle classes in atlas's DELIVERS table.
type HandleKind string

const (
	KindCall     HandleKind = "call"
	KindLand     HandleKind = "land"
	KindTimer    HandleKind = "timer"
	KindJob      HandleKind = "job"
	KindSubagent HandleKind = "subagent"
	KindShell    HandleKind = "shell"
	KindQuestion HandleKind = "question"
)

// Deliverer identifies the exclusive component allowed to deliver wakes for a kind.
type Deliverer string

const (
	DelivererRegistry     Deliverer = "registry"
	DelivererWakes        Deliverer = "wakes_model"
	DelivererSubagentHost Deliverer = "subagent_host"
	DelivererShellHost    Deliverer = "shell_host"
	DelivererQuestions    Deliverer = "questions_mod"
)

// DeliversOf maps each HandleKind to its sole deliverer per DELIVERS in kernel.ergo.
func DeliversOf(k HandleKind) (Deliverer, error) {
	switch k {
	case KindCall, KindLand:
		return DelivererRegistry, nil
	case KindTimer, KindJob:
		return DelivererWakes, nil
	case KindSubagent:
		return DelivererSubagentHost, nil
	case KindShell:
		return DelivererShellHost, nil
	case KindQuestion:
		return DelivererQuestions, nil
	default:
		return "", errors.New("unknown handle kind: " + string(k))
	}
}

// HandleDisposition describes the lifecycle state of a WakeHandle.
type HandleDisposition string

const (
	DispOpen      HandleDisposition = "open"
	DispSucceeded HandleDisposition = "succeeded"
	DispFailed    HandleDisposition = "failed"
	DispLost      HandleDisposition = "lost"
	DispDeclined  HandleDisposition = "declined"
	DispCancelled HandleDisposition = "cancelled"
	// DispHanded is terminal: a detached call answered Unfinished and handed
	// delivery to the spawned entity's own deliverer (HandedNeverDoubleWakes).
	DispHanded HandleDisposition = "handed"
)

// WakeHandle represents running work owned by a session that settles asynchronously.
type WakeHandle struct {
	ID          string            `json:"id"`
	Kind        HandleKind        `json:"kind"`
	Owner       string            `json:"owner"`
	SessionID   string            `json:"session_id"`
	Deliverer   Deliverer         `json:"deliverer"`
	Disposition HandleDisposition `json:"disposition"`
	Value       any               `json:"value,omitempty"`
	StartedID   string            `json:"started_id,omitempty"`
	Summary     string            `json:"summary,omitempty"`
}

// AsyncSettleEvent is emitted to OnAsyncSettle listeners when an open handle finishes.
type AsyncSettleEvent struct {
	Handle    WakeHandle `json:"handle"`
	Letter    string     `json:"letter"`
	SettledAt time.Time  `json:"settled_at"`
}

// AsyncSettleFunc receives handle settlement notifications after cell cut or return.
type AsyncSettleFunc func(AsyncSettleEvent)

// ToolMode governs tool execution semantics inside a cell.
type ToolMode string

const (
	ToolSync       ToolMode = "sync"
	ToolAsyncStart ToolMode = "async_start"
	ToolReadOnly   ToolMode = "read_only"
)

// ToolSpec declares a bound host tool and its role policy.
type ToolSpec struct {
	Name         string   `json:"name"`
	Mode         ToolMode `json:"mode"`
	AllowedRoles []Role   `json:"allowed_roles,omitempty"`
}

// Allows reports whether role is permitted to call the tool.
func (s ToolSpec) Allows(r Role) bool {
	if len(s.AllowedRoles) == 0 {
		return true
	}
	for _, allowed := range s.AllowedRoles {
		if allowed == r {
			return true
		}
	}
	return false
}

// ToolCall describes one invocation from inside a cell to a host tool.
type ToolCall struct {
	CallID        string         `json:"call_id"`
	SessionID     string         `json:"session_id"`
	OwnerID       string         `json:"owner_id"`
	Role          Role           `json:"role"`
	ToolName      string         `json:"tool_name"`
	Args          map[string]any `json:"args"`
	RemainingWait time.Duration  `json:"remaining_wait"`
	StartAsync    bool           `json:"start_async"`
}

// ToolOutcome is the host tool's reply to the VM.
type ToolOutcome struct {
	Done       bool        `json:"done"`
	Value      any         `json:"value,omitempty"`
	Unfinished bool        `json:"unfinished,omitempty"`
	Handle     *WakeHandle `json:"handle,omitempty"`
	ErrKind    string      `json:"err_kind,omitempty"`
	ErrMessage string      `json:"err_message,omitempty"`
}

// ToolHandler executes a bound host tool call.
type ToolHandler func(ctx context.Context, call ToolCall) (ToolOutcome, error)

// CutReason identifies why a cell ended early via Cut or Kill.
type CutReason string

const (
	CutStarted CutReason = "started"
	CutWaiting CutReason = "waiting"
	CutAsking  CutReason = "asking"
	CutIdle    CutReason = "idle"
	CutKilled  CutReason = "killed"
)

// CallJournalStatus classifies a recorded call inside cut.calls.
type CallJournalStatus string

const (
	CallFinished    CallJournalStatus = "finished"
	CallPending     CallJournalStatus = "pending"
	CallInterrupted CallJournalStatus = "interrupted"
	CallStarted     CallJournalStatus = "started"
)

// CallJournalEntry records one host tool call made during a cell (rule 8).
type CallJournalEntry struct {
	Index    int               `json:"index"`
	Tool     string            `json:"tool"`
	Input    map[string]any    `json:"input,omitempty"`
	Status   CallJournalStatus `json:"status"`
	HandleID string            `json:"handle_id,omitempty"`
	Output   any               `json:"output,omitempty"`
}

// MapItemResult captures a finished item in cut.maps.
type MapItemResult struct {
	Index int `json:"index"`
	Value any `json:"value,omitempty"`
}

// MapStartedHandle captures an in-flight item handle in cut.maps.
type MapStartedHandle struct {
	Index  int    `json:"index"`
	Handle string `json:"handle"`
}

// MapCutRecord captures partial-cut state for tools.map (W9).
type MapCutRecord struct {
	Items      []any              `json:"items"`
	Finished   []MapItemResult    `json:"finished"`
	Pending    []int              `json:"pending"`
	Started    []MapStartedHandle `json:"started"`
	NotStarted []int              `json:"not_started"`
	Remaining  []any              `json:"remaining"`
}

// CutInfo holds the structured cut payload returned to the session and persisted across cells.
type CutInfo struct {
	Reason       CutReason          `json:"reason"`
	Started      []WakeHandle       `json:"started,omitempty"`
	Calls        []CallJournalEntry `json:"calls,omitempty"`
	Maps         []MapCutRecord     `json:"maps,omitempty"`
	Sleeps       int                `json:"sleeps,omitempty"`
	Snapshot     []byte             `json:"snapshot,omitempty"`
	LostBindings []string           `json:"lost_bindings,omitempty"`
	Notice       string             `json:"notice,omitempty"`
}

// SnapshotBlob carries persisted cell bindings, engine continuation bytes, and cut state.
type SnapshotBlob struct {
	Format       int            `json:"format"`
	Engine       string         `json:"engine"`
	BuildID      string         `json:"build_id,omitempty"`
	SessionID    string         `json:"session_id"`
	Bindings     map[string]any `json:"bindings,omitempty"`
	EngineBytes  []byte         `json:"engine_bytes,omitempty"`
	Cut          *CutInfo       `json:"cut,omitempty"`
	LostBindings []string       `json:"lost_bindings,omitempty"`
	Notice       string         `json:"notice,omitempty"`
}

// EvalStatus is the high-level outcome of one cell evaluation.
type EvalStatus string

const (
	EvalCompleted EvalStatus = "completed"
	EvalCut       EvalStatus = "cut"
	EvalKilled    EvalStatus = "killed"
	EvalFailed    EvalStatus = "failed"
)

// EvalError describes a compile, policy, or runtime failure inside a cell.
type EvalError struct {
	Kind        string   `json:"kind"`
	Message     string   `json:"message"`
	Line        int      `json:"line,omitempty"`
	Col         int      `json:"col,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// EvalMetrics records per-cell execution timings and memory/snapshot footprint.
type EvalMetrics struct {
	CompileDuration time.Duration `json:"compile_duration"`
	ComputeDuration time.Duration `json:"compute_duration"`
	WaitDuration    time.Duration `json:"wait_duration"`
	TotalDuration   time.Duration `json:"total_duration"`
	SnapshotBytes   int           `json:"snapshot_bytes"`
}

// EvalRequest supplies one cell execution request to a KernelEngine.
type EvalRequest struct {
	CellID        string         `json:"cell_id"`
	SessionID     string         `json:"session_id"`
	OwnerID       string         `json:"owner_id"`
	Role          Role           `json:"role"`
	Code          string         `json:"code"`
	RemainingWait time.Duration  `json:"remaining_wait"`
	Limits        EngineLimits   `json:"limits"`
	Bindings      map[string]any `json:"bindings,omitempty"`
}

// EvalResult returns the completed value, structured cut info, or error plus metrics.
type EvalResult struct {
	Status       EvalStatus  `json:"status"`
	Value        any         `json:"value,omitempty"`
	Logs         []string    `json:"logs,omitempty"`
	Error        *EvalError  `json:"error,omitempty"`
	Cut          *CutInfo    `json:"cut,omitempty"`
	LostBindings []string    `json:"lost_bindings,omitempty"`
	Notice       string      `json:"notice,omitempty"`
	Metrics      EvalMetrics `json:"metrics"`
}

var (
	ErrEngineClosed        = errors.New("kernel engine is closed")
	ErrDelivererCollision  = errors.New("handle kind belongs to another deliverer in DELIVERS")
	ErrRoleDenied          = errors.New("tool denied for session role")
	ErrNativeUnavailable   = errors.New("native shared library not built or cgo disabled")
	ErrNonSerializableBind = errors.New("non-serializable binding rejected")
)

// KernelEngine defines the uniform embedded runtime boundary behind atlas/kernel.
type KernelEngine interface {
	Name() string
	Eval(ctx context.Context, req EvalRequest) (EvalResult, error)
	Restore(ctx context.Context, snap SnapshotBlob) error
	Snapshot(ctx context.Context) (SnapshotBlob, error)
	Cut(ctx context.Context, reason CutReason) (CutInfo, error)
	Kill(ctx context.Context) error
	BindTool(name string, spec ToolSpec, handler ToolHandler) error
	OnAsyncSettle(cb AsyncSettleFunc)
	Close() error
}
