package pgstate

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/lib/pq"
)

func TestParseLSN(t *testing.T) {
	l, err := ParseLSN("16/B374D848")
	if err != nil {
		t.Fatal(err)
	}
	if uint64(l) != 0x16B374D848 {
		t.Fatalf("got %x", uint64(l))
	}
	if l.String() != "16/B374D848" {
		t.Fatalf("round trip: %s", l)
	}
	for _, bad := range []string{"", "16", "x/1", "1/zz"} {
		if _, err := ParseLSN(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestParseHistory(t *testing.T) {
	h := "1\t0/3000148\tno recovery target specified\n\n2\t0/5000000\tat restore point \"x\"\n"
	e, err := ParseHistory(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 2 || e[0].ParentTLI != 1 || e[0].SwitchPoint.String() != "0/3000148" || e[1].ParentTLI != 2 {
		t.Fatalf("unexpected %+v", e)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "ack"},
		{&pq.Error{Code: "25006"}, "fail"},     // read-only transaction (hit a standby)
		{&pq.Error{Code: "57P01"}, "fail"},     // admin shutdown
		{&pq.Error{Code: "57014"}, "in-doubt"}, // our own cancel
		{&net.OpError{Op: "dial", Err: errors.New("refused")}, "fail"},
		{context.DeadlineExceeded, "in-doubt"},
		{errors.New("unexpected EOF"), "in-doubt"},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Errorf("Classify(%v) = %s, want %s", c.err, got, c.want)
		}
	}
}
