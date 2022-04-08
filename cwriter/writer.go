package cwriter

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
)

// ErrNotTTY not a TeleTYpewriter error.
var ErrNotTTY = errors.New("not a terminal")

// http://ascii-table.com/ansi-escape-sequences.php
const (
	escOpen  = "\x1b["
	cuuAndEd = "A\x1b[J"
)

// Writer is a buffered the writer that updates the terminal. The
// contents of writer will be flushed when Flush is called.
type Writer struct {
	sync.Mutex
	out                io.Writer
	buf                bytes.Buffer
	lineCount          int
	fd                 int
	isTerminal         bool
	lastTypeIsProgress bool
}

// New returns a new Writer with defaults.
func New(out io.Writer) *Writer {
	w := &Writer{out: out}
	if f, ok := out.(*os.File); ok {
		w.fd = int(f.Fd())
		w.isTerminal = IsTerminal(w.fd)
	}
	return w
}

// FlushBar flushes the underlying buffer.
func (w *Writer) FlushBar(lineCount int) (err error) {
	w.Lock()
	defer w.Unlock()
	// some terminals interpret 'cursor up 0' as 'cursor up 1'
	if w.lastTypeIsProgress && w.lineCount > 0 {
		err = w.clearLines()
		if err != nil {
			return
		}
	}
	w.lineCount = lineCount
	_, err = w.buf.WriteTo(w.out)
	w.lastTypeIsProgress = true
	return
}

func (w *Writer) FlushLog(content []byte) (n int, err error) {
	w.Lock()
	defer w.Unlock()
	write, err := w.Write(content)
	// some terminals interpret 'cursor up 0' as 'cursor up 1'
	if w.lastTypeIsProgress && w.lineCount > 0 {
		err = w.clearLines()
		if err != nil {
			return
		}
	}

	_, err = w.buf.WriteTo(w.out)

	w.lineCount = bytes.Count(content, []byte("\n"))
	w.lastTypeIsProgress = false
	return write, err
}

// Write appends the contents of p to the underlying buffer.
func (w *Writer) Write(p []byte) (n int, err error) {
	return w.buf.Write(p)
}

// WriteString writes string to the underlying buffer.
func (w *Writer) WriteString(s string) (n int, err error) {
	return w.buf.WriteString(s)
}

// ReadFrom reads from the provided io.Reader and writes to the
// underlying buffer.
func (w *Writer) ReadFrom(r io.Reader) (n int64, err error) {
	return w.buf.ReadFrom(r)
}

// GetWidth returns width of underlying terminal.
func (w *Writer) GetWidth() (int, error) {
	if !w.isTerminal {
		return -1, ErrNotTTY
	}
	tw, _, err := GetSize(w.fd)
	return tw, err
}

func (w *Writer) ansiCuuAndEd() (err error) {
	buf := make([]byte, 8)
	buf = strconv.AppendInt(buf[:copy(buf, escOpen)], int64(w.lineCount), 10)
	_, err = w.out.Write(append(buf, cuuAndEd...))
	return
}
