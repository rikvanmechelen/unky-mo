//go:build !linux

package notify

import "net"

// samePeer can't read the peer's user here; the socket's private directory
// and 0600 mode are what keep other users out.
func samePeer(net.Conn) bool { return true }
