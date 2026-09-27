package module

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// A link carries calls both ways between two ends, as JSON-RPC 2.0: between
// nodes, over a WebSocket, or between a node and a process module, over
// stdio. A call is a request, whose method is the capability ref and whose
// params are the input; its end is the response. What else a call needs
// travels in params._meta. See spec/wire.md.
type Link struct {
	conn *jsonrpc2.Conn

	mu      sync.Mutex
	next    uint64
	running map[jsonrpc2.ID]context.CancelFunc // the other end's calls, while handled
}

// Call is a call as a link carries it.
type Call struct {
	Ref   string
	Input json.RawMessage
	// From is the ID of the node that made the call, as a node tells its
	// process modules.
	From string
	// To is the node a process module's call is for, if not its own.
	To *Peer
}

// meta is what a call carries besides its input, in params._meta.
type meta struct {
	// Timeout is how long the caller waits, in milliseconds.
	Timeout int64  `json:"timeout,omitempty"`
	From    string `json:"from,omitempty"`
	To      *Peer  `json:"to,omitempty"`
}

// cancelMethod is the notification that cancels a call, as in LSP.
const cancelMethod = "$/cancelRequest"

// MaxMessage is the size of the largest message a link carries between
// nodes, encoded.
const MaxMessage = 1 << 20

// NewLink returns a link over stream, until ctx is done or the stream
// closes. handle handles the calls the other end makes.
func NewLink(ctx context.Context, stream jsonrpc2.ObjectStream, handle func(ctx context.Context, call Call) (json.RawMessage, error)) *Link {
	l := &Link{running: map[jsonrpc2.ID]context.CancelFunc{}}
	l.conn = jsonrpc2.NewConn(ctx, stream, jsonrpc2.AsyncHandler(handler{l, handle}),
		jsonrpc2.SetLogger(log.New(io.Discard, "", 0)))
	return l
}

// NewStdioLink returns a link over a process's stdio: newline-delimited
// JSON read from r and written to w.
func NewStdioLink(ctx context.Context, r io.Reader, w io.WriteCloser, handle func(ctx context.Context, call Call) (json.RawMessage, error)) *Link {
	return NewLink(ctx, jsonrpc2.NewBufferedStream(stdio{r, w}, jsonrpc2.PlainObjectCodec{}), handle)
}

type stdio struct {
	io.Reader
	io.WriteCloser
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
	l.mu.Lock()
	l.next++
	id := jsonrpc2.ID{Num: l.next}
	l.mu.Unlock()
	w, err := l.conn.DispatchCall(ctx, call.Ref, params, jsonrpc2.PickID(id))
	if err != nil {
		return nil, Errorf(CodeUnavailable, "%v", err)
	}
	var result json.RawMessage
	err = w.Wait(ctx, &result)
	var rpcErr *jsonrpc2.Error
	switch {
	case err == nil:
		return result, nil
	case errors.As(err, &rpcErr):
		return nil, fromRPC(rpcErr)
	case ctx.Err() != nil:
		_ = l.conn.Notify(context.Background(), cancelMethod, map[string]jsonrpc2.ID{"id": id})
		return nil, ErrorOf(ctx.Err())
	}
	return nil, Errorf(CodeUnavailable, "%v", err)
}

// Done is closed once the link is.
func (l *Link) Done() <-chan struct{} { return l.conn.DisconnectNotify() }

func (l *Link) Close() error { return l.conn.Close() }

// handler handles what the other end sends: its calls, and its
// cancellations.
type handler struct {
	l      *Link
	handle func(ctx context.Context, call Call) (json.RawMessage, error)
}

func (h handler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		var cancel struct{ ID jsonrpc2.ID }
		if req.Method == cancelMethod && req.Params != nil && json.Unmarshal(*req.Params, &cancel) == nil {
			h.l.mu.Lock()
			stop := h.l.running[cancel.ID]
			h.l.mu.Unlock()
			if stop != nil {
				stop()
			}
		}
		return // other notifications are for later versions
	}
	var params json.RawMessage
	if req.Params != nil {
		params = *req.Params
	}
	input, m, err := splitMeta(params)
	if err != nil {
		_ = conn.ReplyWithError(ctx, req.ID, toRPC(Errorf(CodeInvalidArgument, "params: %v", err)))
		return
	}
	var cancel context.CancelFunc
	if m.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(m.Timeout)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	h.l.mu.Lock()
	h.l.running[req.ID] = cancel
	h.l.mu.Unlock()
	defer func() {
		h.l.mu.Lock()
		delete(h.l.running, req.ID)
		h.l.mu.Unlock()
	}()

	result, err := h.handle(ctx, Call{Ref: req.Method, Input: input, From: m.From, To: m.To})
	if err != nil {
		_ = conn.ReplyWithError(ctx, req.ID, toRPC(ErrorOf(err)))
		return
	}
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	_ = conn.Reply(ctx, req.ID, result)
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
	if len(params) == 0 {
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
