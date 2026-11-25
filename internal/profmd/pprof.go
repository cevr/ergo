// Package profmd decodes Go runtime/pprof profiles (gzip-compressed
// perftools.profiles.Profile protobufs) and renders compact, deterministic,
// agent-readable Markdown summaries with zero external dependencies.
package profmd

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// Profile is a decoded pprof profile (perftools.profiles.Profile).
type Profile struct {
	SampleType    []ValueType
	Sample        []Sample
	Location      []*Location
	Function      []*Function
	StringTable   []string
	TimeNanos     int64
	DurationNanos int64
	PeriodType    ValueType
	Period        int64

	locationByID map[uint64]*Location
	functionByID map[uint64]*Function
}

// ValueType describes one sample value dimension (e.g. "cpu"/"nanoseconds" or
// "alloc_space"/"bytes").
type ValueType struct {
	Type string
	Unit string
}

// Sample is a single recorded stack sample and its corresponding values.
// LocationID is ordered leaf-first per the pprof specification.
type Sample struct {
	LocationID []uint64
	Value      []int64
}

// Location is a program counter location resolved to zero or more inlined
// source lines (innermost/leaf line first).
type Location struct {
	ID        uint64
	MappingID uint64
	Address   uint64
	Line      []Line
}

// Line identifies a function and source line number within a Location.
type Line struct {
	FunctionID uint64
	Line       int64
}

// Function describes a symbol referenced by Line entries.
type Function struct {
	ID         uint64
	Name       string
	SystemName string
	Filename   string
	StartLine  int64
}

type rawValueType struct {
	typeIdx int64
	unitIdx int64
}

type rawFunction struct {
	id         uint64
	nameIdx    int64
	sysNameIdx int64
	fileIdx    int64
	startLine  int64
}

// ParseProfile decompresses a gzip-encoded pprof protobuf and decodes it into
// a validated Profile. Empty, corrupt, or truncated inputs return an error.
func ParseProfile(pprofBytes []byte) (*Profile, error) {
	if len(pprofBytes) == 0 {
		return nil, errors.New("profmd: empty profile input")
	}

	gz, err := gzip.NewReader(bytes.NewReader(pprofBytes))
	if err != nil {
		return nil, fmt.Errorf("profmd: invalid gzip profile: %w", err)
	}
	raw, readErr := io.ReadAll(gz)
	closeErr := gz.Close()
	if readErr != nil {
		return nil, fmt.Errorf("profmd: corrupt gzip stream: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("profmd: corrupt gzip trailer: %w", closeErr)
	}
	if len(raw) == 0 {
		return nil, errors.New("profmd: empty protobuf payload")
	}

	p, err := decodeProfileProto(raw)
	if err != nil {
		return nil, err
	}
	if len(p.SampleType) == 0 {
		return nil, errors.New("profmd: profile has no sample types")
	}
	return p, nil
}

func decodeProfileProto(data []byte) (*Profile, error) {
	r := protoReader{b: data}
	p := &Profile{}
	var rawTypes []rawValueType
	var rawPeriodType rawValueType
	var hasPeriodType bool
	var rawFuncs []rawFunction

	for !r.eof() {
		fieldNum, wireType, err := r.readTag()
		if err != nil {
			return nil, fmt.Errorf("profmd: decode profile tag: %w", err)
		}
		switch fieldNum {
		case 1: // repeated ValueType sample_type
			sub, err := r.readBytes(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode sample_type: %w", err)
			}
			vt, err := decodeRawValueType(sub)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode sample_type: %w", err)
			}
			rawTypes = append(rawTypes, vt)
		case 2: // repeated Sample sample
			sub, err := r.readBytes(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode sample: %w", err)
			}
			s, err := decodeSample(sub)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode sample: %w", err)
			}
			p.Sample = append(p.Sample, s)
		case 4: // repeated Location location
			sub, err := r.readBytes(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode location: %w", err)
			}
			loc, err := decodeLocation(sub)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode location: %w", err)
			}
			p.Location = append(p.Location, loc)
		case 5: // repeated Function function
			sub, err := r.readBytes(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode function: %w", err)
			}
			fn, err := decodeRawFunction(sub)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode function: %w", err)
			}
			rawFuncs = append(rawFuncs, fn)
		case 6: // repeated string string_table
			sub, err := r.readBytes(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode string_table: %w", err)
			}
			p.StringTable = append(p.StringTable, string(sub))
		case 9: // int64 time_nanos
			v, err := r.readVarintField(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode time_nanos: %w", err)
			}
			p.TimeNanos = int64(v)
		case 10: // int64 duration_nanos
			v, err := r.readVarintField(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode duration_nanos: %w", err)
			}
			p.DurationNanos = int64(v)
		case 11: // ValueType period_type
			sub, err := r.readBytes(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode period_type: %w", err)
			}
			vt, err := decodeRawValueType(sub)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode period_type: %w", err)
			}
			rawPeriodType = vt
			hasPeriodType = true
		case 12: // int64 period
			v, err := r.readVarintField(wireType)
			if err != nil {
				return nil, fmt.Errorf("profmd: decode period: %w", err)
			}
			p.Period = int64(v)
		default:
			if err := r.skip(wireType); err != nil {
				return nil, fmt.Errorf("profmd: skip profile field %d: %w", fieldNum, err)
			}
		}
	}

	strAt := func(idx int64) (string, error) {
		if idx < 0 || int(idx) >= len(p.StringTable) {
			return "", fmt.Errorf("profmd: string_table index %d out of range [0, %d)", idx, len(p.StringTable))
		}
		return p.StringTable[idx], nil
	}

	p.SampleType = make([]ValueType, len(rawTypes))
	for i, rt := range rawTypes {
		t, err := strAt(rt.typeIdx)
		if err != nil {
			return nil, err
		}
		u, err := strAt(rt.unitIdx)
		if err != nil {
			return nil, err
		}
		p.SampleType[i] = ValueType{Type: t, Unit: u}
	}

	if hasPeriodType {
		t, err := strAt(rawPeriodType.typeIdx)
		if err != nil {
			return nil, err
		}
		u, err := strAt(rawPeriodType.unitIdx)
		if err != nil {
			return nil, err
		}
		p.PeriodType = ValueType{Type: t, Unit: u}
	}

	p.Function = make([]*Function, len(rawFuncs))
	p.functionByID = make(map[uint64]*Function, len(rawFuncs))
	for i, rf := range rawFuncs {
		name, err := strAt(rf.nameIdx)
		if err != nil {
			return nil, err
		}
		sysName, err := strAt(rf.sysNameIdx)
		if err != nil {
			return nil, err
		}
		file, err := strAt(rf.fileIdx)
		if err != nil {
			return nil, err
		}
		fn := &Function{
			ID:         rf.id,
			Name:       name,
			SystemName: sysName,
			Filename:   file,
			StartLine:  rf.startLine,
		}
		p.Function[i] = fn
		p.functionByID[fn.ID] = fn
	}

	p.locationByID = make(map[uint64]*Location, len(p.Location))
	for _, loc := range p.Location {
		p.locationByID[loc.ID] = loc
	}

	return p, nil
}

func decodeRawValueType(data []byte) (rawValueType, error) {
	r := protoReader{b: data}
	var vt rawValueType
	for !r.eof() {
		fieldNum, wireType, err := r.readTag()
		if err != nil {
			return vt, err
		}
		switch fieldNum {
		case 1:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return vt, err
			}
			vt.typeIdx = int64(v)
		case 2:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return vt, err
			}
			vt.unitIdx = int64(v)
		default:
			if err := r.skip(wireType); err != nil {
				return vt, err
			}
		}
	}
	return vt, nil
}

func decodeSample(data []byte) (Sample, error) {
	r := protoReader{b: data}
	var s Sample
	for !r.eof() {
		fieldNum, wireType, err := r.readTag()
		if err != nil {
			return s, err
		}
		switch fieldNum {
		case 1: // repeated uint64 location_id (packed or unpacked)
			if wireType == wireBytes {
				sub, err := r.readBytes(wireType)
				if err != nil {
					return s, err
				}
				pr := protoReader{b: sub}
				for !pr.eof() {
					v, err := pr.readVarint()
					if err != nil {
						return s, err
					}
					s.LocationID = append(s.LocationID, v)
				}
			} else {
				v, err := r.readVarintField(wireType)
				if err != nil {
					return s, err
				}
				s.LocationID = append(s.LocationID, v)
			}
		case 2: // repeated int64 value (packed or unpacked)
			if wireType == wireBytes {
				sub, err := r.readBytes(wireType)
				if err != nil {
					return s, err
				}
				pr := protoReader{b: sub}
				for !pr.eof() {
					v, err := pr.readVarint()
					if err != nil {
						return s, err
					}
					s.Value = append(s.Value, int64(v))
				}
			} else {
				v, err := r.readVarintField(wireType)
				if err != nil {
					return s, err
				}
				s.Value = append(s.Value, int64(v))
			}
		default:
			if err := r.skip(wireType); err != nil {
				return s, err
			}
		}
	}
	return s, nil
}

func decodeLocation(data []byte) (*Location, error) {
	r := protoReader{b: data}
	loc := &Location{}
	for !r.eof() {
		fieldNum, wireType, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch fieldNum {
		case 1:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return nil, err
			}
			loc.ID = v
		case 2:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return nil, err
			}
			loc.MappingID = v
		case 3:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return nil, err
			}
			loc.Address = v
		case 4:
			sub, err := r.readBytes(wireType)
			if err != nil {
				return nil, err
			}
			ln, err := decodeLine(sub)
			if err != nil {
				return nil, err
			}
			loc.Line = append(loc.Line, ln)
		default:
			if err := r.skip(wireType); err != nil {
				return nil, err
			}
		}
	}
	return loc, nil
}

func decodeLine(data []byte) (Line, error) {
	r := protoReader{b: data}
	var ln Line
	for !r.eof() {
		fieldNum, wireType, err := r.readTag()
		if err != nil {
			return ln, err
		}
		switch fieldNum {
		case 1:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return ln, err
			}
			ln.FunctionID = v
		case 2:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return ln, err
			}
			ln.Line = int64(v)
		default:
			if err := r.skip(wireType); err != nil {
				return ln, err
			}
		}
	}
	return ln, nil
}

func decodeRawFunction(data []byte) (rawFunction, error) {
	r := protoReader{b: data}
	var fn rawFunction
	for !r.eof() {
		fieldNum, wireType, err := r.readTag()
		if err != nil {
			return fn, err
		}
		switch fieldNum {
		case 1:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return fn, err
			}
			fn.id = v
		case 2:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return fn, err
			}
			fn.nameIdx = int64(v)
		case 3:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return fn, err
			}
			fn.sysNameIdx = int64(v)
		case 4:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return fn, err
			}
			fn.fileIdx = int64(v)
		case 5:
			v, err := r.readVarintField(wireType)
			if err != nil {
				return fn, err
			}
			fn.startLine = int64(v)
		default:
			if err := r.skip(wireType); err != nil {
				return fn, err
			}
		}
	}
	return fn, nil
}

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

type protoReader struct {
	b   []byte
	pos int
}

func (r *protoReader) eof() bool {
	return r.pos >= len(r.b)
}

func (r *protoReader) readVarint() (uint64, error) {
	var v uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if r.pos >= len(r.b) {
			return 0, errors.New("truncated varint")
		}
		b := r.b[r.pos]
		r.pos++
		if shift == 63 && b > 1 {
			return 0, errors.New("varint overflow")
		}
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v, nil
		}
	}
	return 0, errors.New("varint overflow")
}

func (r *protoReader) readTag() (uint64, uint64, error) {
	tag, err := r.readVarint()
	if err != nil {
		return 0, 0, err
	}
	fieldNum := tag >> 3
	wireType := tag & 7
	if fieldNum == 0 {
		return 0, 0, errors.New("invalid protobuf field number 0")
	}
	return fieldNum, wireType, nil
}

func (r *protoReader) readVarintField(wireType uint64) (uint64, error) {
	if wireType != wireVarint {
		return 0, fmt.Errorf("expected varint wire type 0, got %d", wireType)
	}
	return r.readVarint()
}

func (r *protoReader) readBytes(wireType uint64) ([]byte, error) {
	if wireType != wireBytes {
		return nil, fmt.Errorf("expected length-delimited wire type 2, got %d", wireType)
	}
	n, err := r.readVarint()
	if err != nil {
		return nil, err
	}
	rem := uint64(len(r.b) - r.pos)
	if n > rem {
		return nil, fmt.Errorf("length-delimited field truncated: need %d bytes, have %d", n, rem)
	}
	out := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return out, nil
}

func (r *protoReader) skip(wireType uint64) error {
	switch wireType {
	case wireVarint:
		_, err := r.readVarint()
		return err
	case wireFixed64:
		if len(r.b)-r.pos < 8 {
			return errors.New("truncated fixed64")
		}
		r.pos += 8
		return nil
	case wireBytes:
		_, err := r.readBytes(wireBytes)
		return err
	case wireFixed32:
		if len(r.b)-r.pos < 4 {
			return errors.New("truncated fixed32")
		}
		r.pos += 4
		return nil
	default:
		return fmt.Errorf("unsupported protobuf wire type %d", wireType)
	}
}
