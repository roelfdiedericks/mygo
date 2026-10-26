package terminal

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/terminal/internal/vt"
	"github.com/egoist/mygo/ui"
)

// appAnchor returns what places a 1×1 image of id over cols×rows cells at
// the cursor, as an app does for something it draws itself.
func appAnchor(id uint32, cols, rows int) string {
	px := base64.StdEncoding.EncodeToString([]byte{255, 0, 0, 255})
	return fmt.Sprintf("\x1b_Ga=t,f=32,s=1,v=1,i=%d,q=2;%s\x1b\\\x1b_Ga=p,i=%d,p=1,c=%d,r=%d,q=2\x1b\\", id, px, id, cols, rows)
}

// An image of the app's ids is not drawn; its placement is reported with
// the cells it takes, and scrolls with the rows. Another image draws as
// before, and CellRect gives a cell's rectangle.
func TestAppPlacements(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), Theme: LightTheme(), AppImages: struct{ First, Last uint32 }{1000, 1999}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 400, 200)
	tt.Frame()
	if ps := term.AppPlacements(); len(ps) != 0 {
		t.Errorf("placements before any: %+v", ps)
	}
	bg := LightTheme().Background
	// Row 1: the app's anchor over 3×2 cells, which moves the cursor below
	// it, then a row of padding; row 5: an ordinary image, 1 cell.
	term.Feed([]byte("\r\n" + appAnchor(1000, 3, 2) + "\r\n\r\n\r\n" + kittyImage(7, term.v.cellW, term.v.cellH, ui.RGB(0, 0, 255), ",C=1")))
	tt.Frame()
	ps := term.AppPlacements()
	if len(ps) != 1 {
		t.Fatalf("placements: %+v", ps)
	}
	p := ps[0]
	cell, ok := term.CellRect(0, 1)
	if !ok {
		t.Fatal("no cell rect")
	}
	if p.Image != 1000 || p.ID != 1 || p.Col != 0 || p.Row != 1 || p.Cols != 3 || p.Rows != 2 || !p.Visible ||
		p.Rect.X != cell.X || p.Rect.Y != cell.Y || p.Rect.W != cell.W*3 || p.Rect.H != cell.H*2 {
		t.Errorf("placement %+v, cell %+v", p, cell)
	}
	// Not drawn: the anchor's cell shows the background; the image shows.
	x, y := cellCenter(term, 0, 1)
	if c := tt.Image().RGBAAt(int(x*term.v.scale), int(y*term.v.scale)); ui.RGB(c.R, c.G, c.B) != bg {
		t.Errorf("the app's image was drawn: %v", c)
	}
	x, y = cellCenter(term, 0, 5)
	if c := tt.Image().RGBAAt(int(x*term.v.scale), int(y*term.v.scale)); ui.RGB(c.R, c.G, c.B) != ui.RGB(0, 0, 255) {
		t.Errorf("the ordinary image was not drawn: %v", c)
	}
	// Scrolled off above, still reported with its row.
	_, rows := term.Size()
	term.Feed([]byte(strings.Repeat("\r\n", rows)))
	tt.Frame()
	ps = term.AppPlacements()
	if len(ps) != 1 || ps[0].Visible || ps[0].Row >= 0 {
		t.Errorf("after scrolling: %+v", ps)
	}
}

// A Mirror carries the app's placements to the other terminal, where the
// view reports them too.
func TestMirrorAppPlacements(t *testing.T) {
	loadLib(t)
	src, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := New(Options{Conn: newPipe(), Theme: LightTheme(), AppImages: struct{ First, Last uint32 }{1000, 1999}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := src.NewMirror()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(); src.Close(); dst.Close() })
	tt := ui.NewTester(func(c *ui.Context) { View(c, dst).Fill() }, 400, 200)
	tt.Frame()
	cols, rows := dst.Size()
	src.ResizePixels(cols, rows, dst.v.cellW, dst.v.cellH)
	f, _ := m.Frame()
	dst.Feed(f)
	src.Feed([]byte("\r\n" + appAnchor(1001, 4, 3) + "\r\n\r\n\r\n"))
	f, _ = m.Frame()
	dst.Feed(f)
	tt.Frame()
	ps := dst.AppPlacements()
	if len(ps) != 1 || ps[0].Image != 1001 || ps[0].Row != 1 || ps[0].Cols != 4 || ps[0].Rows != 3 {
		t.Errorf("mirrored placements: %+v", ps)
	}
}

// A placement whose row went to the scrollback is placed in the other
// terminal as its row passes through the screen: by a Mirror that starts
// over, and by one whose frames missed the rows going by.
func TestMirrorScrollbackPlacements(t *testing.T) {
	loadLib(t)
	src, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	src.ResizePixels(40, 10, 8, 16)
	src.Feed([]byte("one\r\n" + appAnchor(1002, 40, 3) + "\r\n\r\n\r\n"))
	// Twelve more rows: the anchor's row 1 is now 3 rows into the
	// scrollback.
	src.Feed([]byte(strings.Repeat("x\r\n", 12)))
	mirrorTo := func(m *Mirror) *Terminal {
		dst, _ := New(Options{Conn: newPipe(), AppImages: struct{ First, Last uint32 }{1000, 1999}})
		dst.ResizePixels(40, 10, 8, 16)
		f, _ := m.Frame()
		dst.Feed(f)
		return dst
	}
	placements := func(dst *Terminal) string {
		dst.mu.Lock()
		defer dst.mu.Unlock()
		var out []string
		dst.term.Placements(func(p vt.Placement) {
			out = append(out, fmt.Sprintf("img=%d row=%d col=%d cols=%d rows=%d", p.ImageID, p.Row, p.Col, p.ParamCols, p.ParamRows))
		})
		return strings.Join(out, "; ")
	}
	// Starting over: the first frame draws the scrollback through the
	// screen, the anchor on its row.
	m, _ := src.NewMirror()
	defer m.Close()
	dst := mirrorTo(m)
	defer dst.Close()
	if got, want := placements(dst), placements(src); got != want || !strings.Contains(got, "img=1002 row=-") {
		t.Errorf("after a restart: %q, the source has %q", got, want)
	}
	// Frames missed the rows going by: a placement made, then scrolled
	// off between two frames, is placed as its row passes.
	m2, _ := src.NewMirror()
	defer m2.Close()
	dst2 := mirrorTo(m2)
	defer dst2.Close()
	src.Feed([]byte(appAnchor(1003, 40, 2) + "\r\n\r\n" + strings.Repeat("y\r\n", 11)))
	f, _ := m2.Frame()
	dst2.Feed(f)
	if got, want := placements(dst2), placements(src); got != want || !strings.Contains(got, "img=1003 row=-") {
		t.Errorf("after a fast scroll: %q, the source has %q", got, want)
	}
}

// A terminal no view shows reports its app's placements in cells, and
// where the cursor is, as a server reads them.
func TestAppPlacementsHeadless(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), AppImages: struct{ First, Last uint32 }{1000, 1999}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.ResizePixels(40, 10, 8, 16)
	term.Feed([]byte("one\r\n" + appAnchor(1000, 1, 1) + "\r\n\r\n\r\n" + appAnchor(7, 1, 1)))
	ps := term.AppPlacements()
	if len(ps) != 1 || ps[0].Image != 1000 || ps[0].Row != 1 || ps[0].Col != 0 || ps[0].Rect != (ui.Rect{}) {
		t.Errorf("placements: %+v", ps)
	}
	// The ordinary image, placed without C=1, moved the cursor past it.
	if col, row := term.Cursor(); col != 1 || row != 4 {
		t.Errorf("cursor at %d,%d, want 1,4", col, row)
	}
}

// A placement whose row left the scrollback with the oldest lines is
// gone: the library keeps it, at no position, until a placement is next
// added, and it is not reported at the top of the screen in between.
func TestAppPlacementsPruned(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), ScrollbackLines: 100, AppImages: struct{ First, Last uint32 }{1000, 1999}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.ResizePixels(40, 10, 8, 16)
	term.Feed([]byte(appAnchor(1000, 1, 1) + "\r\n" + strings.Repeat("line\r\n", 50)))
	if ps := term.AppPlacements(); len(ps) != 1 || ps[0].Row >= 0 {
		t.Fatalf("in the scrollback: %+v", ps)
	}
	term.Feed([]byte(strings.Repeat("line\r\n", 5000)))
	if ps := term.AppPlacements(); len(ps) != 0 {
		t.Errorf("after its row left the scrollback: %+v", ps)
	}
}

// ScrollRows, ScrollToTop and ScrollToBottom scroll the scrollback from
// code; a full-screen program's screen has none.
func TestScrollRows(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.ResizePixels(40, 10, 8, 16)
	term.Feed([]byte(strings.Repeat("line\r\n", 100)))
	offset := func() uint64 {
		term.mu.Lock()
		defer term.mu.Unlock()
		return term.term.Scrollbar().Offset
	}
	bottom := offset()
	if !term.ScrollRows(-5) || offset() != bottom-5 {
		t.Errorf("5 rows up: offset %d, bottom %d", offset(), bottom)
	}
	if !term.ScrollToTop() || offset() != 0 {
		t.Errorf("to the top: offset %d", offset())
	}
	if !term.ScrollToBottom() || offset() != bottom {
		t.Errorf("to the bottom: offset %d, want %d", offset(), bottom)
	}
	term.Feed([]byte("\x1b[?1049h"))
	if term.ScrollRows(-5) || term.ScrollToTop() {
		t.Error("scrolled a full-screen program's screen")
	}
}

// An element the app draws over the view hands the wheel to the
// terminal with Scroll, which scrolls the scrollback as the view does.
func TestScrollFromAppElement(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), Theme: LightTheme()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Children(func() {
			View(c, term).Fill()
			ui.Box(c).Absolute().Fill().Background(ui.RGB(1, 2, 3)).HandleInput(func(ev ui.InputEvent) bool {
				return term.Scroll(ev)
			})
		})
	}, 400, 200)
	tt.Frame()
	offset := func() uint64 {
		term.mu.Lock()
		defer term.mu.Unlock()
		return term.term.Scrollbar().Offset
	}
	_, rows := term.Size()
	term.Feed([]byte(strings.Repeat("line\r\n", rows+20)))
	tt.Frame()
	before := offset()
	if before == 0 {
		t.Fatal("no scrollback to scroll")
	}
	tt.Wheel(100, 100, 0, -40) // a notch up: three rows
	tt.Frame()
	if got := offset(); got != before-3 {
		t.Errorf("offset after a notch up over the app's element: %d, was %d", got, before)
	}
	if term.Scroll(ui.InputEvent{Kind: ui.InputPointerDown}) {
		t.Error("a pointer event was taken")
	}
}
