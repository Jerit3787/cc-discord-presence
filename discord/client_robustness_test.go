package discord

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type chunkedReadConn struct {
	*mockConn
	chunk int
}

func (c *chunkedReadConn) Read(b []byte) (int, error) {
	if len(b) > c.chunk {
		b = b[:c.chunk]
	}
	return c.mockConn.Read(b)
}

type shortWriteConn struct {
	*mockConn
	chunk int
}

func (c *shortWriteConn) Write(b []byte) (int, error) {
	if len(b) > c.chunk {
		b = b[:c.chunk]
	}
	return c.mockConn.Write(b)
}

func TestClientReceiveHandlesShortReads(t *testing.T) {
	mock := &mockConn{}
	payload := []byte(`{"cmd":"DISPATCH","evt":"READY"}`)
	mock.writeFrame(opFrame, payload)

	client := NewClient("test")
	client.conn = &chunkedReadConn{mockConn: mock, chunk: 1}

	got, err := client.receive()
	if err != nil {
		t.Fatalf("receive() error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("receive() = %q, want %q", got, payload)
	}
}

func TestClientReceiveRejectsOversizedFrame(t *testing.T) {
	mock := &mockConn{}
	binary.Write(&mock.readBuffer, binary.LittleEndian, int32(opFrame))
	binary.Write(&mock.readBuffer, binary.LittleEndian, int32(maxFrameSize+1))

	client := NewClient("test")
	client.conn = mock

	if _, err := client.receive(); err == nil {
		t.Fatal("receive() accepted an oversized frame")
	}
}

func TestClientSendHandlesShortWrites(t *testing.T) {
	mock := &mockConn{}
	client := NewClient("test")
	client.conn = &shortWriteConn{mockConn: mock, chunk: 1}

	if err := client.send(opFrame, map[string]string{"a": "b"}); err != nil {
		t.Fatalf("send() error: %v", err)
	}

	payload, err := json.Marshal(map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	if want := 8 + len(payload); mock.writeBuffer.Len() != want {
		t.Errorf("wrote %d bytes, want %d", mock.writeBuffer.Len(), want)
	}
}

func TestClientConnectRequiresReady(t *testing.T) {
	tests := []struct {
		name    string
		opcode  int32
		payload string
		wantErr string
	}{
		{
			name:    "READY dispatch succeeds",
			opcode:  opFrame,
			payload: `{"cmd":"DISPATCH","evt":"READY","data":{}}`,
		},
		{
			name:    "CLOSE is rejected",
			opcode:  opClose,
			payload: `{"code":4000,"message":"bad client id"}`,
			wantErr: "bad client id",
		},
		{
			name:    "non-READY frame is rejected",
			opcode:  opFrame,
			payload: `{"cmd":"SET_ACTIVITY"}`,
			wantErr: "READY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockConn{}
			mock.writeFrame(tt.opcode, []byte(tt.payload))

			client := NewClient("test")
			client.dial = func() (Conn, error) { return mock, nil }

			err := client.Connect()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Connect() error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Connect() succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Connect() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestClientConnectDialError(t *testing.T) {
	client := NewClient("test")
	client.dial = func() (Conn, error) { return nil, errors.New("dial failed") }

	if err := client.Connect(); err == nil {
		t.Fatal("Connect() succeeded, want dial error")
	}
	if client.conn != nil {
		t.Error("Connect() left a connection after a dial error")
	}
}

func TestClientCloseClearsConnection(t *testing.T) {
	client := NewClient("test")
	mock := &mockConn{}
	client.conn = mock

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if client.conn != nil {
		t.Error("Close() left conn set")
	}
	if err := client.SetActivity(Activity{Details: "x"}); err == nil || err.Error() != "not connected" {
		t.Errorf("SetActivity() after Close = %v, want not connected", err)
	}
}

func TestClientConcurrentSetActivityAndClose(t *testing.T) {
	client := NewClient("test")
	client.conn = &mockConn{}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = client.SetActivity(Activity{Details: "test"})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = client.Close()
	}()
	wg.Wait()
}
