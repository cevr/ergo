//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package v1

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"ergo.local/prototype/runtime/rt"
)

const processWorkerFlag = "-ergo.runtime.process-worker"

type procWireEnvelope struct {
	Tag     string
	ID      string
	RPC     string
	Payload string
}

type procWireLimits struct {
	TimeNanos int64
	Steps     int
	Bytes     int
}

type procWireEnd struct {
	Kind  string
	Value int
	Code  int
	Text  string
}

type procWireScript struct {
	Cleanups  int
	WaitNanos int64
	Steps     int
	End       procWireEnd
}

type procWirePlay struct {
	Limits  procWireLimits
	Script  procWireScript
	OneShot bool
}

type procWireReport struct {
	Kind       string
	Value      int
	Code       int
	Text       string
	LimitNanos int64
	LimitSteps int
	Cleaned    int
}

func decodeWireEnvelope(text string) (procWireEnvelope, error) {
	c, err := JSONOpen(text, 0, 0)
	if err != nil {
		return procWireEnvelope{}, err
	}
	if !c.BeginObject() {
		return procWireEnvelope{}, fmt.Errorf("expected object")
	}
	var env procWireEnvelope
	for c.More() {
		switch c.Key() {
		case "tag":
			env.Tag = c.String()
		case "id":
			env.ID = c.String()
		case "rpc":
			env.RPC = c.String()
		case "payload":
			env.Payload = c.String()
		default:
			c.Skip()
		}
		if c.Wrong() {
			return procWireEnvelope{}, fmt.Errorf("invalid field in envelope")
		}
	}
	c.End()
	if err := JSONSettle(c); err != nil {
		return procWireEnvelope{}, err
	}
	return env, nil
}

func decodeWireLimits(c *Cursor) (procWireLimits, error) {
	if !c.BeginObject() {
		return procWireLimits{}, fmt.Errorf("expected limits object")
	}
	var out procWireLimits
	for c.More() {
		switch c.Key() {
		case "timeNanos":
			out.TimeNanos = c.Int()
		case "steps":
			out.Steps = int(c.Int())
		case "bytes":
			out.Bytes = int(c.Int())
		default:
			c.Skip()
		}
		if c.Wrong() {
			return procWireLimits{}, fmt.Errorf("invalid limits field")
		}
	}
	c.End()
	return out, nil
}

func decodeWireEnd(c *Cursor) (procWireEnd, error) {
	if !c.BeginObject() {
		return procWireEnd{}, fmt.Errorf("expected end object")
	}
	var out procWireEnd
	for c.More() {
		switch c.Key() {
		case "kind":
			out.Kind = c.String()
		case "value":
			out.Value = int(c.Int())
		case "code":
			out.Code = int(c.Int())
		case "text":
			out.Text = c.String()
		default:
			c.Skip()
		}
		if c.Wrong() {
			return procWireEnd{}, fmt.Errorf("invalid end field")
		}
	}
	c.End()
	return out, nil
}

func decodeWireScript(c *Cursor) (procWireScript, error) {
	if !c.BeginObject() {
		return procWireScript{}, fmt.Errorf("expected script object")
	}
	var out procWireScript
	for c.More() {
		switch c.Key() {
		case "cleanups":
			out.Cleanups = int(c.Int())
		case "waitNanos":
			out.WaitNanos = c.Int()
		case "steps":
			out.Steps = int(c.Int())
		case "end":
			end, err := decodeWireEnd(c)
			if err != nil {
				return procWireScript{}, err
			}
			out.End = end
		default:
			c.Skip()
		}
		if c.Wrong() {
			return procWireScript{}, fmt.Errorf("invalid script field")
		}
	}
	c.End()
	return out, nil
}

func decodeWirePlay(text string) (procWirePlay, error) {
	c, err := JSONOpen(text, 0, 0)
	if err != nil {
		return procWirePlay{}, err
	}
	if !c.BeginObject() {
		return procWirePlay{}, fmt.Errorf("expected play object")
	}
	var out procWirePlay
	for c.More() {
		switch c.Key() {
		case "limits":
			l, err := decodeWireLimits(c)
			if err != nil {
				return procWirePlay{}, err
			}
			out.Limits = l
		case "script":
			s, err := decodeWireScript(c)
			if err != nil {
				return procWirePlay{}, err
			}
			out.Script = s
		case "oneShot":
			out.OneShot = c.Bool()
		default:
			c.Skip()
		}
		if c.Wrong() {
			return procWirePlay{}, fmt.Errorf("invalid play field")
		}
	}
	c.End()
	if err := JSONSettle(c); err != nil {
		return procWirePlay{}, err
	}
	return out, nil
}

func encodeWireEnvelope(env procWireEnvelope) string {
	var b []byte
	b = append(b, `{"tag":`...)
	b = JSONAppendString(b, env.Tag)
	b = append(b, `,"id":`...)
	b = JSONAppendString(b, env.ID)
	b = append(b, `,"rpc":`...)
	b = JSONAppendString(b, env.RPC)
	b = append(b, `,"payload":`...)
	b = JSONAppendString(b, env.Payload)
	b = append(b, '}')
	return string(b)
}

func encodeWireWait(nanos int64) string {
	var b []byte
	b = append(b, `{"nanos":`...)
	b = JSONAppendInt(b, nanos)
	b = append(b, '}')
	return string(b)
}

func encodeWireReport(r procWireReport) string {
	var b []byte
	b = append(b, `{"kind":`...)
	b = JSONAppendString(b, r.Kind)
	b = append(b, `,"value":`...)
	b = JSONAppendInt(b, int64(r.Value))
	b = append(b, `,"code":`...)
	b = JSONAppendInt(b, int64(r.Code))
	b = append(b, `,"text":`...)
	b = JSONAppendString(b, r.Text)
	b = append(b, `,"limitNanos":`...)
	b = JSONAppendInt(b, r.LimitNanos)
	b = append(b, `,"limitSteps":`...)
	b = JSONAppendInt(b, int64(r.LimitSteps))
	b = append(b, `,"cleaned":`...)
	b = JSONAppendInt(b, int64(r.Cleaned))
	b = append(b, '}')
	return string(b)
}

func writeWorkerLine(s string) {
	_, _ = os.Stdout.WriteString(s + "\n")
}

func runProcessWorkerMain() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		env, err := decodeWireEnvelope(line)
		if err != nil {
			writeWorkerLine(encodeWireEnvelope(procWireEnvelope{Tag: "Defect", Payload: err.Error()}))
			continue
		}
		if env.Tag != "Request" || env.RPC != "process.Play" {
			continue
		}
		req, err := decodeWirePlay(env.Payload)
		if err != nil {
			writeWorkerLine(encodeWireEnvelope(procWireEnvelope{Tag: "Defect", ID: env.ID, RPC: env.RPC, Payload: err.Error()}))
			continue
		}
		rep := runWorkerPlay(sc, env.ID, req)
		writeWorkerLine(encodeWireEnvelope(procWireEnvelope{
			Tag:     "Reply",
			ID:      env.ID,
			RPC:     env.RPC,
			Payload: encodeWireReport(rep),
		}))
	}
}

func runWorkerPlay(sc *bufio.Scanner, reqID string, req procWirePlay) procWireReport {
	var cleaned int
	var aborted bool
	task := rt.Root("process-worker")
	if req.Limits.Steps > 0 {
		task.SetStepBudget(int64(req.Limits.Steps))
	}
	res, _ := rt.Run(task, func(t *rt.Task) (procWireReport, error) {
		t.Check()
		t.Check()
		for i := 0; i < req.Script.Cleanups; i++ {
			defer func() { cleaned++ }()
			t.Check()
		}
		for i := 0; i < req.Script.Steps; i++ {
			t.Check()
		}
		if req.Script.WaitNanos > 0 {
			t.Check()
			t.Check()
			if !req.OneShot {
				writeWorkerLine(encodeWireEnvelope(procWireEnvelope{
					Tag:     "NeedSleep",
					ID:      reqID,
					RPC:     "process.Play",
					Payload: encodeWireWait(req.Script.WaitNanos),
				}))
				if !sc.Scan() {
					aborted = true
					return procWireReport{}, nil
				}
				ack, err := decodeWireEnvelope(strings.TrimSpace(sc.Text()))
				if err != nil || ack.Tag != "Go" {
					aborted = true
					return procWireReport{}, nil
				}
			}
		}
		switch req.Script.End.Kind {
		case "Gives":
			return procWireReport{Kind: "Gives", Value: req.Script.End.Value}, nil
		case "Fails":
			return procWireReport{Kind: "Fails", Code: req.Script.End.Code}, nil
		default:
			return procWireReport{Kind: "Crashed", Text: "die at process.ergo:280: {" + req.Script.End.Text + "}"}, nil
		}
	})
	res.Cleaned = cleaned
	if task.BudgetExceeded() {
		return procWireReport{Kind: "OverSteps", LimitSteps: req.Limits.Steps, Cleaned: cleaned}
	}
	if aborted {
		return procWireReport{Kind: "OverTime", LimitNanos: req.Limits.TimeNanos, Cleaned: cleaned}
	}
	return res
}
