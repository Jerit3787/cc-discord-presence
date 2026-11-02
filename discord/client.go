package discord

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Opcodes for Discord IPC
const (
	opHandshake = 0
	opFrame     = 1
	opClose     = 2

	// maxFrameSize bounds the allocation for a single incoming frame. Discord
	// activity payloads are small; anything larger is a protocol error.
	maxFrameSize = 1 << 20

	// ioTimeout keeps a hung Discord process from blocking Connect or
	// SetActivity indefinitely.
	ioTimeout = 5 * time.Second
)

// Activity represents Discord Rich Presence activity
type Activity struct {
	Details    string     `json:"details,omitempty"`
	State      string     `json:"state,omitempty"`
	LargeImage string     `json:"large_image,omitempty"`
	LargeText  string     `json:"large_text,omitempty"`
	SmallImage string     `json:"small_image,omitempty"`
	SmallText  string     `json:"small_text,omitempty"`
	StartTime  *time.Time `json:"-"`
}

// Conn interface for both unix sockets and windows pipes
type Conn interface {
	Read(b []byte) (n int, err error)
	Write(b []byte) (n int, err error)
	Close() error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// Client handles Discord RPC connection
type Client struct {
	clientID string
	mu       sync.Mutex
	conn     Conn

	// dial is overridable in tests. NewClient sets it to connectToDiscord.
	dial func() (Conn, error)
}

// NewClient creates a new Discord RPC client
func NewClient(clientID string) *Client {
	client := &Client{clientID: clientID}
	client.dial = client.connectToDiscord
	return client
}

// Connect establishes connection to Discord and validates the READY handshake.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	dial := c.dial
	if dial == nil {
		dial = c.connectToDiscord
	}
	conn, err := dial()
	if err != nil {
		return err
	}
	c.conn = conn

	// Send handshake
	handshake := map[string]interface{}{
		"v":         1,
		"client_id": c.clientID,
	}
	if err := c.sendLocked(opHandshake, handshake); err != nil {
		c.closeLocked()
		return fmt.Errorf("handshake failed: %w", err)
	}

	if err := c.conn.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		c.closeLocked()
		return fmt.Errorf("handshake response failed: %w", err)
	}
	opcode, payload, err := c.receiveFrameLocked()
	_ = c.conn.SetReadDeadline(time.Time{})
	if err != nil {
		c.closeLocked()
		return fmt.Errorf("handshake response failed: %w", err)
	}
	if opcode == opClose {
		c.closeLocked()
		var closeFrame struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &closeFrame)
		return fmt.Errorf("discord closed the connection: %d %s", closeFrame.Code, closeFrame.Message)
	}
	if opcode != opFrame {
		c.closeLocked()
		return fmt.Errorf("handshake failed: unexpected opcode %d", opcode)
	}

	var ready struct {
		Cmd string `json:"cmd"`
		Evt string `json:"evt"`
	}
	if err := json.Unmarshal(payload, &ready); err != nil {
		c.closeLocked()
		return fmt.Errorf("handshake failed: invalid response: %w", err)
	}
	if ready.Evt != "READY" {
		c.closeLocked()
		return fmt.Errorf("handshake failed: expected READY, got %q", string(payload))
	}

	return nil
}

// Reconnect closes any existing connection and establishes a fresh one.
// Used to recover after Discord is quit and reopened.
func (c *Client) Reconnect() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	return c.Connect()
}

// SetActivity updates the Discord Rich Presence
func (c *Client) SetActivity(activity Activity) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}

	// Build timestamps if StartTime is set
	var timestamps map[string]int64
	if activity.StartTime != nil {
		timestamps = map[string]int64{
			"start": activity.StartTime.Unix(),
		}
	}

	// Build assets
	assets := map[string]string{}
	if activity.LargeImage != "" {
		assets["large_image"] = activity.LargeImage
	}
	if activity.LargeText != "" {
		assets["large_text"] = activity.LargeText
	}
	if activity.SmallImage != "" {
		assets["small_image"] = activity.SmallImage
	}
	if activity.SmallText != "" {
		assets["small_text"] = activity.SmallText
	}

	activityData := map[string]interface{}{}
	if activity.Details != "" {
		activityData["details"] = activity.Details
	}
	if activity.State != "" {
		activityData["state"] = activity.State
	}
	if len(assets) > 0 {
		activityData["assets"] = assets
	}
	if timestamps != nil {
		activityData["timestamps"] = timestamps
	}

	payload := map[string]interface{}{
		"cmd": "SET_ACTIVITY",
		"args": map[string]interface{}{
			"pid":      os.Getpid(),
			"activity": activityData,
		},
		"nonce": fmt.Sprintf("%d", time.Now().UnixNano()),
	}

	return c.sendLocked(opFrame, payload)
}

// Close disconnects from Discord and clears the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// closeLocked closes and clears the current connection. The caller must hold
// c.mu.
func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *Client) send(opcode int, data interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendLocked(opcode, data)
}

// sendLocked writes one complete frame. The caller must hold c.mu.
func (c *Client) sendLocked(opcode int, data interface{}) error {
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}

	if err := c.conn.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	defer c.conn.SetWriteDeadline(time.Time{})

	// Discord IPC frame: [opcode:4bytes][length:4bytes][payload]
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, int32(opcode))
	binary.Write(buf, binary.LittleEndian, int32(len(payload)))
	buf.Write(payload)

	remaining := buf.Bytes()
	for len(remaining) > 0 {
		n, err := c.conn.Write(remaining)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[n:]
	}
	return nil
}

func (c *Client) receive() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, payload, err := c.receiveFrameLocked()
	return payload, err
}

// receiveFrameLocked reads one complete frame. The caller must hold c.mu.
func (c *Client) receiveFrameLocked() (int, []byte, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return 0, nil, err
	}

	opcode := int(binary.LittleEndian.Uint32(header[0:4]))
	length := binary.LittleEndian.Uint32(header[4:8])
	if length > maxFrameSize {
		return opcode, nil, fmt.Errorf("frame too large: %d bytes", length)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(c.conn, payload); err != nil {
			return opcode, nil, err
		}
	}
	return opcode, payload, nil
}
