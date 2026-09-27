package module

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"time"
)

// A link carries calls both ways between two ends, as JSON-RPC 2.0: between
// nodes, over a WebSocket, or between a node and a process module, over
// stdio. A call is a request, whose method is the capability ref and whose
// params are the input; its end is the response. What else a call needs
// travels in params._meta. See spec/wire.md.
type Link struct {
	stream Stream
	handle func(ctx context.Context, call Call) (json.RawMessage, error)
	done   chan struct{}

	wmu     sync.Mutex
	mu      sync.Mutex
	next    uint64
	closed  bool
	pending map[uint64]chan message       // our calls, until they end
	running map[string]context.CancelFunc // the other end's, while handled, by ID
}

// Stream carries whole JSON-RPC messages.
type Stream interface {
	Read() ([]byte, error)
	Write([]byte) error
	Close() error
}

// Call is a call as a link carries it.
type Call struct {
	Ref   string
	Input json.RawMessage
	// From is the ID of the node that made the call, as a node tells its
	// process modules.
	From string
	// To is the ID of the node a process module's call is for, if not its
	// own.
	To string
}

// meta is what a call carries besides its input, in params._meta.
type meta struct {
	// Timeout is how long the caller waits, in milliseconds.
	Timeout int64  `json:"timeout,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

// message is a JSON-RPC 2.0 request, notification or response.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// cancelMethod is the notification that cancels a call, as in LSP.
const cancelMethod = "$/cancelRequest"

// MaxMessage is the size of the largest message a link carries between
// nodes, encoded.
const MaxMessage = 1 << 20

// NewLink returns a link over stream, until ctx is done or the stream ends.
// handle handles the calls the other end makes.
func NewLink(ctx context.Context, stream Stream, handle func(ctx context.Context, call Call) (json.RawMessage, error)) *Link {
	l := &Link{
		stream:  stream,
		handle:  handle,
		done:    make(chan struct{}),
		pending: map[uint64]chan message{},
		running: map[string]context.CancelFunc{},
	}
	ctx, cancel := context.WithCancel(ctx)
	context.AfterFunc(ctx, func() { l.Close() })
	go func() {
		defer cancel()
		for {
			data, err := stream.Read()
			if err != nil {
				return
			}
			var m message
			if err := json.Unmarshal(data, &m); err != nil {
				l.reply(json.RawMessage("null"), nil, Errorf(CodeInvalidArgument, "not a JSON-RPC message: %v", err))
				continue
			}
			l.receive(ctx, m)
		}
	}()
	return l
}

// NewStdioStream returns a stream over a process's stdio: one message per
// line, read from r and written to w.
func NewStdioStream(r io.Reader, w io.WriteCloser) Stream {
	return &stdio{json.NewDecoder(r), w}
}

type stdio struct {
	r *json.Decoder
	w io.WriteCloser
}

func (s *stdio) Read() ([]byte, error) {
	var m json.RawMessage
	err := s.r.Decode(&m)
	return m, err
}

func (s *stdio) Write(m []byte) error {
	_, err := s.w.Write(append(m, '\n'))
	return err
}

func (s *stdio) Close() error { return s.w.Close() }

// receive handles a message from the other end.
func (l *Link) receive(ctx context.Context, m message) {
	switch {
	case m.Method != "" && m.ID == nil: // a notification
		var cancel struct{ ID json.RawMessage }
		if m.Method == cancelMethod && json.Unmarshal(m.Params, &cancel) == nil {
			l.mu.Lock()
			stop := l.running[string(cancel.ID)]
			l.mu.Unlock()
			if stop != nil {
				stop()
			}
		}
	case m.Method != "": // a call
		l.serve(ctx, m)
	default: // an end
		id, err := strconv.ParseUint(string(m.ID), 10, 64)
		if err != nil {
			return
		}
		l.mu.Lock()
		ch := l.pending[id]
		delete(l.pending, id)
		l.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

// serve handles a call from the other end, and answers it.
func (l *Link) serve(ctx context.Context, m message) {
	input, meta, err := splitMeta(m.Params)
	if err != nil {
		l.reply(m.ID, nil, Errorf(CodeInvalidArgument, "params: %v", err))
		return
	}
	var cancel context.CancelFunc
	if meta.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(meta.Timeout)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	key := string(m.ID)
	l.mu.Lock()
	l.running[key] = cancel
	l.mu.Unlock()
	go func() {
		defer cancel()
		result, err := l.handle(ctx, Call{Ref: m.Method, Input: input, From: meta.From, To: meta.To})
		l.mu.Lock()
		delete(l.running, key)
		l.mu.Unlock()
		l.reply(m.ID, result, err)
	}()
}

// reply ends the call with the given ID.
func (l *Link) reply(id, result json.RawMessage, err error) {
	m := message{JSONRPC: "2.0", ID: id}
	if err != nil {
		m.Error = toRPC(ErrorOf(err))
	} else if m.Result = result; len(result) == 0 {
		m.Result = json.RawMessage("{}")
	}
	_ = l.send(m)
}

// Call makes a call and waits for it to end. It tells the other end how long
// it waits, from ctx, and cancels the call if ctx ends first.
func (l *Link) Call(ctx context.Context, call Call) (json.RawMessage, error) {
	m := meta{From: call.From, To: call.To}
	if deadline, ok := ctx.Deadline(); ok {
		m.Timeout = max(1, time.Until(deadline).Milliseconds())
	}
	params, err := withMeta(call.Input, m)
	if err != nil {
		return nil, err
	}
	ch := make(chan message, 1)
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, Errorf(CodeUnavailable, "link closed")
	}
	l.next++
	id := l.next
	l.pending[id] = ch
	l.mu.Unlock()

	rawID := json.RawMessage(strconv.FormatUint(id, 10))
	if err := l.send(message{JSONRPC: "2.0", ID: rawID, Method: call.Ref, Params: params}); err != nil {
		l.forget(id)
		return nil, Errorf(CodeUnavailable, "%v", err)
	}
	select {
	case end := <-ch:
		if end.Error != nil {
			return nil, end.Error.module()
		}
		return end.Result, nil
	case <-ctx.Done():
		l.forget(id)
		cancel, _ := json.Marshal(map[string]json.RawMessage{"id": rawID})
		_ = l.send(message{JSONRPC: "2.0", Method: cancelMethod, Params: cancel})
		return nil, ErrorOf(ctx.Err())
	}
}

// Done is closed once the link is.
func (l *Link) Done() <-chan struct{} { return l.done }

// Close closes the link and its stream. Calls waiting for their end fail.
func (l *Link) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	for id, ch := range l.pending {
		ch <- message{Error: toRPC(Errorf(CodeUnavailable, "link closed"))}
		delete(l.pending, id)
	}
	close(l.done)
	return l.stream.Close()
}

func (l *Link) send(m message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return l.stream.Write(data)
}

func (l *Link) forget(id uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.pending, id)
}

// withMeta returns input, an object, with m as its _meta.
func withMeta(input json.RawMessage, m meta) (json.RawMessage, error) {
	params := map[string]json.RawMessage{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return nil, Errorf(CodeInvalidArgument, "input must be an object: %v", err)
		}
	}
	if m != (meta{}) {
		data, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		params["_meta"] = data
	}
	return json.Marshal(params)
}

// splitMeta splits params into the call's input and its _meta.
func splitMeta(params json.RawMessage) (json.RawMessage, meta, error) {
	var m meta
	if len(bytes.TrimSpace(params)) == 0 {
		return json.RawMessage("{}"), m, nil
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, m, err
	}
	if data, ok := fields["_meta"]; ok {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, m, err
		}
		delete(fields, "_meta")
	}
	input, err := json.Marshal(fields)
	return input, m, err
}
