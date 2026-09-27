package module

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// links returns two links joined in memory, handling calls with a and b.
func links(t *testing.T, a, b func(context.Context, Call) (json.RawMessage, error)) (*Link, *Link) {
	t.Helper()
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	la := NewLink(context.Background(), NewStdioStream(ar, aw), a)
	lb := NewLink(context.Background(), NewStdioStream(br, bw), b)
	t.Cleanup(func() {
		la.Close()
		lb.Close()
	})
	return la, lb
}

func TestLinkCallsBothWays(t *testing.T) {
	echo := func(_ context.Context, call Call) (json.RawMessage, error) {
		return json.Marshal(map[string]any{"ref": call.Ref, "from": call.From, "input": call.Input})
	}
	a, b := links(t, echo, echo)
	for _, l := range []*Link{a, b} {
		result, err := l.Call(context.Background(), Call{Ref: "echo.it", Input: json.RawMessage(`{"n":1}`), From: "me"})
		if err != nil {
			t.Fatal(err)
		}
		if string(result) != `{"from":"me","input":{"n":1},"ref":"echo.it"}` {
			t.Fatalf("result = %s", result)
		}
	}
}

func TestLinkCarriesErrors(t *testing.T) {
	fail := func(context.Context, Call) (json.RawMessage, error) {
		return nil, Errorf("greeter.busy", "try later")
	}
	a, _ := links(t, nil, fail)
	_, err := a.Call(context.Background(), Call{Ref: "greeter.hello"})
	if e := ErrorOf(err); e.Code != "greeter.busy" || e.Message != "try later" {
		t.Fatalf("err = %#v", e)
	}
}

func TestLinkPassesDeadlinesAndCancels(t *testing.T) {
	seen := make(chan error, 1)
	wait := func(ctx context.Context, _ Call) (json.RawMessage, error) {
		if _, ok := ctx.Deadline(); !ok {
			seen <- nil
			return nil, nil
		}
		<-ctx.Done()
		seen <- ctx.Err()
		return nil, ctx.Err()
	}
	a, _ := links(t, nil, wait)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := a.Call(ctx, Call{Ref: "slow.wait"}); Code(err) != CodeCanceled {
		t.Fatalf("err = %v, want %s", err, CodeCanceled)
	}
	select {
	case err := <-seen:
		if err != context.Canceled {
			t.Fatalf("the handler saw %v, want the call canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler was never canceled")
	}
}

func TestLinkFailsCallsWhenClosed(t *testing.T) {
	block := func(ctx context.Context, _ Call) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	a, b := links(t, nil, block)
	time.AfterFunc(50*time.Millisecond, func() { b.Close() })
	if _, err := a.Call(context.Background(), Call{Ref: "slow.wait"}); Code(err) != CodeUnavailable {
		t.Fatalf("err = %v, want %s", err, CodeUnavailable)
	}
}
