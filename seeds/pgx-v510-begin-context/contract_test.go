package pgxbegincontextcontract

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type postgresStub struct {
	listener net.Listener

	mu      sync.Mutex
	queries []string
	err     error
	done    chan struct{}
}

func startPostgresStub(t *testing.T) *postgresStub {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	stub := &postgresStub{listener: listener, done: make(chan struct{})}
	go stub.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-stub.done:
		case <-time.After(2 * time.Second):
			t.Fatal("postgres stub did not stop")
		}
		stub.mu.Lock()
		defer stub.mu.Unlock()
		if stub.err != nil && !errors.Is(stub.err, net.ErrClosed) && !errors.Is(stub.err, io.EOF) {
			t.Errorf("postgres stub: %v", stub.err)
		}
	})
	return stub
}

func (s *postgresStub) address() string {
	return s.listener.Addr().String()
}

func (s *postgresStub) recordedQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func (s *postgresStub) serve() {
	defer close(s.done)
	conn, err := s.listener.Accept()
	if err != nil {
		s.setError(err)
		return
	}
	defer conn.Close()

	if err := readStartup(conn); err != nil {
		s.setError(err)
		return
	}
	for _, message := range [][]byte{
		backendMessage('R', int32Payload(0)),
		backendMessage('S', cstrings("server_version", "17.0")),
		backendMessage('S', cstrings("client_encoding", "UTF8")),
		backendMessage('K', append(int32Payload(1234), int32Payload(5678)...)),
		backendMessage('Z', []byte{'I'}),
	} {
		if _, err := conn.Write(message); err != nil {
			s.setError(err)
			return
		}
	}

	transactionStatus := byte('I')
	for {
		messageType, payload, err := readFrontendMessage(conn)
		if err != nil {
			s.setError(err)
			return
		}
		switch messageType {
		case 'Q':
			query := strings.TrimSuffix(string(payload), "\x00")
			s.mu.Lock()
			s.queries = append(s.queries, strings.ToLower(strings.TrimSpace(query)))
			s.mu.Unlock()

			command := strings.ToUpper(strings.Fields(query)[0])
			switch command {
			case "BEGIN":
				transactionStatus = 'T'
			case "COMMIT", "ROLLBACK":
				transactionStatus = 'I'
			default:
				s.setError(fmt.Errorf("unexpected query %q", query))
				return
			}
			if _, err := conn.Write(backendMessage('C', cstrings(command))); err != nil {
				s.setError(err)
				return
			}
			if _, err := conn.Write(backendMessage('Z', []byte{transactionStatus})); err != nil {
				s.setError(err)
				return
			}
		case 'X':
			return
		default:
			s.setError(fmt.Errorf("unexpected frontend message %q", messageType))
			return
		}
	}
}

func (s *postgresStub) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func readStartup(reader io.Reader) error {
	var length uint32
	if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
		return err
	}
	if length < 8 {
		return fmt.Errorf("invalid startup length %d", length)
	}
	payload := make([]byte, length-4)
	_, err := io.ReadFull(reader, payload)
	return err
}

func readFrontendMessage(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length < 4 {
		return 0, nil, fmt.Errorf("invalid message length %d", length)
	}
	payload := make([]byte, length-4)
	_, err := io.ReadFull(reader, payload)
	return header[0], payload, err
}

func backendMessage(messageType byte, payload []byte) []byte {
	message := make([]byte, 5+len(payload))
	message[0] = messageType
	binary.BigEndian.PutUint32(message[1:], uint32(4+len(payload)))
	copy(message[5:], payload)
	return message
}

func int32Payload(value uint32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, value)
	return payload
}

func cstrings(values ...string) []byte {
	return []byte(strings.Join(values, "\x00") + "\x00")
}

func connect(t *testing.T, stub *postgresStub) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig("postgres://contract@" + stub.address() + "/contract?sslmode=disable")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	conn, err := pgx.ConnectConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func waitForQueries(t *testing.T, stub *postgresStub, count int) []string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		queries := stub.recordedQueries()
		if len(queries) >= count {
			return queries
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("recorded %v, want at least %d queries", stub.recordedQueries(), count)
	return nil
}

func TestCancellingBeginContextDoesNotRollbackTheTransaction(t *testing.T) {
	stub := startPostgresStub(t)
	conn := connect(t, stub)

	beginContext, cancel := context.WithCancel(context.Background())
	tx, err := conn.Begin(beginContext)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	cancel()

	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit after begin context cancellation: %v", err)
	}
	queries := waitForQueries(t, stub, 2)
	if got, want := strings.Join(queries, ","), "begin,commit"; got != want {
		t.Fatalf("queries = %q, want %q", got, want)
	}
	if err := tx.Rollback(context.Background()); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("rollback after commit = %v, want pgx.ErrTxClosed", err)
	}
}

func TestRollbackMustBeExplicitAndIsSafeToDefer(t *testing.T) {
	stub := startPostgresStub(t)
	conn := connect(t, stub)

	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatalf("first rollback: %v", err)
	}
	if err := tx.Rollback(context.Background()); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("second rollback = %v, want pgx.ErrTxClosed", err)
	}
	queries := waitForQueries(t, stub, 2)
	if got, want := strings.Join(queries, ","), "begin,rollback"; got != want {
		t.Fatalf("queries = %q, want %q", got, want)
	}
}
