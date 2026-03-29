package nbd

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
)

const (
	NBD_REQUEST_MAGIC = 0x25609513
	NBD_REPLY_MAGIC   = 0x67446698
	NBD_CMD_READ      = 0
	NBD_CMD_DISC      = 2

	INIT_PASSWD    = 0x4e42444d41474943
	OPTS_MAGIC     = 0x49484156454F5054
	NBD_OPT_EXPORT = 1
	NBD_REP_ACK    = 1
	NBD_REP_SERVER = 2

	NBD_FLAG_FIXED_NEWSTYLE = 1
	NBD_FLAG_NO_ZEROES      = 2
	NBD_FLAG_C_FIXED_NEWSTYLE = 1
	NBD_FLAG_C_NO_ZEROES      = 2
)

type Server struct {
	isoDir   string
	port     int
	listener net.Listener
	mu       sync.RWMutex
	clients  map[string]*Client
}

type Client struct {
	conn     net.Conn
	file     *os.File
	fileSize int64
	isoName  string
}

func NewServer(isoDir string, port int) *Server {
	return &Server{
		isoDir:  isoDir,
		port:    port,
		clients: make(map[string]*Client),
	}
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start NBD server: %w", err)
	}

	s.listener = listener
	log.Printf("NBD server listening on %s", addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}

		go s.handleClient(conn)
	}
}

func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()

	log.Printf("NBD client connected: %s", conn.RemoteAddr())

	isoPath, fileSize, err := s.negotiateConnection(conn)
	if err != nil {
		log.Printf("Negotiation failed: %v", err)
		return
	}

	file, err := os.Open(isoPath)
	if err != nil {
		log.Printf("Failed to open ISO: %v", err)
		return
	}
	defer file.Close()

	client := &Client{
		conn:     conn,
		file:     file,
		fileSize: fileSize,
		isoName:  filepath.Base(isoPath),
	}

	if err := s.handleRequests(client); err != nil {
		log.Printf("Client error: %v", err)
	}

	log.Printf("NBD client disconnected: %s", conn.RemoteAddr())
}

func (s *Server) negotiateConnection(conn net.Conn) (string, int64, error) {
	if err := binary.Write(conn, binary.BigEndian, INIT_PASSWD); err != nil {
		return "", 0, err
	}
	if err := binary.Write(conn, binary.BigEndian, OPTS_MAGIC); err != nil {
		return "", 0, err
	}

	flags := uint16(NBD_FLAG_FIXED_NEWSTYLE | NBD_FLAG_NO_ZEROES)
	if err := binary.Write(conn, binary.BigEndian, flags); err != nil {
		return "", 0, err
	}

	var clientFlags uint32
	if err := binary.Read(conn, binary.BigEndian, &clientFlags); err != nil {
		return "", 0, err
	}

	var optMagic uint64
	if err := binary.Read(conn, binary.BigEndian, &optMagic); err != nil {
		return "", 0, err
	}
	if optMagic != OPTS_MAGIC {
		return "", 0, fmt.Errorf("invalid option magic")
	}

	var optType uint32
	if err := binary.Read(conn, binary.BigEndian, &optType); err != nil {
		return "", 0, err
	}

	var nameLen uint32
	if err := binary.Read(conn, binary.BigEndian, &nameLen); err != nil {
		return "", 0, err
	}

	nameBuf := make([]byte, nameLen)
	if _, err := io.ReadFull(conn, nameBuf); err != nil {
		return "", 0, err
	}
	exportName := string(nameBuf)

	isoPath := filepath.Join(s.isoDir, exportName)
	stat, err := os.Stat(isoPath)
	if err != nil {
		return "", 0, fmt.Errorf("ISO not found: %s", exportName)
	}

	if err := binary.Write(conn, binary.BigEndian, uint64(NBD_REPLY_MAGIC)); err != nil {
		return "", 0, err
	}
	if err := binary.Write(conn, binary.BigEndian, uint32(NBD_OPT_EXPORT)); err != nil {
		return "", 0, err
	}
	if err := binary.Write(conn, binary.BigEndian, uint32(NBD_REP_ACK)); err != nil {
		return "", 0, err
	}
	if err := binary.Write(conn, binary.BigEndian, uint32(0)); err != nil {
		return "", 0, err
	}

	if err := binary.Write(conn, binary.BigEndian, uint64(stat.Size())); err != nil {
		return "", 0, err
	}

	transmissionFlags := uint16(1)
	if err := binary.Write(conn, binary.BigEndian, transmissionFlags); err != nil {
		return "", 0, err
	}

	return isoPath, stat.Size(), nil
}

func (s *Server) handleRequests(client *Client) error {
	for {
		var req struct {
			Magic  uint32
			Type   uint32
			Handle uint64
			Offset uint64
			Length uint32
		}

		if err := binary.Read(client.conn, binary.BigEndian, &req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		if req.Magic != NBD_REQUEST_MAGIC {
			return fmt.Errorf("invalid request magic")
		}

		switch req.Type {
		case NBD_CMD_READ:
			if err := s.handleRead(client, req.Handle, req.Offset, req.Length); err != nil {
				return err
			}
		case NBD_CMD_DISC:
			return nil
		default:
			s.sendReply(client, req.Handle, 1)
		}
	}
}

func (s *Server) handleRead(client *Client, handle uint64, offset uint64, length uint32) error {
	data := make([]byte, length)
	n, err := client.file.ReadAt(data, int64(offset))
	if err != nil && err != io.EOF {
		return s.sendReply(client, handle, 1)
	}

	if err := s.sendReply(client, handle, 0); err != nil {
		return err
	}

	_, err = client.conn.Write(data[:n])
	return err
}

func (s *Server) sendReply(client *Client, handle uint64, errCode uint32) error {
	reply := struct {
		Magic  uint32
		Error  uint32
		Handle uint64
	}{
		Magic:  NBD_REPLY_MAGIC,
		Error:  errCode,
		Handle: handle,
	}

	return binary.Write(client.conn, binary.BigEndian, &reply)
}

func (s *Server) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}
