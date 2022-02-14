package main

import (
	"math/rand"
	"time"

	"github.com/zhijian-pro/mpb/v7"
	"github.com/zhijian-pro/mpb/v7/decor"
)

func main() {
	// initialize progress container, with custom width
	p := mpb.New(mpb.WithWidth(80))

	total := 100
	name := "Single Bar:"
	bar := p.New(int64(total),
		mpb.BarStyle().TipOnComplete(">"),
		mpb.PrependDecorators(decor.Name(name)),
		mpb.AppendDecorators(decor.Percentage()),
	)
	// simulating some work
	max := 100 * time.Millisecond
	for i := 0; i < total; i++ {
		time.Sleep(time.Duration(rand.Intn(10)+1) * max / 10)
		bar.Increment()
	}
	// wait for our bar to complete and flush
	p.Wait()
}
