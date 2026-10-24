package terminal

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

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
