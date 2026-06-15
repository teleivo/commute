package swim_test

import (
	"net"
	"net/netip"
)

type fakeListener struct {
	addr   *net.TCPAddr
	closed chan struct{}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *fakeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *fakeListener) Addr() net.Addr { return l.addr }

func newFakeListener() net.Listener {
	return &fakeListener{
		addr:   net.TCPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 5000)),
		closed: make(chan struct{}),
	}
}
