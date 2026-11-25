// Package mqtt provides a minimal pure-Go MQTT v3.1.1 QoS-0 publisher for optional smart-home alerts.
package mqtt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Client publishes retained state topics to an optional MQTT broker.
type Client struct {
	mu      sync.RWMutex
	host    string
	port    int
	prefix  string
	timeout time.Duration
	lastErr string
}

// New creates an MQTT publisher. When host is empty, Publish is a no-op.
func New(host string, port int, prefix string) *Client {
	if port <= 0 {
		port = 1883
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		prefix = "prusa/printer-hawk"
	}
	return &Client{
		host:    strings.TrimSpace(host),
		port:    port,
		prefix:  prefix,
		timeout: 3 * time.Second,
	}
}

// Update reconfigures the MQTT target safely at runtime.
func (c *Client) Update(host string, port int, prefix string) {
	if port <= 0 {
		port = 1883
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		prefix = "prusa/printer-hawk"
	}
	c.mu.Lock()
	c.host = strings.TrimSpace(host)
	c.port = port
	c.prefix = prefix
	c.lastErr = ""
	c.mu.Unlock()
}

// Enabled reports whether an MQTT broker host is configured.
func (c *Client) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.host != ""
}

// LastError returns the most recent publish error, if any.
func (c *Client) LastError() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// Publish sends a retained QoS-0 message to <prefix>/<subtopic>.
func (c *Client) Publish(subtopic string, payload []byte, retain bool) error {
	c.mu.RLock()
	host, port, prefix, timeout := c.host, c.port, c.prefix, c.timeout
	c.mu.RUnlock()

	if host == "" {
		return nil
	}

	topic := prefix + "/" + strings.TrimLeft(subtopic, "/")
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), timeout)
	if err != nil {
		c.setErr(err.Error())
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if _, err := conn.Write(buildConnect("printer-hawk")); err != nil {
		c.setErr(err.Error())
		return err
	}
	// Read CONNACK (4 bytes: 0x20 0x02 ackFlags returnCode)
	ack := make([]byte, 4)
	if _, err := conn.Read(ack); err != nil {
		c.setErr(err.Error())
		return err
	}
	if ack[0] != 0x20 || ack[3] != 0 {
		err := fmt.Errorf("mqtt connack rejected: code=%d", ack[3])
		c.setErr(err.Error())
		return err
	}

	if _, err := conn.Write(buildPublish(topic, payload, retain)); err != nil {
		c.setErr(err.Error())
		return err
	}
	_, _ = conn.Write([]byte{0xE0, 0x00}) // DISCONNECT
	c.setErr("")
	return nil
}

func (c *Client) setErr(msg string) {
	c.mu.Lock()
	c.lastErr = msg
	c.mu.Unlock()
}

func encodeString(buf *bytes.Buffer, s string) {
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(s)))
	buf.Write(lenBuf[:])
	buf.WriteString(s)
}

func writePacket(packetType byte, body []byte) []byte {
	var out bytes.Buffer
	out.WriteByte(packetType)
	rem := len(body)
	for {
		b := byte(rem % 128)
		rem /= 128
		if rem > 0 {
			b |= 0x80
		}
		out.WriteByte(b)
		if rem == 0 {
			break
		}
	}
	out.Write(body)
	return out.Bytes()
}

func buildConnect(clientID string) []byte {
	var body bytes.Buffer
	encodeString(&body, "MQTT")
	body.WriteByte(0x04) // MQTT 3.1.1
	body.WriteByte(0x02) // Clean session
	body.Write([]byte{0x00, 0x1E})
	encodeString(&body, clientID)
	return writePacket(0x10, body.Bytes())
}

func buildPublish(topic string, payload []byte, retain bool) []byte {
	var body bytes.Buffer
	encodeString(&body, topic)
	body.Write(payload)
	header := byte(0x30)
	if retain {
		header |= 0x01
	}
	return writePacket(header, body.Bytes())
}
