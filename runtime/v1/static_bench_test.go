package v1

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// BenchmarkReleaseStatic compares the emitted static shape to B1's owner
// list and hand Go. Each local is closed once; loop locals close at each
// iteration. These are runtime-shape controls, not a whole-program claim.
func BenchmarkReleaseStatic(b *testing.B) {
	root := rt.Root("main")
	var n int
	b.Run("local/static", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			func() {
				var live uint64
				var slot benchRes
				defer func() {
					s := StaticBegin(root, nil, recover())
					if live&1 != 0 {
						s.Part(func() { slot.Close() })
					}
					s.End(nil)
				}()
				_ = OwnStatic(&live, 1, &slot, benchRes{&n})
			}()
		}
	})
	b.Run("local/scope", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			frameNoRow(root, func(s *Scope) {
				_ = Owning(s.Owner(), benchRes{&n}, func(v benchRes) { v.Close() })
			})
		}
	})
	b.Run("local/hand", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			func() {
				r := benchRes{&n}
				defer r.Close()
			}()
		}
	})
	b.Run("loop/static", func(b *testing.B) {
		b.ReportAllocs()
		var live uint64
		var slot benchRes
		defer func() {
			s := StaticBegin(root, nil, recover())
			if live&1 != 0 {
				s.Part(func() { slot.Close() })
			}
			s.End(nil)
		}()
		for range b.N {
			_ = OwnStatic(&live, 1, &slot, benchRes{&n})
			if live&1 != 0 {
				live &^= 1
				slot.Close()
			}
		}
	})
	b.Run("loop/scope", func(b *testing.B) {
		b.ReportAllocs()
		frameNoRow(root, func(s *Scope) {
			for range b.N {
				h := s.Owner()
				_ = Owning(h, benchRes{&n}, func(v benchRes) { v.Close() })
				h.Release()
			}
		})
	})
	b.Run("loop/hand", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			func() {
				r := benchRes{&n}
				defer r.Close()
			}()
		}
	})
}
