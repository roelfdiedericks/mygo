package terminal

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

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
// code; a full-screen program's screen has none. AtBottom follows them.
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
	if !term.AtBottom() {
		t.Error("not at the bottom after output")
	}
	if !term.ScrollRows(-5) || offset() != bottom-5 {
		t.Errorf("5 rows up: offset %d, bottom %d", offset(), bottom)
	}
	if term.AtBottom() {
		t.Error("at the bottom 5 rows up")
	}
	if !term.ScrollToTop() || offset() != 0 {
		t.Errorf("to the top: offset %d", offset())
	}
	if !term.ScrollToBottom() || offset() != bottom {
		t.Errorf("to the bottom: offset %d, want %d", offset(), bottom)
	}
	if !term.AtBottom() {
		t.Error("not at the bottom after ScrollToBottom")
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
	if !term.AtBottom() {
		t.Error("the painted view is not at the bottom")
	}
	tt.Wheel(100, 100, 0, -40) // a notch up: three rows
	tt.Frame()
	if got := offset(); got != before-3 {
		t.Errorf("offset after a notch up over the app's element: %d, was %d", got, before)
	}
	if term.AtBottom() {
		t.Error("the painted view is at the bottom a notch up")
	}
	if term.Scroll(ui.InputEvent{Kind: ui.InputPointerDown}) {
		t.Error("a pointer event was taken")
	}
}

// The pointer over an element the app draws over the view is over the
// view for its scroll bar, which shows there as over the text, and goes
// with the pointer off the view.
func TestScrollbarUnderAppElement(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), Theme: LightTheme()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Children(func() {
			View(c, term).Fill()
			ui.Box(c).Absolute().Left(0).Top(0).Bottom(0).Right(40).Background(ui.RGB(1, 2, 3))
		})
	}, 400, 200)
	_, rows := term.Size()
	term.Feed([]byte(strings.Repeat("line\r\n", rows+20)))
	// shows reports whether the bar paints at (394, 188), by the pixel off
	// it, both clear of the element.
	shows := func() bool {
		img := tt.Image()
		s := img.Bounds().Dx() / 400
		return img.RGBAAt(394*s, 188*s) != img.RGBAAt(370*s, 188*s)
	}
	tt.Move(100, 100)
	tt.Frame()
	if !shows() {
		t.Error("the bar doesn't show with the pointer over the app's element")
	}
	tt.Move(-20, -20)
	tt.Frame()
	if shows() {
		t.Error("the bar shows with the pointer off the view")
	}
}

// glideTerm returns a terminal in a view with a scrollback, the app's
// anchor and a blue image on one row of the screen, and a red last row;
// with the anchor's row.
func glideTerm(t *testing.T, smooth bool) (*Terminal, *ui.Tester, int) {
	t.Helper()
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), Theme: LightTheme(), SmoothScroll: smooth, AppImages: struct{ First, Last uint32 }{1000, 1999}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 400, 200)
	tt.Frame()
	_, rows := term.Size()
	v := term.v
	term.Feed([]byte(strings.Repeat("line\r\n", rows+20) +
		"\x1b[11G" + kittyImage(7, v.cellW, v.cellH, ui.RGB(0, 0, 255), ",C=1") + "\r" + appAnchor(1000, 3, 1) +
		strings.Repeat("\r\n", 5) + "\x1b[41m" + strings.Repeat(" ", 20) + "\x1b[0m"))
	tt.Frame()
	ps := term.AppPlacements()
	if len(ps) != 1 || ps[0].Row < 0 || ps[0].Row+3 >= rows-1 {
		t.Fatalf("rows %d, placements %+v", rows, ps)
	}
	return term, tt, ps[0].Row
}

// placedY returns where the app's placement shows, in DIPs.
func placedY(t *testing.T, term *Terminal) float32 {
	t.Helper()
	ps := term.AppPlacements()
	if len(ps) != 1 {
		t.Fatalf("placements: %+v", ps)
	}
	return ps[0].Rect.Y
}

// rowY returns where row y of the viewport is at rest, in DIPs.
func rowY(term *Terminal, y int) float32 {
	v := term.v
	return (float32(v.oy) + float32(y*v.cellH)) / v.scale
}

// pixel returns the color the view shows at cell col, row at rest.
func pixel(tt *ui.Tester, term *Terminal, col, row int) ui.Color {
	x, y := cellCenter(term, col, row)
	img := tt.Image()
	s := float32(img.Bounds().Dx()) / 400
	c := img.RGBAAt(int(x*s), int(y*s))
	return ui.RGB(c.R, c.G, c.B)
}

func near(a, b float32) bool { return a-b < 0.01 && b-a < 0.01 }

// With SmoothScroll, a notch of the wheel scrolls the scrollback at once,
// and the view glides there: the rows, the images, the rows that left the
// viewport and the app's placements show where they were, then slide into
// place. AtBottom waits for the glide to come to rest at the end.
func TestSmoothScroll(t *testing.T) {
	term, tt, row := glideTerm(t, true)
	_, rows := term.Size()
	offset := func() uint64 {
		term.mu.Lock()
		defer term.mu.Unlock()
		return term.term.Scrollbar().Offset
	}
	bottom := offset()
	blue := ui.RGB(0, 0, 255)
	red := pixel(tt, term, 5, rows-1) // the theme's red, behind spaces
	if pixel(tt, term, 10, row) != blue || red == LightTheme().Background {
		t.Fatalf("before: image %v, last row %v", pixel(tt, term, 10, row), red)
	}
	at := placedY(t, term)
	tt.Wheel(100, 100, 0, -40) // a notch up: three rows
	tt.Frame()
	if got := offset(); got != bottom-3 {
		t.Fatalf("offset after a notch up: %d, was %d", got, bottom)
	}
	if y := placedY(t, term); !near(y, at) {
		t.Errorf("the placement jumped with the notch: at %v, was at %v", y, at)
	}
	if pixel(tt, term, 10, row) != blue || pixel(tt, term, 10, row+3) == blue {
		t.Error("the image jumped with the notch")
	}
	// The last row left the viewport by its bottom; the gap shows it.
	if c := pixel(tt, term, 5, rows-1); c != red {
		t.Errorf("the gap shows %v, not the row that left", c)
	}
	if term.AtBottom() {
		t.Error("at the bottom a notch up")
	}
	tt.Advance(50 * time.Millisecond)
	if y := placedY(t, term); y <= at || y >= rowY(term, row+3) {
		t.Errorf("halfway, the placement is at %v, from %v to %v", y, at, rowY(term, row+3))
	}
	tt.Advance(200 * time.Millisecond)
	if y := placedY(t, term); !near(y, rowY(term, row+3)) {
		t.Errorf("at rest, the placement is at %v, not %v", y, rowY(term, row+3))
	}
	if pixel(tt, term, 10, row+3) != blue || pixel(tt, term, 5, rows-1) == red {
		t.Error("at rest, the image or the rows are not in place")
	}
	// Back down to the end: not there until the glide is.
	tt.Wheel(100, 100, 0, 40)
	tt.Frame()
	if offset() != bottom {
		t.Fatalf("offset after a notch down: %d, want %d", offset(), bottom)
	}
	if term.AtBottom() {
		t.Error("at the bottom as the glide begins")
	}
	tt.Advance(200 * time.Millisecond)
	if !term.AtBottom() || !near(placedY(t, term), at) {
		t.Errorf("at rest at the end: AtBottom %v, placement at %v, want %v", term.AtBottom(), placedY(t, term), at)
	}
}

// Without SmoothScroll, as by default, and with SmoothScroll while the
// desktop asks for less motion, a notch shows its rows in place at once.
func TestSmoothScrollOff(t *testing.T) {
	for _, still := range []bool{false, true} {
		term, tt, row := glideTerm(t, still)
		if still {
			tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
		}
		tt.Wheel(100, 100, 0, -40)
		tt.Frame()
		if y := placedY(t, term); !near(y, rowY(term, row+3)) {
			t.Errorf("smooth %v: after a notch, the placement is at %v, not %v", still, y, rowY(term, row+3))
		}
	}
}

// A move of the viewport the wheel did not make snaps a glide into
// place: output following the end, and ScrollToBottom.
func TestSmoothScrollSnaps(t *testing.T) {
	term, tt, row := glideTerm(t, true)
	tt.Wheel(100, 100, 0, -40)
	tt.Advance(200 * time.Millisecond)
	tt.Wheel(100, 100, 0, 40)
	tt.Frame()
	if term.AtBottom() {
		t.Fatal("at the bottom as the glide begins")
	}
	term.Feed([]byte("\r\nmore\r\nmore"))
	tt.Frame()
	if !term.AtBottom() || !near(placedY(t, term), rowY(term, row-2)) {
		t.Errorf("after output: AtBottom %v, placement at %v, want %v", term.AtBottom(), placedY(t, term), rowY(term, row-2))
	}
	tt.Wheel(100, 100, 0, -40)
	tt.Frame()
	term.ScrollToBottom()
	tt.Frame()
	if !term.AtBottom() || !near(placedY(t, term), rowY(term, row-2)) {
		t.Errorf("after ScrollToBottom: AtBottom %v, placement at %v, want %v", term.AtBottom(), placedY(t, term), rowY(term, row-2))
	}
}
