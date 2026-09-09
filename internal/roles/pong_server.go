// Package roles implements the concurrent monitoring roles run by roled.
package roles

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"connection_monitor/internal/protocol"
)

// defaultMaxRequestBytes caps a single newline-framed request so an
// unauthenticated client cannot exhaust memory by streaming newline-free bytes.
const defaultMaxRequestBytes = 64 * 1024

// PongServer is a TCP server that answers newline-framed ping requests with
// pong responses.
type PongServer struct {
	listen      string
	readTimeout time.Duration
	maxRequest  int
	logger      *slog.Logger

	// ready is closed once the listener is bound; addr is then readable.
	ready chan struct{}
	addr  net.Addr
}

// NewPongServer builds a PongServer bound (at Run time) to listen.
func NewPongServer(listen string, readTimeout time.Duration, logger *slog.Logger) *PongServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &PongServer{
		listen:      listen,
		readTimeout: readTimeout,
		maxRequest:  defaultMaxRequestBytes,
		logger:      logger,
		ready:       make(chan struct{}),
	}
}

// Run listens and serves connections until ctx is cancelled, then closes the
// listener and returns.
func (s *PongServer) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.listen)
	if err != nil {
		return err
	}
	s.addr = ln.Addr()
	close(s.ready)
	s.logger.Info("pong_server listening", slog.String("addr", ln.Addr().String()))

	// Close the listener on cancellation so Accept unblocks.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.logger.Warn("pong_server accept error", slog.Any("error", err))
			continue
		}
		go s.handle(ctx, conn)
	}
}

// Ready returns a channel closed once the listener is bound. After it is
// closed, Addr returns the bound address.
func (s *PongServer) Ready() <-chan struct{} {
	return s.ready
}

// Addr returns the bound address; only valid after Ready is closed.
func (s *PongServer) Addr() net.Addr {
	return s.addr
}

func (s *PongServer) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Close the connection when the server shuts down.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

	// A fixed-size buffered reader bounds allocation: ReadSlice returns
	// bufio.ErrBufferFull rather than growing when a line exceeds the cap.
	reader := bufio.NewReaderSize(conn, s.maxRequest)
	for {
		if s.readTimeout > 0 {
			conn.SetReadDeadline(time.Now().Add(s.readTimeout))
		}
		line, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				// Oversized request: reply once (best-effort) and close the
				// connection without reading the rest.
				s.writeResponse(conn, protocol.NewError("", "request too large"))
			}
			// ErrBufferFull, EOF, timeout, or shutdown: end this connection.
			return
		}

		if err := s.writeResponse(conn, s.buildResponse(line)); err != nil {
			return
		}
	}
}

// writeResponse encodes and writes a single newline-framed response.
func (s *PongServer) writeResponse(conn net.Conn, resp protocol.Response) error {
	out, err := protocol.EncodeResponse(resp)
	if err != nil {
		s.logger.Warn("pong_server encode error", slog.Any("error", err))
		return err
	}
	out = append(out, '\n')
	_, err = conn.Write(out)
	return err
}

// buildResponse parses one request line and produces the appropriate response,
// never panicking on malformed input.
func (s *PongServer) buildResponse(line []byte) protocol.Response {
	req, err := protocol.DecodeRequest(line)
	if err != nil {
		if errors.Is(err, protocol.ErrVersionMismatch) {
			return protocol.NewError(req.ID, "unsupported protocol version")
		}
		return protocol.NewError("", "malformed request")
	}
	if req.Type != protocol.TypePing {
		return protocol.NewError(req.ID, "unsupported request type")
	}
	return protocol.NewPong(req)
}
