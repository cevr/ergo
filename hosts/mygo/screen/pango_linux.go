//go:build linux && (amd64 || arm64)

package screen

import (
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// MyGo calls Pango, on Linux, through purego.RegisterFunc: each call goes
// through reflect.MakeFunc, which allocates its arguments' Values, its
// result and the closures that place them, two to six allocations a call.
// Shaping one line makes some twenty such calls, so the calls allocated
// more than the layout they produce (MAPCOPY RU16, PUREGO-CALL).
//
// MyGo's Darwin engine calls its hot functions through purego.SyscallN
// for that reason (coretext_darwin.go). fastPango does the same for the
// Linux engine's calls on the shaping path, without editing MyGo: it
// rebinds those entries of MyGo's function table, pangoLib, to typed
// functions that pass their words through SyscallN from a pooled
// argument array, which allocates nothing. The table is reached by
// linkname, so pangoTable mirrors its declaration field for field;
// TestPangoTableMirrorsMyGo holds the mirror to the pinned MyGo's source.
//
// Only functions whose arguments and result are words (pointers and
// integers) are rebound: SyscallN passes no floats in their registers
// and no strings, so the others keep MyGo's binding.
//
// fastPangoCalls false keeps MyGo's own binding, the reference
// TestFastPangoShapesAsMyGo holds the fast one to.
var fastPangoCalls = true

//go:linkname pangoLib github.com/egoist/mygo/internal/text.pangoLib
var pangoLib pangoTable

// Pango and cairo structs, as MyGo declares them (pango_linux.go): the
// table's signatures name them.
type (
	gSList struct {
		data unsafe.Pointer
		next *gSList
	}
	pangoLayoutLine struct {
		layout     uintptr
		startIndex int32
		length     int32
		runs       *gSList
		flags      uint32
	}
	cairoMatrix struct{ xx, yx, xy, yy, x0, y0 float64 }
	cairoGlyph  struct {
		index uint64
		x, y  float64
	}
	cairoTextExtents struct {
		xBearing, yBearing, width, height, xAdvance, yAdvance float64
	}
	pangoRectangle struct{ x, y, width, height int32 }
	fcFontSet      struct {
		nfont, sfont int32
		fonts        *uintptr
	}
)

// pangoTable is MyGo's pangoLib's type, field for field.
type pangoTable struct {
	gObjectRef        func(obj uintptr) uintptr
	gObjectUnref      func(obj uintptr)
	gObjectGetPointer func(object uintptr, name string, value *uintptr, end uintptr)
	gFree             func(p uintptr)

	fontMapListFamilies   func(fontMap uintptr, families **uintptr, n *int32)
	fontFamilyGetName     func(family uintptr) *byte
	fontFamilyIsMonospace func(family uintptr) bool

	fontMapNew                func() uintptr
	fontMapCreateContext      func(fontMap uintptr) uintptr
	fontMapLoadFont           func(fontMap, context, desc uintptr) uintptr
	contextSetFontOptions     func(context, options uintptr)
	contextSetBaseDir         func(context uintptr, dir int32)
	contextSetRoundPositions  func(context uintptr, round int32)
	fontGetScaledFont         func(font uintptr) uintptr
	fontGetMetrics            func(font, language uintptr) uintptr
	fontDescribeAbsolute      func(font uintptr) uintptr
	fontGetHBFont             func(font uintptr) uintptr
	hbNominalGlyph            func(font uintptr, r rune, glyph *uint32) int32
	hbGlyphHAdvance           func(font uintptr, glyph uint32) int32
	metricsGetAscent          func(metrics uintptr) int32
	metricsGetUnderlinePos    func(metrics uintptr) int32
	metricsGetUnderlineThick  func(metrics uintptr) int32
	metricsGetStrikePos       func(metrics uintptr) int32
	metricsGetStrikeThick     func(metrics uintptr) int32
	fontGetGlyphExtents       func(font uintptr, glyph uint32, ink, logical *pangoRectangle)
	metricsGetDescent         func(metrics uintptr) int32
	metricsGetHeight          func(metrics uintptr) int32
	metricsUnref              func(metrics uintptr)
	descNew                   func() uintptr
	descFree                  func(desc uintptr)
	descSetFamily             func(desc uintptr, family string)
	descSetWeight             func(desc uintptr, weight int32)
	descSetStyle              func(desc uintptr, style int32)
	descSetAbsoluteSize       func(desc uintptr, size float64)
	descGetSize               func(desc uintptr) int32
	layoutNew                 func(context uintptr) uintptr
	layoutSetText             func(layout uintptr, text *byte, length int32)
	layoutSetFontDescription  func(layout, desc uintptr)
	layoutSetWidth            func(layout uintptr, width int32)
	layoutSetWrap             func(layout uintptr, wrap int32)
	layoutSetAutoDir          func(layout uintptr, auto int32)
	layoutSetTabs             func(layout, tabs uintptr)
	tabArrayNew               func(size, pixels int32) uintptr
	tabArraySetTab            func(tabs uintptr, index, align, location int32)
	tabArrayFree              func(tabs uintptr)
	layoutGetLineCount        func(layout uintptr) int32
	layoutGetLineReadonly     func(layout uintptr, line int32) *pangoLayoutLine
	layoutSetAttributes       func(layout, attrs uintptr)
	attrListNew               func() uintptr
	attrListUnref             func(list uintptr)
	attrListInsert            func(list, attr uintptr)
	attrLetterSpacingNew      func(spacing int32) uintptr
	attrFontFeaturesNew       func(features string) uintptr // Pango 1.38
	attrFamilyNew             func(family string) uintptr
	attrSizeAbsoluteNew       func(size int32) uintptr
	attrWeightNew             func(weight int32) uintptr
	attrStyleNew              func(style int32) uintptr
	attrShapeNew              func(ink, logical *pangoRectangle) uintptr
	attrLanguageNew           func(language uintptr) uintptr
	attrAllowBreaksNew        func(allow int32) uintptr // Pango 1.44
	languageFromString        func(tag string) uintptr
	languageGetDefault        func() uintptr
	fontMapConfigChanged      func(fontMap uintptr)
	cairoFontOptionsCreate    func() uintptr
	cairoFontOptionsDestroy   func(options uintptr)
	cairoFontOptionsAntialias func(options uintptr, antialias int32)
	cairoFontOptionsHintMets  func(options uintptr, hint int32)
	cairoFontOptionsHintStyle func(options uintptr, style int32)
	cairoFontOptionsSubpixel  func(options uintptr, order int32)
	cairoScaledFontFace       func(font uintptr) uintptr
	cairoFontFaceFromPattern  func(pattern uintptr) uintptr
	cairoFontFaceDestroy      func(face uintptr)
	cairoScaledFontMatrix     func(font uintptr, m *cairoMatrix)
	cairoScaledFontOptions    func(font, options uintptr)
	cairoScaledFontCreate     func(face uintptr, fontMatrix, ctm *cairoMatrix, options uintptr) uintptr
	cairoScaledFontStatus     func(font uintptr) int32
	cairoScaledFontDestroy    func(font uintptr)
	cairoScaledFontExtents    func(font uintptr, glyphs *cairoGlyph, n int32, extents *cairoTextExtents)
	cairoImageSurfaceCreate   func(format, width, height int32) uintptr
	cairoImageSurfaceData     func(surface uintptr) *byte
	cairoImageSurfaceStride   func(surface uintptr) int32
	cairoSurfaceFlush         func(surface uintptr)
	cairoSurfaceDestroy       func(surface uintptr)
	cairoCreate               func(surface uintptr) uintptr
	cairoDestroy              func(cr uintptr)
	cairoSetScaledFont        func(cr, font uintptr)
	cairoSetSourceRGBA        func(cr uintptr, r, g, b, a float64)
	cairoShowGlyphs           func(cr uintptr, glyphs *cairoGlyph, n int32)
	cairoVersion              func() int32
	fcConfigGetCurrent        func() uintptr
	fcConfigAppFontAddFile    func(config uintptr, file string) int32
	fcConfigGetFonts          func(config uintptr, set int32) *fcFontSet
	fcConfigParseFromMemory   func(config uintptr, xml string, complain int32) int32
	fcPatternGetString        func(pattern uintptr, object string, n int32, out **byte) int32
	fcPatternCreate           func() uintptr
	fcPatternDestroy          func(pattern uintptr)
	fcPatternAddString        func(pattern uintptr, object, s string) int32
	fcPatternAddInteger       func(pattern uintptr, object string, value int32) int32
	fcPatternDuplicate        func(pattern uintptr) uintptr
	fcPatternDel              func(pattern uintptr, object string) int32
	fcFontSetAdd              func(set *fcFontSet, pattern uintptr) int32
	fcPatternGetLangSet       func(pattern uintptr, object string, n int32, out *uintptr) int32
	fcLangSetHasLang          func(langs uintptr, lang string) int32
	fcConfigSubstitute        func(config, pattern uintptr, kind int32) int32
	fcDefaultSubstitute       func(pattern uintptr)
	fcFontMatch               func(config, pattern uintptr, result *int32) uintptr
}

// words is a call's argument array, pooled so that a call allocates
// nothing: SyscallN's variadic slice escapes.
type words [4]uintptr

var wordPool = sync.Pool{New: func() any { return new(words) }}

func call0(fn uintptr) uintptr {
	fastCalls.Add(1)
	r, _, _ := purego.SyscallN(fn)
	return r
}

func call(fn uintptr, n int, a, b, c, d uintptr) uintptr {
	fastCalls.Add(1)
	w := wordPool.Get().(*words)
	w[0], w[1], w[2], w[3] = a, b, c, d
	r, _, _ := purego.SyscallN(fn, w[:n]...)
	wordPool.Put(w)
	return r
}

func i32(v int32) uintptr { return uintptr(int64(v)) }

var fastPangoOnce sync.Once

// pangoMirror is the MyGo release whose pangoLib pangoTable mirrors
// (TestPangoMirrorIsThePinnedMyGo holds it to go.mod's pin). fastPango
// reads or writes the table only when the binary links that release:
// another MyGo's table may have other fields, and writes through a stale
// mirror land in the wrong entries (the v0.10.0 bump's SIGSEGV in a
// rebound call, 81cc27328). With another MyGo the window keeps MyGo's own
// binding: correct, only slower (PangoCalls counts its layouts as slow).
var pangoMirror = "v0.10.0"

// linksMirroredMyGo reports that the binary links the MyGo release
// pangoTable mirrors, by its build information; without that, or with a
// replaced module, it does not.
func linksMirroredMyGo() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, d := range bi.Deps {
		if d.Path == "github.com/egoist/mygo" {
			return d.Replace == nil && d.Version == pangoMirror
		}
	}
	return false
}

// pangoRef is MyGo's own binding of the table, as fastPango found it.
var pangoRef pangoTable

// fastCalls counts the calls through the fast binding; fastLayoutNew is
// the closure fastPango put in the table's layoutNew, by which
// pangoBound tells that MyGo has not loaded the table again since.
var (
	fastCalls     atomic.Uint64
	fastLayoutNew unsafe.Pointer
)

// funcWord is the closure a func value points at.
func funcWord(f *func(uintptr) uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(f)) }

// pangoBound reports whether MyGo's table still holds the fast binding. A
// text System made after the window's (its engine loads the table again)
// puts MyGo's binding back, and every call is slow again: the window
// counts the layouts it shapes then (PangoCalls), so that shows as a
// number. The read is of one word MyGo writes only while it loads.
func pangoBound() bool {
	return fastLayoutNew != nil && funcWord(&pangoLib.layoutNew) == fastLayoutNew
}

// PangoCalls is how many Pango calls went through the fast binding, and
// how many layouts the window shaped while MyGo's own binding was in the
// table (0 unless MyGo loaded it again, or fastPangoCalls is false).
func PangoCalls() (fast, slow uint64) { return fastCalls.Load(), slowShapes.Load() }

var slowShapes atomic.Uint64

// shaping counts a layout the window shapes, as slow when the fast
// binding is not in the table.
func shaping() {
	if !pangoBound() {
		slowShapes.Add(1)
	}
}

// fastPango rebinds the shaping path's entries of MyGo's Pango table, once
// the engine has loaded it (sys's first Metrics loads it), and before any
// window shapes text. MyGo loads the table again for each new text
// System's engine, which puts its own binding back: correct, only slower.
func fastPango(load func()) {
	fastPangoOnce.Do(func() {
		load()
		if !linksMirroredMyGo() {
			return // another MyGo: its table is not pangoTable, so not touched
		}
		pangoRef = pangoLib
		l := &pangoLib
		if !fastPangoCalls || l.layoutNew == nil {
			return // the reference, or no Pango: MyGo shows no text
		}
		open := func(name string) uintptr {
			h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				return 0
			}
			return h
		}
		gobject, pango, hb := open("libgobject-2.0.so.0"), open("libpango-1.0.so.0"), open("libharfbuzz.so.0")
		sym := func(lib uintptr, name string) uintptr {
			if lib == 0 {
				return 0
			}
			s, err := purego.Dlsym(lib, name)
			if err != nil {
				return 0
			}
			return s
		}
		// Each binding only when its symbol resolves; MyGo's stays otherwise.
		if f := sym(gobject, "g_object_unref"); f != 0 {
			l.gObjectUnref = func(obj uintptr) { call(f, 1, obj, 0, 0, 0) }
		}
		if f := sym(pango, "pango_context_set_base_dir"); f != 0 {
			l.contextSetBaseDir = func(context uintptr, dir int32) { call(f, 2, context, i32(dir), 0, 0) }
		}
		if f := sym(pango, "pango_layout_new"); f != 0 {
			l.layoutNew = func(context uintptr) uintptr { return call(f, 1, context, 0, 0, 0) }
			fastLayoutNew = funcWord(&l.layoutNew)
		}
		if f := sym(pango, "pango_layout_set_text"); f != 0 {
			l.layoutSetText = func(layout uintptr, text *byte, length int32) {
				var pin runtime.Pinner
				if text != nil {
					pin.Pin(text)
				}
				call(f, 3, layout, uintptr(unsafe.Pointer(text)), i32(length), 0)
				pin.Unpin()
			}
		}
		if f := sym(pango, "pango_layout_set_font_description"); f != 0 {
			l.layoutSetFontDescription = func(layout, desc uintptr) { call(f, 2, layout, desc, 0, 0) }
		}
		if f := sym(pango, "pango_layout_set_width"); f != 0 {
			l.layoutSetWidth = func(layout uintptr, width int32) { call(f, 2, layout, i32(width), 0, 0) }
		}
		if f := sym(pango, "pango_layout_set_wrap"); f != 0 {
			l.layoutSetWrap = func(layout uintptr, wrap int32) { call(f, 2, layout, i32(wrap), 0, 0) }
		}
		if f := sym(pango, "pango_layout_set_auto_dir"); f != 0 {
			l.layoutSetAutoDir = func(layout uintptr, auto int32) { call(f, 2, layout, i32(auto), 0, 0) }
		}
		if f := sym(pango, "pango_layout_set_tabs"); f != 0 {
			l.layoutSetTabs = func(layout, tabs uintptr) { call(f, 2, layout, tabs, 0, 0) }
		}
		if f := sym(pango, "pango_tab_array_new"); f != 0 {
			l.tabArrayNew = func(size, pixels int32) uintptr { return call(f, 2, i32(size), i32(pixels), 0, 0) }
		}
		if f := sym(pango, "pango_tab_array_set_tab"); f != 0 {
			l.tabArraySetTab = func(tabs uintptr, index, align, location int32) {
				call(f, 4, tabs, i32(index), i32(align), i32(location))
			}
		}
		if f := sym(pango, "pango_tab_array_free"); f != 0 {
			l.tabArrayFree = func(tabs uintptr) { call(f, 1, tabs, 0, 0, 0) }
		}
		if f := sym(pango, "pango_layout_get_line_count"); f != 0 {
			l.layoutGetLineCount = func(layout uintptr) int32 { return int32(call(f, 1, layout, 0, 0, 0)) }
		}
		if f := sym(pango, "pango_layout_get_line_readonly"); f != 0 {
			l.layoutGetLineReadonly = func(layout uintptr, line int32) *pangoLayoutLine {
				r := call(f, 2, layout, i32(line), 0, 0)
				return *(**pangoLayoutLine)(unsafe.Pointer(&r)) // C memory Pango keeps while layout lives
			}
		}
		if f := sym(pango, "pango_layout_set_attributes"); f != 0 {
			l.layoutSetAttributes = func(layout, attrs uintptr) { call(f, 2, layout, attrs, 0, 0) }
		}
		if f := sym(pango, "pango_attr_list_new"); f != 0 {
			l.attrListNew = func() uintptr { return call0(f) }
		}
		if f := sym(pango, "pango_attr_list_unref"); f != 0 {
			l.attrListUnref = func(list uintptr) { call(f, 1, list, 0, 0, 0) }
		}
		if f := sym(pango, "pango_attr_list_insert"); f != 0 {
			l.attrListInsert = func(list, attr uintptr) { call(f, 2, list, attr, 0, 0) }
		}
		if f := sym(pango, "pango_attr_letter_spacing_new"); f != 0 {
			l.attrLetterSpacingNew = func(spacing int32) uintptr { return call(f, 1, i32(spacing), 0, 0, 0) }
		}
		if f := sym(pango, "pango_attr_size_new_absolute"); f != 0 {
			l.attrSizeAbsoluteNew = func(size int32) uintptr { return call(f, 1, i32(size), 0, 0, 0) }
		}
		if f := sym(pango, "pango_attr_weight_new"); f != 0 {
			l.attrWeightNew = func(weight int32) uintptr { return call(f, 1, i32(weight), 0, 0, 0) }
		}
		if f := sym(pango, "pango_attr_style_new"); f != 0 {
			l.attrStyleNew = func(style int32) uintptr { return call(f, 1, i32(style), 0, 0, 0) }
		}
		if f := sym(pango, "pango_attr_language_new"); f != 0 {
			l.attrLanguageNew = func(language uintptr) uintptr { return call(f, 1, language, 0, 0, 0) }
		}
		if f := sym(pango, "pango_language_get_default"); f != 0 {
			l.languageGetDefault = func() uintptr { return call0(f) }
		}
		if l.hbNominalGlyph != nil {
			if f := sym(hb, "hb_font_get_nominal_glyph"); f != 0 {
				l.hbNominalGlyph = func(font uintptr, r rune, glyph *uint32) int32 {
					var pin runtime.Pinner
					if glyph != nil {
						pin.Pin(glyph)
					}
					n := int32(call(f, 3, font, uintptr(uint32(r)), uintptr(unsafe.Pointer(glyph)), 0))
					pin.Unpin()
					return n
				}
			}
		}
		if l.hbGlyphHAdvance != nil {
			if f := sym(hb, "hb_font_get_glyph_h_advance"); f != 0 {
				l.hbGlyphHAdvance = func(font uintptr, glyph uint32) int32 {
					return int32(call(f, 2, font, uintptr(glyph), 0, 0))
				}
			}
		}
	})
}
