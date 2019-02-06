// Input - acts as a bridge between cybermon and cherami.
// cherami currently does not have any lua library so This
// bridge handles TCP connections and spits messages seperated
// by a new line into a configurable number of cherami queues
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dt "github.com/trustnetworks/analytics-common/datatypes"
	"github.com/trustnetworks/analytics-common/utils"
	"github.com/trustnetworks/analytics-common/worker"
)

const (
	PORT  = "48879"
	PROTO = "tcp"

	pgm = "input"
)

// Listener Service
type Service struct {
	ch        chan bool
	waitGroup *sync.WaitGroup
	worker    *worker.Worker

	eventLatency *prometheus.SummaryVec
	recvLabels   prometheus.Labels
}

// Make a new Service.
func NewService(outputs []string) (*Service, error) {

	var w worker.Worker

	err := w.Initialise(outputs)
	if err != nil {
		utils.Log("ERROR: Failed to init: %s", err.Error())
		return nil, err
	}

	s := &Service{
		ch:        make(chan bool),
		waitGroup: &sync.WaitGroup{},
		worker:    &w,
	}
	s.waitGroup.Add(1)
	return s, nil
}

// Accept connections and spawn a goroutine to serve each one.  Stop listening
// if anything is received on the service's channel.
func (s *Service) Serve(listener *net.TCPListener) {
	defer s.waitGroup.Done()
	for {
		select {
		case <-s.ch:
			utils.Log("INFO: Stopping listener on: %s", listener.Addr())
			listener.Close()
			return
		default:
		}
		listener.SetDeadline(time.Now().Add(1e9))
		conn, err := listener.AcceptTCP()
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
				continue
			}
			utils.Log("ERROR: Failed to start TCP Connection: %s", err.Error())
		}
		utils.Log("INFO: Connected to address: %s", conn.RemoteAddr())
		s.waitGroup.Add(1)
		go s.serve(conn)
	}
}

// Stop the service by closing the service's channel.  Block until the service
// is really stopped.
func (s *Service) Stop() {
	close(s.ch)
	s.waitGroup.Wait()
}

// Serve a connection by reading to the newline and then sending
// it off to the cherami worker for output
func (s *Service) serve(conn *net.TCPConn) {
	defer conn.Close()
	defer s.waitGroup.Done()
	reader := bufio.NewReader(conn)
	sample := 0
	for {
		select {
		case <-s.ch:
			utils.Log("INFO: Disconnecting from: %s", conn.RemoteAddr())
			return
		default:
		}
		conn.SetDeadline(time.Now().Add(1e9))
		msg, err := reader.ReadBytes('\n')
		ts := time.Now().UnixNano()

		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
				continue
			}
			utils.Log("WARN: Unable to read from connection: %s, %s", conn.RemoteAddr(), err.Error())
			return
		}
		sample++
		if sample == 10 {
			go s.recordLatency(msg, ts)
			sample = 0
		}
		s.worker.Send("output", msg)
	}
}

func (s *Service) recordLatency(msg []uint8, ts int64) {

	var e dt.Event

	// Convert JSON object to internal object.
	err := json.Unmarshal(msg, &e)
	if err != nil {
		utils.Log("WARN: Unable to log latency, couldn't unmarshal json: %s", err.Error())
		return
	}
	eTime, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		utils.Log("Date Parse Error: %s", err.Error())
	}
	latency := ts - eTime.UnixNano()
	if(latency > 1000000000) {
		utils.Log("WARN: Latency of %d ms for event id: %s", latency/1000000, e.Id)
	}
	s.eventLatency.With(s.recvLabels).Observe(float64(latency))
}

func main() {
	utils.LogPgm = pgm

	// Defaults to listen on 127.0.0.1:48879.  That's my favorite port
	// number because in hex 48879 is 0xBEEF.
	port := utils.Getenv("TCP_PORT", PORT)
	var outputs []string
	if len(os.Args) > 0 {
		outputs = os.Args[1:]
	} else {
		utils.Log("ERROR: No outputs defined. You need to define at least one")
		return
	}
	laddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		utils.Log("ERROR: Failed to resolve address: %s", err.Error())
		return
	}
	listener, err := net.ListenTCP(PROTO, laddr)
	if err != nil {
		utils.Log("ERROR: Failed to listen on address: %s", err.Error())
		return
	}
	utils.Log("INFO: Listening on: %s", listener.Addr())

	// Make a new service and send it into the background.
	service, err := NewService(outputs)
	if err != nil {
		return
	}
	go service.Serve(listener)

	// server prometheus metrics
	service.recvLabels = prometheus.Labels{"store": "trust-networks"}
	service.eventLatency = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name: "event_latency",
			Help: "Latency from cyberprobe to store",
		},
		[]string{"store"},
	)

	prometheus.MustRegister(service.eventLatency)
	service.eventLatency.With(service.recvLabels).Observe(float64(0)) // default the value to 0
	utils.Log("INFO: Starting prometheus metrics on :8080")
	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(":8080", nil)

	// Handle SIGINT and SIGTERM.
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	utils.Log("INFO: Received signal: %s", <-ch)

	// Stop the service gracefully.
	service.Stop()
}
