package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// maxMessageSize bounds one socket message (one line).
const maxMessageSize = 16 * 1024 * 1024

// Send writes one hook message read from r to the socket at socketPath —
// what status-hook.sh calls (as `mo hooks send`) instead of depending on an
// external `nc`. The message is compacted to a single line first, since the
// server reads one JSON message per line and Claude's hook payload may be
// pretty-printed.
func Send(socketPath string, r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, maxMessageSize))
	if err != nil {
		return fmt.Errorf("read message: %w", err)
	}
	var line bytes.Buffer
	if err := json.Compact(&line, bytes.TrimSpace(data)); err != nil {
		return fmt.Errorf("message is not valid JSON: %w", err)
	}
	line.WriteByte('\n')

	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Write(line.Bytes())
	return err
}
