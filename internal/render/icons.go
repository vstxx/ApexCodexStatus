package render

// Tiny hand-drawn 7x7 (or narrower) status icons, string-art source parsed at
// init. Icons are secondary accents; the state word next to them carries the
// meaning.

// Icon is a small left-aligned bitmap.
type Icon struct {
	W    int
	H    int
	Rows [7]byte
}

var icons = map[string]Icon{}

var iconArt = map[string][7]string{
	"exec": { // >_
		"#.....",
		".#....",
		"..#...",
		"...#..",
		"..#...",
		".#....",
		"..###.",
	},
	"edit": { // diagonal pencil
		"......#",
		".....##",
		"....##.",
		"...##..",
		"..##...",
		".##....",
		"##.....",
	},
	"test": { // vial
		"..###..",
		"...#...",
		"...#...",
		"...#...",
		"..###..",
		".#####.",
		"..###..",
	},
	"read": { // page with lines
		".#####.",
		".#...#.",
		".#.#.#.",
		".#...#.",
		".#.#.#.",
		".#...#.",
		".#####.",
	},
	"search": { // magnifier
		".###...",
		"#...#..",
		"#...#..",
		"#...#..",
		".###...",
		"...##..",
		"....##.",
	},
	"check": { // ✓
		".......",
		"......#",
		".....##",
		"#...##.",
		".####..",
		"..##...",
		".......",
	},
	"cross": { // ×
		"#.....#",
		".#...#.",
		"..#.#..",
		"...#...",
		"..#.#..",
		".#...#.",
		"#.....#",
	},
	"bell": { // attention
		"..###..",
		".#####.",
		"#######",
		"#######",
		".......",
		"..###..",
		".......",
	},
	"gear": { // working: coarse cog
		".#.#.#.",
		".######",
		"##...##",
		".#...#.",
		"##...##",
		".######",
		".#.#.#.",
	},
}

func init() {
	for name, rows := range iconArt {
		var ic Icon
		ic.H = len(rows)
		ic.W = 0
		for _, s := range rows {
			if len(s) > ic.W {
				ic.W = len(s)
			}
		}
		for i, s := range rows {
			var b byte
			for j := 0; j < len(s) && j < 8; j++ {
				if s[j] == '1' || s[j] == '#' {
					b |= 0x80 >> j
				}
			}
			ic.Rows[i] = b
		}
		icons[name] = ic
	}
}

// IconByName returns the named icon, if it exists.
func IconByName(name string) (Icon, bool) {
	ic, ok := icons[name]
	return ic, ok
}

// DrawIcon draws an icon with its left edge at (x,y).
func (f *FB) DrawIcon(x, y int, ic Icon) {
	f.Blit(x, y, ic.Rows[:], ic.W, ic.H)
}

// IconWidth returns the horizontal footprint of an icon plus spacing.
func IconWidth(ic Icon) int {
	return ic.W + 2
}
