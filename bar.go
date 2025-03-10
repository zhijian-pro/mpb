package mpb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/acarl005/stripansi"
	"github.com/mattn/go-runewidth"
	"github.com/vbauerster/mpb/v7/decor"
)

// Bar represents a progress bar.
type Bar struct {
	index          int // used by heap
	priority       int // used by heap
	hasEwma        bool
	frameCh        chan *frame
	operateState   chan func(*bState)
	done           chan struct{}
	container      *Progress
	bs             *bState
	cancel         func()
	recoveredPanic interface{}
}

type extenderFunc func(in io.Reader, reqWidth int, st decor.Statistics) (out io.Reader, lines int)

// bState is actual bar's state.
type bState struct {
	id                int
	priority          int
	reqWidth          int
	total             int64
	current           int64
	refill            int64
	lastIncrement     int64
	trimSpace         bool
	completed         bool
	aborted           bool
	triggerComplete   bool
	dropOnComplete    bool
	noPop             bool
	aDecorators       []decor.Decorator
	pDecorators       []decor.Decorator
	averageDecorators []decor.AverageDecorator
	ewmaDecorators    []decor.EwmaDecorator
	shutdownListeners []decor.ShutdownListener
	buffers           [3]*bytes.Buffer
	filler            BarFiller
	middleware        func(BarFiller) BarFiller
	extender          extenderFunc
	debugOut          io.Writer

	afterBar *Bar // key for (*pState).queueBars
	sync     bool
}

type frame struct {
	reader   io.Reader
	lines    int
	shutdown bool
}

func newBar(container *Progress, bs *bState) *Bar {
	ctx, cancel := context.WithCancel(container.ctx)

	bar := &Bar{
		priority:     bs.priority,
		hasEwma:      len(bs.ewmaDecorators) != 0,
		frameCh:      make(chan *frame, 1),
		operateState: make(chan func(*bState)),
		done:         make(chan struct{}),
		container:    container,
		cancel:       cancel,
	}

	go bar.serve(ctx, bs)
	return bar
}

// ProxyReader wraps r with metrics required for progress tracking.
// Panics if r is nil.
func (b *Bar) ProxyReader(r io.Reader) io.ReadCloser {
	if r == nil {
		panic("expected non nil io.Reader")
	}
	return b.newProxyReader(r)
}

// ID returs id of the bar.
func (b *Bar) ID() int {
	result := make(chan int)
	select {
	case b.operateState <- func(s *bState) { result <- s.id }:
		return <-result
	case <-b.done:
		return b.bs.id
	}
}

// Current returns bar's current value, in other words sum of all increments.
func (b *Bar) Current() int64 {
	result := make(chan int64)
	select {
	case b.operateState <- func(s *bState) { result <- s.current }:
		return <-result
	case <-b.done:
		return b.bs.current
	}
}

// SetRefill sets refill flag with specified amount.
// The underlying BarFiller will change its visual representation, to
// indicate refill event. Refill event may be referred to some retry
// operation for example.
func (b *Bar) SetRefill(amount int64) {
	select {
	case b.operateState <- func(s *bState) {
		s.refill = amount
	}:
	case <-b.done:
	}
}

// TraverseDecorators traverses all available decorators and calls cb func on each.
func (b *Bar) TraverseDecorators(cb func(decor.Decorator)) {
	done := make(chan struct{})
	select {
	case b.operateState <- func(s *bState) {
		for _, decorators := range [...][]decor.Decorator{
			s.pDecorators,
			s.aDecorators,
		} {
			for _, d := range decorators {
				cb(extractBaseDecorator(d))
			}
		}
		close(done)
	}:
		<-done
	case <-b.done:
	}
}

// SetTotal sets total dynamically.
// If total is negative it takes progress' current value.
func (b *Bar) SetTotal(total int64, triggerComplete bool) {
	select {
	case b.operateState <- func(s *bState) {
		s.triggerComplete = triggerComplete
		if total < 0 {
			s.total = s.current
		} else {
			s.total = total
		}
		if s.triggerComplete && !s.completed && !s.aborted {
			s.current = s.total
			s.completed = true
			go b.forceRefresh()
		}
	}:
		if triggerComplete {
			<-b.done
		}
	case <-b.done:
	}
}

// SetCurrent sets progress' current to an arbitrary value.
// Setting a negative value will cause a panic.
func (b *Bar) SetCurrent(current int64) {
	select {
	case b.operateState <- func(s *bState) {
		s.lastIncrement = current - s.current
		s.current = current
		if s.triggerComplete && s.current >= s.total {
			s.current = s.total
			s.completed = true
			go b.forceRefresh()
		}
	}:
	case <-b.done:
	}
}

// Increment is a shorthand for b.IncrInt64(1).
func (b *Bar) Increment() {
	b.IncrInt64(1)
}

// IncrBy is a shorthand for b.IncrInt64(int64(n)).
func (b *Bar) IncrBy(n int) {
	b.IncrInt64(int64(n))
}

// IncrInt64 increments progress by amount of n.
func (b *Bar) IncrInt64(n int64) {
	if n <= 0 {
		return
	}
	select {
	case b.operateState <- func(s *bState) {
		s.lastIncrement = n
		s.current += n
		if s.triggerComplete && s.current >= s.total {
			s.current = s.total
			s.completed = true
			go b.forceRefresh()
		}
	}:
	case <-b.done:
	}
}

// DecoratorEwmaUpdate updates all EWMA based decorators. Should be
// called on each iteration, because EWMA's unit of measure is an
// iteration's duration. Panics if called before *Bar.Incr... family
// methods.
func (b *Bar) DecoratorEwmaUpdate(dur time.Duration) {
	select {
	case b.operateState <- func(s *bState) {
		if s.lastIncrement > 0 {
			s.decoratorEwmaUpdate(dur)
			s.lastIncrement = 0
		} else {
			panic("increment required before ewma iteration update")
		}
	}:
	case <-b.done:
		if b.bs.lastIncrement > 0 {
			b.bs.decoratorEwmaUpdate(dur)
			b.bs.lastIncrement = 0
		}
	}
}

// DecoratorAverageAdjust adjusts all average based decorators. Call
// if you need to adjust start time of all average based decorators
// or after progress resume.
func (b *Bar) DecoratorAverageAdjust(start time.Time) {
	select {
	case b.operateState <- func(s *bState) {
		s.decoratorAverageAdjust(start)
	}:
	case <-b.done:
	}
}

// SetPriority changes bar's order among multiple bars. Zero is highest
// priority, i.e. bar will be on top. If you don't need to set priority
// dynamically, better use BarPriority option.
func (b *Bar) SetPriority(priority int) {
	b.container.UpdateBarPriority(b, priority)
}

// Abort interrupts bar's running goroutine. Abort won't be engaged
// if bar is already in complete state. If drop is true bar will be
// removed as well.
func (b *Bar) Abort(drop bool) {
	select {
	case b.operateState <- func(s *bState) {
		if s.completed || s.aborted {
			return
		}
		s.aborted = true
		s.dropOnComplete = drop
		go b.forceRefresh()
	}:
		<-b.done
	case <-b.done:
	}
}

// Aborted reports whether the bar is in aborted state.
func (b *Bar) Aborted() bool {
	result := make(chan bool)
	select {
	case b.operateState <- func(s *bState) { result <- s.aborted }:
		return <-result
	case <-b.done:
		return b.bs.aborted
	}
}

// Completed reports whether the bar is in completed state.
func (b *Bar) Completed() bool {
	result := make(chan bool)
	select {
	case b.operateState <- func(s *bState) { result <- s.completed }:
		return <-result
	case <-b.done:
		return b.bs.completed
	}
}

func (b *Bar) serve(ctx context.Context, bs *bState) {
	defer b.container.bwg.Done()
	if bs.afterBar != nil && bs.sync {
		<-bs.afterBar.done
	}
	for {
		select {
		case op := <-b.operateState:
			op(bs)
		case <-ctx.Done():
			bs.aborted = !bs.completed
			bs.decoratorShutdownNotify()
			b.bs = bs
			close(b.done)
			return
		}
	}
}

func (b *Bar) render(tw int) {
	select {
	case b.operateState <- func(s *bState) {
		var reader io.Reader
		var lines int
		stat := newStatistics(tw, s)
		defer func() {
			// recovering if user defined decorator panics for example
			if p := recover(); p != nil {
				if s.debugOut != nil {
					fmt.Fprintln(s.debugOut, p)
					_, _ = s.debugOut.Write(debug.Stack())
				}
				s.aborted = !s.completed
				s.extender = makePanicExtender(p)
				reader, lines = s.extender(nil, s.reqWidth, stat)
				b.recoveredPanic = p
			}
			b.frameCh <- &frame{
				reader:   reader,
				lines:    lines + 1,
				shutdown: s.completed || s.aborted,
			}
		}()
		if b.recoveredPanic == nil {
			reader = s.draw(stat)
		}
		reader, lines = s.extender(reader, s.reqWidth, stat)
	}:
	case <-b.done:
		var reader io.Reader
		var lines int
		stat, s := newStatistics(tw, b.bs), b.bs
		if b.recoveredPanic == nil {
			reader = s.draw(stat)
		}
		reader, lines = s.extender(reader, s.reqWidth, stat)
		b.frameCh <- &frame{
			reader: reader,
			lines:  lines + 1,
		}
	}
}

func (b *Bar) forceRefresh() {
	var anyOtherRunning bool
	b.container.traverseBars(func(bar *Bar) bool {
		anyOtherRunning = b != bar && !bar.Completed() && !bar.Aborted()
		return !anyOtherRunning
	})
	if !anyOtherRunning {
		for {
			select {
			case b.container.refreshCh <- time.Now():
				time.Sleep(prr)
			case <-b.done:
				return
			}
		}
	}
}

func (b *Bar) wSyncTable() [][]chan int {
	result := make(chan [][]chan int)
	select {
	case b.operateState <- func(s *bState) { result <- s.wSyncTable() }:
		return <-result
	case <-b.done:
		return b.bs.wSyncTable()
	}
}

func (s *bState) draw(stat decor.Statistics) io.Reader {
	bufP, bufB, bufA := s.buffers[0], s.buffers[1], s.buffers[2]
	nlr := strings.NewReader("\n")
	tw := stat.AvailableWidth
	for _, d := range s.pDecorators {
		str := d.Decor(stat)
		stat.AvailableWidth -= runewidth.StringWidth(stripansi.Strip(str))
		bufP.WriteString(str)
	}
	if stat.AvailableWidth < 1 {
		trunc := strings.NewReader(runewidth.Truncate(stripansi.Strip(bufP.String()), tw, "…"))
		bufP.Reset()
		return io.MultiReader(trunc, nlr)
	}

	if !s.trimSpace && stat.AvailableWidth > 1 {
		stat.AvailableWidth -= 2
		bufB.WriteByte(' ')
		defer bufB.WriteByte(' ')
	}

	tw = stat.AvailableWidth
	for _, d := range s.aDecorators {
		str := d.Decor(stat)
		stat.AvailableWidth -= runewidth.StringWidth(stripansi.Strip(str))
		bufA.WriteString(str)
	}
	if stat.AvailableWidth < 1 {
		trunc := strings.NewReader(runewidth.Truncate(stripansi.Strip(bufA.String()), tw, "…"))
		bufA.Reset()
		return io.MultiReader(bufP, bufB, trunc, nlr)
	}

	s.filler.Fill(bufB, s.reqWidth, stat)

	return io.MultiReader(bufP, bufB, bufA, nlr)
}

func (s *bState) wSyncTable() [][]chan int {
	columns := make([]chan int, 0, len(s.pDecorators)+len(s.aDecorators))
	var pCount int
	for _, d := range s.pDecorators {
		if ch, ok := d.Sync(); ok {
			columns = append(columns, ch)
			pCount++
		}
	}
	var aCount int
	for _, d := range s.aDecorators {
		if ch, ok := d.Sync(); ok {
			columns = append(columns, ch)
			aCount++
		}
	}
	table := make([][]chan int, 2)
	table[0] = columns[0:pCount]
	table[1] = columns[pCount : pCount+aCount : pCount+aCount]
	return table
}

func (s *bState) subscribeDecorators() {
	for _, decorators := range [...][]decor.Decorator{
		s.pDecorators,
		s.aDecorators,
	} {
		for _, d := range decorators {
			d = extractBaseDecorator(d)
			if d, ok := d.(decor.AverageDecorator); ok {
				s.averageDecorators = append(s.averageDecorators, d)
			}
			if d, ok := d.(decor.EwmaDecorator); ok {
				s.ewmaDecorators = append(s.ewmaDecorators, d)
			}
			if d, ok := d.(decor.ShutdownListener); ok {
				s.shutdownListeners = append(s.shutdownListeners, d)
			}
		}
	}
}

func (s bState) decoratorEwmaUpdate(dur time.Duration) {
	wg := new(sync.WaitGroup)
	for i := 0; i < len(s.ewmaDecorators); i++ {
		switch d := s.ewmaDecorators[i]; i {
		case len(s.ewmaDecorators) - 1:
			d.EwmaUpdate(s.lastIncrement, dur)
		default:
			wg.Add(1)
			go func() {
				d.EwmaUpdate(s.lastIncrement, dur)
				wg.Done()
			}()
		}
	}
	wg.Wait()
}

func (s bState) decoratorAverageAdjust(start time.Time) {
	wg := new(sync.WaitGroup)
	for i := 0; i < len(s.averageDecorators); i++ {
		switch d := s.averageDecorators[i]; i {
		case len(s.averageDecorators) - 1:
			d.AverageAdjust(start)
		default:
			wg.Add(1)
			go func() {
				d.AverageAdjust(start)
				wg.Done()
			}()
		}
	}
	wg.Wait()
}

func (s bState) decoratorShutdownNotify() {
	wg := new(sync.WaitGroup)
	for i := 0; i < len(s.shutdownListeners); i++ {
		switch d := s.shutdownListeners[i]; i {
		case len(s.shutdownListeners) - 1:
			d.Shutdown()
		default:
			wg.Add(1)
			go func() {
				d.Shutdown()
				wg.Done()
			}()
		}
	}
	wg.Wait()
}

func newStatistics(tw int, s *bState) decor.Statistics {
	return decor.Statistics{
		AvailableWidth: tw,
		ID:             s.id,
		Total:          s.total,
		Current:        s.current,
		Refill:         s.refill,
		Completed:      s.completed,
		Aborted:        s.aborted,
	}
}

func extractBaseDecorator(d decor.Decorator) decor.Decorator {
	if d, ok := d.(decor.Wrapper); ok {
		return extractBaseDecorator(d.Base())
	}
	return d
}

func makePanicExtender(p interface{}) extenderFunc {
	pstr := fmt.Sprint(p)
	return func(_ io.Reader, _ int, st decor.Statistics) (io.Reader, int) {
		mr := io.MultiReader(
			strings.NewReader(runewidth.Truncate(pstr, st.AvailableWidth, "…")),
			strings.NewReader("\n"),
		)
		return mr, 0
	}
}
