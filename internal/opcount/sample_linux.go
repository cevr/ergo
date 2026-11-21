//go:build linux

package opcount

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	perfSampleIP        = 1 << 0
	perfSampleTID       = 1 << 1
	perfSampleTime      = 1 << 2
	perfFlagComm        = 1 << 9
	perfFlagSampleIDAll = 1 << 18
	perfFlagCommExec    = 1 << 24
	perfRecordLost      = 2
	perfRecordComm      = 3
	perfRecordThrottle  = 5
	perfRecordSample    = 9
	perfRecordLostSamp  = 13
	perfMiscCommExec    = 1 << 13
	// ringPages is each CPU's ring of samples, in pages: 16 KiB on 4 KiB
	// pages, so sixteen CPUs stay inside perf_event_mlock_kb's default
	// 516 KiB. The drain empties it every drainEvery.
	ringPages  = 4
	drainEvery = 5 * time.Millisecond
	// fastest is a bound on the user instructions one CPU retires per
	// second (5 GHz at 4 a cycle), which sets the period: at most
	// perf_event_max_sample_rate samples a second, or the kernel
	// throttles the event (PartsShort). The kernel's budget is per tick,
	// so a burst above it throttles too; at 8e9, a child that allocates
	// was throttled 131 times on this host.
	fastest = 2e10
)

// Period is the sampling period in user instructions on this host: the
// fewest instructions between samples that keep a CPU running at fastest
// under the kernel's perf_event_max_sample_rate, made odd so it does not
// beat with a loop's length.
// PeriodVariable, when set to a number, is the period instead.
func Period() uint64 {
	if p, err := strconv.ParseUint(os.Getenv(PeriodVariable), 10, 64); err == nil && p > 0 {
		return p
	}
	rate := 100000.0
	if data, err := os.ReadFile("/proc/sys/kernel/perf_event_max_sample_rate"); err == nil {
		if r, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil && r > 0 {
			rate = r
		}
	}
	return uint64(fastest/rate) | 1
}

// sample is one sampled instruction: its process, its time and where.
type sample struct {
	pid  uint32
	time uint64
	ip   uint64
}

// sampler samples the user instructions of the process tree the locked
// thread starts, on every online CPU, into one ring per CPU.
type sampler struct {
	period  uint64
	fds     []int
	rings   [][]byte
	mu      sync.Mutex
	samples []sample
	execs   map[uint32]uint64 // pid -> time of its last exec
	lost    uint64
	thrott  uint64
	stop    chan struct{}
	stopped chan struct{}
}

// onlineCPUs reads /sys/devices/system/cpu/online ("0-15", "0,2-5").
func onlineCPUs() ([]int, error) {
	data, err := os.ReadFile("/sys/devices/system/cpu/online")
	if err != nil {
		return nil, err
	}
	var cpus []int
	for _, r := range strings.Split(strings.TrimSpace(string(data)), ",") {
		lo, hi, ranged := strings.Cut(r, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("cpu list %q", data)
		}
		b := a
		if ranged {
			if b, err = strconv.Atoi(hi); err != nil {
				return nil, fmt.Errorf("cpu list %q", data)
			}
		}
		for c := a; c <= b; c++ {
			cpus = append(cpus, c)
		}
	}
	return cpus, nil
}

// openSampler opens, on the calling (locked) thread, one sampling event per
// online CPU: disabled, inherited by the processes the thread starts, and
// enabled in each at its exec, as openCounter's counting event. A per-task
// event that is inherited cannot be mapped (the kernel refuses), so the
// events are per CPU; an inherited event writes its samples to its
// parent's ring.
func openSampler(period uint64) (*sampler, error) {
	cpus, err := onlineCPUs()
	if err != nil {
		return nil, err
	}
	s := &sampler{period: period, execs: map[uint32]uint64{}, stop: make(chan struct{}), stopped: make(chan struct{})}
	size := (1 + ringPages) * os.Getpagesize()
	for _, cpu := range cpus {
		attr := perfEventAttr{
			Type:         perfTypeHardware,
			Config:       perfCountHWInstr,
			SamplePeriod: period,
			SampleType:   perfSampleIP | perfSampleTID | perfSampleTime,
			ReadFormat:   perfFormatEnabled | perfFormatRunning,
			Flags:        perfFlagDisabled | perfFlagInherit | perfFlagExclKernel | perfFlagExclHV | perfFlagComm | perfFlagEnableExec | perfFlagSampleIDAll | perfFlagCommExec,
		}
		attr.Size = uint32(unsafe.Sizeof(attr))
		fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, uintptr(cpu), ^uintptr(0), perfFlagFDCloexec, 0)
		if errno != 0 {
			s.close()
			return nil, fmt.Errorf("perf_event_open of sampled user instructions on cpu %d: %w", cpu, errno)
		}
		s.fds = append(s.fds, int(fd))
		ring, err := syscall.Mmap(int(fd), 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("mmap of cpu %d's ring of samples: %w (perf_event_mlock_kb)", cpu, err)
		}
		s.rings = append(s.rings, ring)
	}
	go s.drainLoop()
	return s, nil
}

func (s *sampler) drainLoop() {
	defer close(s.stopped)
	t := time.NewTicker(drainEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.drain()
		}
	}
}

// drain reads every ring's records from its tail to its head.
func (s *sampler) drain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := os.Getpagesize()
	for _, ring := range s.rings {
		meta := unsafe.Pointer(&ring[0])
		head := atomic.LoadUint64((*uint64)(unsafe.Add(meta, 1024)))
		tail := atomic.LoadUint64((*uint64)(unsafe.Add(meta, 1032)))
		data := ring[page:]
		n := uint64(len(data))
		var rec []byte
		for tail < head {
			hdr := make([]byte, 8)
			for i := range hdr {
				hdr[i] = data[(tail+uint64(i))%n]
			}
			typ, misc, size := binary.NativeEndian.Uint32(hdr), binary.NativeEndian.Uint16(hdr[4:]), uint64(binary.NativeEndian.Uint16(hdr[6:]))
			if size < 8 {
				break
			}
			rec = rec[:0]
			for i := uint64(0); i < size; i++ {
				rec = append(rec, data[(tail+i)%n])
			}
			s.record(typ, misc, rec)
			tail += size
		}
		atomic.StoreUint64((*uint64)(unsafe.Add(meta, 1032)), tail)
	}
}

// record takes one record: a sample, an exec (COMM with the exec flag,
// whose sample_id_all trailer ends in its pid, tid and time), or a loss.
func (s *sampler) record(typ uint32, misc uint16, rec []byte) {
	word := func(off int) uint64 { return binary.NativeEndian.Uint64(rec[off:]) }
	switch typ {
	case perfRecordSample:
		if len(rec) >= 32 {
			s.samples = append(s.samples, sample{ip: word(8), pid: binary.NativeEndian.Uint32(rec[16:]), time: word(24)})
		}
	case perfRecordComm:
		if misc&perfMiscCommExec != 0 && len(rec) >= 24 {
			pid := binary.NativeEndian.Uint32(rec[8:])
			at := word(len(rec) - 8)
			if at > s.execs[pid] {
				s.execs[pid] = at
			}
		}
	case perfRecordLost, perfRecordLostSamp:
		if len(rec) >= 24 {
			s.lost += word(16)
		} else {
			s.lost++
		}
	case perfRecordThrottle:
		s.thrott++
	}
}

// finish stops the drain, drains once more, reads the events' counts and
// gives the run's shares: the root's samples from its last exec on, by the
// part of bin's function that holds them, every other sample Outside.
func (s *sampler) finish(root int, bin string) Shares {
	close(s.stop)
	<-s.stopped
	s.drain()
	var sh Shares
	for _, fd := range s.fds {
		var buf [24]byte
		if n, err := syscall.Read(fd, buf[:]); err != nil || n != len(buf) {
			sh.Refused, sh.Why = NoSampler, fmt.Sprintf("read a sampling event: %d bytes, %v", n, err)
			return sh
		}
		// An event of one CPU is enabled but not running while the task
		// runs on another, so its times say nothing of multiplexing; a
		// multiplexed event counts short, which Shares.Check refuses.
		sh.Counted += binary.NativeEndian.Uint64(buf[:8])
	}
	switch {
	case s.lost > 0:
		sh.Refused, sh.Why = PartsShort, fmt.Sprintf("the rings lost %d samples", s.lost)
		return sh
	case s.thrott > 0:
		sh.Refused, sh.Why = PartsShort, fmt.Sprintf("the kernel throttled the sampling %d times (perf_event_max_sample_rate)", s.thrott)
		return sh
	}
	syms, err := ReadSymbols(bin)
	if err != nil {
		sh.Refused, sh.Why = NoSampler, err.Error()
		return sh
	}
	since := s.execs[uint32(root)]
	for _, x := range s.samples {
		p := Outside
		if x.pid == uint32(root) && x.time >= since {
			p = syms.Part(x.ip)
		}
		sh.Samples[p]++
	}
	return sh
}

func (s *sampler) close() {
	for _, r := range s.rings {
		syscall.Munmap(r)
	}
	for _, fd := range s.fds {
		syscall.Close(fd)
	}
}

// CountShares is Count with the run's parts: it also samples the run's
// user instructions every Period() of them and reads which part of the
// executable bin (cmd's program) holds each sample. The parts are checked
// against the count (Shares.Check). A host that cannot sample still
// counts; its parts are refused NoSampler.
func CountShares(cmd *exec.Cmd, limit uint64, bin string) (Run, Shares, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	s, err := openSampler(Period())
	if err != nil {
		r, err2 := Count(cmd, limit)
		return r, Shares{Refused: NoSampler, Why: err.Error()}, err2
	}
	defer s.close()
	r, err := Count(cmd, limit)
	if err != nil {
		close(s.stop)
		<-s.stopped
		return r, Shares{}, err
	}
	sh := s.finish(cmd.Process.Pid, bin)
	sh.Check(r)
	return r, sh, nil
}
