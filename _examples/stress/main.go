package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/zhijian-pro/mpb/v7"
	"github.com/zhijian-pro/mpb/v7/decor"
)

const (
	totalBars = 32
)

func main() {
	var wg sync.WaitGroup
	// passed wg will be accounted at p.Wait() call
	p := mpb.New(mpb.WithWaitGroup(&wg))
	wg.Add(totalBars)

	for i := 0; i < totalBars; i++ {
		name := fmt.Sprintf("Bar#%02d: ", i)
		total := rand.Intn(320) + 10
		bar := p.AddBar(int64(total),
			mpb.PrependDecorators(
				decor.Name(name),
				decor.Elapsed(decor.ET_STYLE_GO, decor.WCSyncSpace),
			),
			mpb.AppendDecorators(
				decor.OnComplete(
					decor.Percentage(decor.WC{W: 5}), "done",
				),
			),
		)

		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			max := 100 * time.Millisecond
			for !bar.Completed() {
				time.Sleep(time.Duration(rng.Intn(10)+1) * max / 10)
				bar.Increment()
			}
		}()
	}
	// wait for passed wg and for all bars to complete and flush
	p.Wait()
}
