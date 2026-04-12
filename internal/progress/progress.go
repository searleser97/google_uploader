package progress

import (
	"fmt"
	"strings"
)

type Bar struct {
	Width int
}

func NewBar(width int) *Bar {
	if width <= 0 {
		width = 30
	}
	return &Bar{Width: width}
}

func (b *Bar) Print(current, total int) {
	if total <= 0 {
		return
	}
	pct := current * 100 / total
	filled := current * b.Width / total
	bar := strings.Repeat("█", filled) + strings.Repeat("░", b.Width-filled)
	fmt.Printf("\r  [%s] %d%% (%d/%d)", bar, pct, current, total)
}

func (b *Bar) Finish() {
	fmt.Println()
}
