package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"

	"ergo.local/prototype/internal/profmd"
)

const (
	defaultCPUProfPath    = "ergo-cpu-profile.pprof"
	defaultCPUProfMDPath  = "ergo-cpu-profile.md"
	defaultHeapProfPath   = "ergo-heap-profile.pprof"
	defaultHeapProfMDPath = "ergo-heap-profile.md"
)

type profileConfig struct {
	cpuRawPath  string
	cpuMDPath   string
	heapRawPath string
	heapMDPath  string
}

func (c profileConfig) enabled() bool {
	return c.cpuRawPath != "" || c.cpuMDPath != "" || c.heapRawPath != "" || c.heapMDPath != ""
}

func (c profileConfig) wantCPU() bool {
	return c.cpuRawPath != "" || c.cpuMDPath != ""
}

func (c profileConfig) wantHeap() bool {
	return c.heapRawPath != "" || c.heapMDPath != ""
}

// extractProfileConfig strips global profiling flags (--cpu-prof, --cpu-prof-md,
// --heap-prof, --heap-prof-md) anywhere before "--" and applies ERGO_CPU_PROF_MD
// and ERGO_HEAP_PROF_MD env defaults when the corresponding flag is unset.
func extractProfileConfig(args []string, getenv func(string) string) ([]string, profileConfig, error) {
	var cfg profileConfig
	out := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}

		matched, nextIdx, err := parseProfileFlag(args, i, &cfg)
		if err != nil {
			return nil, profileConfig{}, err
		}
		if matched {
			i = nextIdx
			continue
		}
		out = append(out, arg)
	}

	if getenv != nil {
		if cfg.cpuMDPath == "" {
			cfg.cpuMDPath = resolveEnvProfilePath(getenv("ERGO_CPU_PROF_MD"), defaultCPUProfMDPath)
		}
		if cfg.heapMDPath == "" {
			cfg.heapMDPath = resolveEnvProfilePath(getenv("ERGO_HEAP_PROF_MD"), defaultHeapProfMDPath)
		}
	}

	return out, cfg, nil
}

type profileFlagSpec struct {
	name        string
	defaultPath string
	isMarkdown  bool
	assign      func(*profileConfig, string)
}

var profileFlagSpecs = []profileFlagSpec{
	{
		name:        "--cpu-prof",
		defaultPath: defaultCPUProfPath,
		isMarkdown:  false,
		assign:      func(c *profileConfig, p string) { c.cpuRawPath = p },
	},
	{
		name:        "--cpu-prof-md",
		defaultPath: defaultCPUProfMDPath,
		isMarkdown:  true,
		assign:      func(c *profileConfig, p string) { c.cpuMDPath = p },
	},
	{
		name:        "--heap-prof",
		defaultPath: defaultHeapProfPath,
		isMarkdown:  false,
		assign:      func(c *profileConfig, p string) { c.heapRawPath = p },
	},
	{
		name:        "--heap-prof-md",
		defaultPath: defaultHeapProfMDPath,
		isMarkdown:  true,
		assign:      func(c *profileConfig, p string) { c.heapMDPath = p },
	},
}

func parseProfileFlag(args []string, idx int, cfg *profileConfig) (bool, int, error) {
	arg := args[idx]
	for _, spec := range profileFlagSpecs {
		if arg == spec.name {
			path := spec.defaultPath
			nextIdx := idx
			if idx+1 < len(args) && shouldConsumeProfilePath(args[idx+1], spec.isMarkdown) {
				path = args[idx+1]
				nextIdx = idx + 1
			}
			spec.assign(cfg, path)
			return true, nextIdx, nil
		}
		prefix := spec.name + "="
		if val, ok := strings.CutPrefix(arg, prefix); ok {
			if val == "" {
				return false, idx, usageError{message: spec.name + ": missing output path"}
			}
			spec.assign(cfg, val)
			return true, idx, nil
		}
	}
	return false, idx, nil
}

func shouldConsumeProfilePath(next string, isMarkdown bool) bool {
	if next == "-" {
		return true
	}
	if next == "" || strings.HasPrefix(next, "-") {
		return false
	}
	if isKnownCommandWord(next) {
		return false
	}
	lower := strings.ToLower(next)
	if isMarkdown {
		return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
	}
	return strings.HasSuffix(lower, ".pprof") || strings.HasSuffix(lower, ".prof") || strings.HasSuffix(lower, ".pb.gz")
}

func isKnownCommandWord(word string) bool {
	for _, c := range commands {
		first, _, _ := strings.Cut(c.name, " ")
		if first == word {
			return true
		}
	}
	return false
}

func resolveEnvProfilePath(raw, defaultPath string) string {
	v := strings.TrimSpace(raw)
	switch strings.ToLower(v) {
	case "", "0", "false":
		return ""
	case "1", "true":
		return defaultPath
	default:
		return v
	}
}

type profileSession struct {
	cfg        profileConfig
	stderr     io.Writer
	cpuBuf     bytes.Buffer
	cpuStarted bool
}

func startProfileSession(cfg profileConfig, stderr io.Writer) (*profileSession, error) {
	if !cfg.enabled() {
		return nil, nil
	}
	s := &profileSession{
		cfg:    cfg,
		stderr: stderr,
	}
	if cfg.wantCPU() {
		if err := pprof.StartCPUProfile(&s.cpuBuf); err != nil {
			return nil, fmt.Errorf("start CPU profile: %w", err)
		}
		s.cpuStarted = true
	}
	return s, nil
}

func (s *profileSession) finish() error {
	if s == nil {
		return nil
	}
	var errs []string
	wroteStderr := false

	if s.cpuStarted {
		pprof.StopCPUProfile()
		s.cpuStarted = false
		raw := s.cpuBuf.Bytes()
		if s.cfg.cpuRawPath != "" {
			if err := writeProfileOutput(s.cfg.cpuRawPath, raw, s.stderr, &wroteStderr); err != nil {
				errs = append(errs, fmt.Sprintf("write %s: %v", s.cfg.cpuRawPath, err))
			}
		}
		if s.cfg.cpuMDPath != "" {
			md, err := profmd.RenderCPUMarkdown(raw, profmd.Options{})
			if err != nil {
				errs = append(errs, fmt.Sprintf("render CPU markdown: %v", err))
			} else if err := writeProfileOutput(s.cfg.cpuMDPath, []byte(md), s.stderr, &wroteStderr); err != nil {
				errs = append(errs, fmt.Sprintf("write %s: %v", s.cfg.cpuMDPath, err))
			}
		}
	}

	if s.cfg.wantHeap() {
		runtime.GC()
		var heapBuf bytes.Buffer
		if err := pprof.WriteHeapProfile(&heapBuf); err != nil {
			errs = append(errs, fmt.Sprintf("capture heap profile: %v", err))
		} else {
			raw := heapBuf.Bytes()
			if s.cfg.heapRawPath != "" {
				if err := writeProfileOutput(s.cfg.heapRawPath, raw, s.stderr, &wroteStderr); err != nil {
					errs = append(errs, fmt.Sprintf("write %s: %v", s.cfg.heapRawPath, err))
				}
			}
			if s.cfg.heapMDPath != "" {
				md, err := profmd.RenderHeapMarkdown(raw, profmd.Options{})
				if err != nil {
					errs = append(errs, fmt.Sprintf("render heap markdown: %v", err))
				} else if err := writeProfileOutput(s.cfg.heapMDPath, []byte(md), s.stderr, &wroteStderr); err != nil {
					errs = append(errs, fmt.Sprintf("write %s: %v", s.cfg.heapMDPath, err))
				}
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func writeProfileOutput(path string, data []byte, stderr io.Writer, wroteStderr *bool) error {
	if path == "-" {
		if *wroteStderr {
			if _, err := io.WriteString(stderr, "\n"); err != nil {
				return err
			}
		}
		*wroteStderr = true
		_, err := stderr.Write(data)
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".ergo-prof-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
