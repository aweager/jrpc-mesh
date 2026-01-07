package internal

import (
	"context"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

// testConns stores distinct connection pointers for testing.
// We only need pointer identity, not real connection behavior.
var testConns = make(map[int]*jsonrpc2.Conn)

func getTestConn(id int) *jsonrpc2.Conn {
	if testConns[id] == nil {
		// Create a unique pointer by allocating a new struct
		testConns[id] = &jsonrpc2.Conn{}
	}
	return testConns[id]
}

func TestRouteTable_Update(t *testing.T) {
	rt := NewRouteTable()
	conn := getTestConn(1)

	// Add initial prefixes
	rt.Update(conn, []string{"foo/", "bar/"})

	if got := rt.Lookup("foo/method"); got != conn {
		t.Errorf("Lookup(foo/method) = %v, want %v", got, conn)
	}
	if got := rt.Lookup("bar/method"); got != conn {
		t.Errorf("Lookup(bar/method) = %v, want %v", got, conn)
	}

	// Update to new prefixes, removing bar/
	rt.Update(conn, []string{"foo/", "baz/"})

	if got := rt.Lookup("foo/method"); got != conn {
		t.Errorf("Lookup(foo/method) after update = %v, want %v", got, conn)
	}
	if got := rt.Lookup("bar/method"); got != nil {
		t.Errorf("Lookup(bar/method) after update = %v, want nil", got)
	}
	if got := rt.Lookup("baz/method"); got != conn {
		t.Errorf("Lookup(baz/method) after update = %v, want %v", got, conn)
	}
}

func TestRouteTable_Lookup_LongestPrefix(t *testing.T) {
	rt := NewRouteTable()
	conn1 := getTestConn(10)
	conn2 := getTestConn(11)

	rt.Update(conn1, []string{"foo/"})
	rt.Update(conn2, []string{"foo/bar/"})

	// foo/baz should match conn1 (prefix "foo/")
	if got := rt.Lookup("foo/baz"); got != conn1 {
		t.Errorf("Lookup(foo/baz) = %v, want %v", got, conn1)
	}

	// foo/bar/baz should match conn2 (longer prefix "foo/bar/")
	if got := rt.Lookup("foo/bar/baz"); got != conn2 {
		t.Errorf("Lookup(foo/bar/baz) = %v, want %v", got, conn2)
	}
}

func TestRouteTable_Lookup_NoMatch(t *testing.T) {
	rt := NewRouteTable()
	conn := getTestConn(20)

	rt.Update(conn, []string{"foo/"})

	if got := rt.Lookup("bar/method"); got != nil {
		t.Errorf("Lookup(bar/method) = %v, want nil", got)
	}
}

func TestRouteTable_Lookup_EmptyTable(t *testing.T) {
	rt := NewRouteTable()

	if got := rt.Lookup("any/method"); got != nil {
		t.Errorf("Lookup on empty table = %v, want nil", got)
	}
}

func TestRouteTable_RemoveConn(t *testing.T) {
	rt := NewRouteTable()
	conn1 := getTestConn(30)
	conn2 := getTestConn(31)

	rt.Update(conn1, []string{"foo/", "bar/"})
	rt.Update(conn2, []string{"baz/"})

	rt.RemoveConn(conn1)

	if got := rt.Lookup("foo/method"); got != nil {
		t.Errorf("Lookup(foo/method) after RemoveConn = %v, want nil", got)
	}
	if got := rt.Lookup("bar/method"); got != nil {
		t.Errorf("Lookup(bar/method) after RemoveConn = %v, want nil", got)
	}
	// conn2's routes should remain
	if got := rt.Lookup("baz/method"); got != conn2 {
		t.Errorf("Lookup(baz/method) after RemoveConn = %v, want %v", got, conn2)
	}
}

func TestRouteTable_WaitUntilRoutable_AlreadyRoutable(t *testing.T) {
	rt := NewRouteTable()
	conn := getTestConn(40)

	rt.Update(conn, []string{"foo/"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := rt.WaitUntilRoutable(ctx, "foo/method")
	if err != nil {
		t.Errorf("WaitUntilRoutable for existing route = %v, want nil", err)
	}
}

func TestRouteTable_WaitUntilRoutable_BecomesRoutable(t *testing.T) {
	rt := NewRouteTable()
	conn := getTestConn(50)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- rt.WaitUntilRoutable(ctx, "foo/method")
	}()

	// Give the goroutine time to start waiting
	time.Sleep(50 * time.Millisecond)

	// Add the route
	rt.Update(conn, []string{"foo/"})

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitUntilRoutable = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Error("WaitUntilRoutable did not return after route was added")
	}
}

func TestRouteTable_WaitUntilRoutable_Timeout(t *testing.T) {
	rt := NewRouteTable()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := rt.WaitUntilRoutable(ctx, "foo/method")
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Errorf("WaitUntilRoutable = %v, want context.DeadlineExceeded", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("WaitUntilRoutable returned too quickly: %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitUntilRoutable took too long: %v", elapsed)
	}
}

func TestRouteTable_WaitUntilRoutable_ContextCancelled(t *testing.T) {
	rt := NewRouteTable()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- rt.WaitUntilRoutable(ctx, "foo/method")
	}()

	// Give the goroutine time to start waiting
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("WaitUntilRoutable = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Error("WaitUntilRoutable did not return after context was cancelled")
	}
}

func TestRouteTable_Update_OverwritesExistingPrefix(t *testing.T) {
	rt := NewRouteTable()
	conn1 := getTestConn(60)
	conn2 := getTestConn(61)

	rt.Update(conn1, []string{"foo/"})
	rt.Update(conn2, []string{"foo/"})

	// conn2 should now own the prefix
	if got := rt.Lookup("foo/method"); got != conn2 {
		t.Errorf("Lookup(foo/method) = %v, want %v", got, conn2)
	}
}

func TestRouteTable_Update_EmptyPrefixes(t *testing.T) {
	rt := NewRouteTable()
	conn := getTestConn(70)

	rt.Update(conn, []string{"foo/"})
	rt.Update(conn, []string{})

	// All prefixes for conn should be removed
	if got := rt.Lookup("foo/method"); got != nil {
		t.Errorf("Lookup(foo/method) after empty update = %v, want nil", got)
	}
}