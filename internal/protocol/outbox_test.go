package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestOutboxWritesEventsInOrder(t *testing.T) {
	var w lockedBuffer
	o := newOutbox(&w)
	for i := range 100 {
		if !o.send(map[string]int{"n": i}) {
			t.Fatal("send refused an encodable event")
		}
	}
	if !o.close(waitLimit) {
		t.Fatal("the queue did not drain")
	}
	var want bytes.Buffer
	for i := range 100 {
		want.WriteString(`{"n":` + strconv.Itoa(i) + "}\n")
	}
	if w.String() != want.String() {
		t.Fatalf("written lines differ from the sends:\n%s", w.String())
	}
	if o.send(map[string]int{"n": 100}); w.String() != want.String() {
		t.Fatal("an event sent after close was written")
	}
}

func TestOutboxRefusesWhatItCannotEncode(t *testing.T) {
	var w lockedBuffer
	o := newOutbox(&w)
	if o.send(math.NaN()) {
		t.Fatal("send accepted a value JSON cannot carry")
	}
	o.close(waitLimit)
	if w.String() != "" {
		t.Fatalf("wrote %q for an event that was refused", w.String())
	}
}

// brokenWriter fails every write, like a stdout whose reader is gone.
type brokenWriter struct{ writes chan struct{} }

func (b brokenWriter) Write([]byte) (int, error) {
	b.writes <- struct{}{}
	return 0, errors.New("broken pipe")
}

func TestOutboxOutlivesABrokenWriter(t *testing.T) {
	w := brokenWriter{writes: make(chan struct{}, 8)}
	o := newOutbox(w)
	o.send("first")
	waitFor(t, o.done, "the writer to stop at the failed write")

	sent := make(chan struct{})
	go func() {
		for range 10_000 {
			o.send("later")
		}
		close(sent)
	}()
	waitFor(t, sent, "sends after the writer broke")
	o.mu.Lock()
	queued := len(o.queue)
	o.mu.Unlock()
	if queued != 0 {
		t.Fatalf("%d events are queued for a writer that is gone", queued)
	}
	if len(w.writes) != 1 {
		t.Fatalf("the writer was called %d times, want once", len(w.writes))
	}
	if !o.close(time.Second) {
		t.Fatal("close waited on a writer that is gone")
	}
}

func TestInlineEventPutsTheBodyBesideTheHead(t *testing.T) {
	type body struct {
		Free int64   `json:"free"`
		Days float64 `json:"days_to_full"`
	}
	tests := []struct {
		event inlineEvent
		want  string
	}{
		{inlineEvent{head{"headroom", "s"}, body{7, 2.5}}, `{"event":"headroom","re":"s","free":7,"days_to_full":2.5}`},
		{inlineEvent{head{"headroom", ""}, struct{}{}}, `{"event":"headroom"}`},
	}
	for _, tt := range tests {
		got, err := json.Marshal(tt.event)
		if err != nil || string(got) != tt.want {
			t.Errorf("Marshal = %s, %v; want %s", got, err, tt.want)
		}
	}
	if _, err := json.Marshal(inlineEvent{head{"headroom", "s"}, 7}); err == nil {
		t.Error("a body that is not an object was put on the line")
	}
}
