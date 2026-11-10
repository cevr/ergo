package sched

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// The worker transport. An explored test binary is built once; a search
// runs its schedules in worker processes of it. A worker runs one schedule
// per request, asking the search's picker at each point over a pipe, and
// runs the next request only while every run it ran ended with every task
// ended. A run that left a task (a stop, a deadlock, an escape) ends the
// worker: its process group is reaped and the next run starts a new one.
// So a worker never holds the stacks of an abandoned run, and a search
// starts at most one worker more than the runs it abandoned.
//
// The messages are JSON lines. The search sends {"run":true} to start a
// run, then answers each {"choice":…} with {"pick":i} or {"stop":why}; the
// worker ends each run with {"end":…}.

// WorkerEnv names the variable that starts an explored test binary as a
// worker: it reads requests on descriptor 3, writes its choices and
// outcomes on descriptor 4, and its standard output is a file the search
// reads (runtime/v1, explore_on.go).
const WorkerEnv = "ERGO_EXPLORE_WORKER"

type toWorker struct {
	Run  bool   `json:"run,omitempty"`
	Pick int    `json:"pick"`
	Stop string `json:"stop,omitempty"`
}

type fromWorker struct {
	Choice *Choice  `json:"choice,omitempty"`
	End    *Outcome `json:"end,omitempty"`
}

// Serve is a worker's loop: for each run request on in, it calls run with
// a picker that asks over out and in, and writes the run's outcome. It
// returns nil when in ends, and after a run that was abandoned.
func Serve(in io.Reader, out io.Writer, run func(Picker) Outcome) error {
	dec := json.NewDecoder(bufio.NewReader(in))
	enc := json.NewEncoder(out)
	for {
		var m toWorker
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if !m.Run {
			return fmt.Errorf("sched: the worker wanted a run request, got %+v", m)
		}
		o := run(remote{dec, enc})
		if err := enc.Encode(fromWorker{End: &o}); err != nil {
			return err
		}
		if o.Abandoned {
			return nil
		}
	}
}

// remote is the worker's picker: it asks the search.
type remote struct {
	dec *json.Decoder
	enc *json.Encoder
}

func (r remote) Pick(c Choice) (int, error) {
	if err := r.enc.Encode(fromWorker{Choice: &c}); err != nil {
		return 0, err
	}
	var m toWorker
	if err := r.dec.Decode(&m); err != nil {
		return 0, err
	}
	if m.Stop != "" {
		return 0, errors.New(m.Stop)
	}
	return m.Pick, nil
}

// Conn is a worker: its pipes, and Close, which ends it and reaps its
// process group.
type Conn interface {
	io.Reader
	io.Writer
	Close() error
}

// Pool runs schedules in workers that Start makes. It is an Executor.
type Pool struct {
	Start func() (Conn, error)
	// Started counts the workers it started; Abandoned the runs that
	// ended a worker.
	Started, Abandoned int

	conn Conn
	dec  *json.Decoder
	enc  *json.Encoder
}

// Run is Executor's: one schedule in the current worker, or in a new one.
func (p *Pool) Run(pk Picker) Outcome {
	if p.conn == nil {
		c, err := p.Start()
		if err != nil {
			return Outcome{Failure: "explore: the worker did not start: " + err.Error(), Abandoned: true}
		}
		p.Started++
		p.conn, p.dec, p.enc = c, json.NewDecoder(bufio.NewReader(c)), json.NewEncoder(c)
	}
	o, err := p.run(pk)
	if err != nil {
		o = Outcome{Failure: "explore: the worker ended in a run: " + err.Error(), Abandoned: true}
	}
	if o.Abandoned {
		p.Abandoned++
		p.Close()
	}
	return o
}

func (p *Pool) run(pk Picker) (Outcome, error) {
	if err := p.enc.Encode(toWorker{Run: true}); err != nil {
		return Outcome{}, err
	}
	for {
		var m fromWorker
		if err := p.dec.Decode(&m); err != nil {
			return Outcome{}, err
		}
		switch {
		case m.End != nil:
			return *m.End, nil
		case m.Choice != nil:
			k, err := pk.Pick(*m.Choice)
			reply := toWorker{Pick: k}
			if err != nil {
				reply = toWorker{Stop: err.Error()}
			}
			if err := p.enc.Encode(reply); err != nil {
				return Outcome{}, err
			}
		default:
			return Outcome{}, errors.New("an empty message")
		}
	}
}

// Close ends the current worker, if any.
func (p *Pool) Close() {
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
}
