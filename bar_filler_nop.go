package mpb

import (
	"io"

	"github.com/zhijian-pro/mpb/v7/decor"
)

// NopStyle provides BarFillerBuilder which builds NOP BarFiller.
func NopStyle() BarFillerBuilder {
	return BarFillerBuilderFunc(func() BarFiller {
		return BarFillerFunc(func(io.Writer, int, decor.Statistics) {})
	})
}
