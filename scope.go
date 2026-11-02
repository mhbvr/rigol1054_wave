package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Scope is a SCPI connection to a Rigol DS1000Z series oscilloscope over a
// raw TCP socket (port 5555).
type Scope struct {
	conn net.Conn
	r    *bufio.Reader
}

// Dial connects to the scope. addr is an IP or host name with optional port.
func Dial(addr string) (*Scope, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "5555")
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return &Scope{conn: c, r: bufio.NewReader(c)}, nil
}

func (s *Scope) Close() error { return s.conn.Close() }

func (s *Scope) send(cmd string, timeout time.Duration) error {
	s.conn.SetDeadline(time.Now().Add(timeout))
	_, err := io.WriteString(s.conn, cmd+"\n")
	return err
}

// Query sends a query and returns the response line without the newline.
func (s *Scope) Query(q string) (string, error) {
	if err := s.send(q, 5*time.Second); err != nil {
		return "", err
	}
	line, err := s.r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("%s: %w", q, err)
	}
	return strings.TrimSpace(line), nil
}

// Command sends a command and waits until the scope has completed it.
func (s *Scope) Command(cmd string) error {
	if err := s.send(cmd, 5*time.Second); err != nil {
		return err
	}
	if _, err := s.Query("*OPC?"); err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	return nil
}

// QueryBlock sends a query whose answer is an IEEE 488.2 definite length
// block ("#9000001200<data>") and returns the data.
func (s *Scope) QueryBlock(q string) ([]byte, error) {
	if err := s.send(q, 30*time.Second); err != nil {
		return nil, err
	}
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(s.r, hdr); err != nil {
		return nil, fmt.Errorf("%s: %w", q, err)
	}
	if hdr[0] != '#' || hdr[1] < '1' || hdr[1] > '9' {
		return nil, fmt.Errorf("%s: bad block header %q", q, hdr)
	}
	digits := make([]byte, hdr[1]-'0')
	if _, err := io.ReadFull(s.r, digits); err != nil {
		return nil, fmt.Errorf("%s: %w", q, err)
	}
	n, err := strconv.Atoi(string(digits))
	if err != nil {
		return nil, fmt.Errorf("%s: bad block length %q", q, digits)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(s.r, data); err != nil {
		return nil, fmt.Errorf("%s: %w", q, err)
	}
	// Skip the terminating newline.
	if b, err := s.r.ReadByte(); err == nil && b != '\n' {
		s.r.UnreadByte()
	}
	return data, nil
}
