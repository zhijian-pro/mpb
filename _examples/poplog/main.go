package main

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/zhijian-pro/mpb/v7"
	"github.com/zhijian-pro/mpb/v7/decor"
)

func main() {
	p := mpb.New(mpb.PopCompletedMode())

	total, numBars := 100, 4
	for i := 0; i < numBars; i++ {
		name := fmt.Sprintf("Bar#%d:", i)
		bar := p.AddBar(int64(total),
			mpb.BarFillerOnComplete(fmt.Sprintf("%s has been completed", name)),
			mpb.BarFillerTrim(),
			mpb.PrependDecorators(
				decor.OnComplete(decor.Name(name), ""),
				decor.OnComplete(decor.NewPercentage(" % d "), ""),
			),
			mpb.AppendDecorators(
				decor.OnComplete(decor.Name(" "), ""),
				decor.OnComplete(decor.EwmaETA(decor.ET_STYLE_GO, 60), ""),
			),
		)
		// simulating some work
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		max := 100 * time.Millisecond
		for i := 0; i < total; i++ {
			// start variable is solely for EWMA calculation
			// EWMA's unit of measure is an iteration's duration
			start := time.Now()
			time.Sleep(time.Duration(rng.Intn(10)+1) * max / 10)
			bar.Increment()
			// we need to call DecoratorEwmaUpdate to fulfill ewma decorator's contract
			bar.DecoratorEwmaUpdate(time.Since(start))
		}
	}

	p.Wait()
}
