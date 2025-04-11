package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/process"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/plotutil"
	"gonum.org/v1/plot/vg"
)

type Config struct {
	ServerMode     bool
	Port           int
	LdtClients     int
	LdtStep        int
	ConnectTimeout time.Duration
	UpdateInterval time.Duration
	ServerPID      int
	ServerURL      string
}

type Metrics struct {
	Timestamp time.Time
	CpuUsage  float64
	RamUsage  float64
	Clients   int
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	cfg := parseFlags()

	if cfg.ServerMode {
		runServer(cfg)
	} else if cfg.LdtClients > 0 {
		runLoadTest(cfg)
	} else {
		fmt.Println("Please specify mode: --serv or --ldt-clients")
	}
}

func parseFlags() *Config {
	cfg := &Config{}
	flag.BoolVar(&cfg.ServerMode, "serv", false, "Run HTTP server")
	flag.IntVar(&cfg.Port, "port", 0, "Server port (default: random)")
	flag.IntVar(&cfg.LdtClients, "ldt-clients", 0, "Number of load test clients")
	flag.IntVar(&cfg.LdtStep, "ldt-step", 10, "Clients connection step")
	flag.Parse()

	cfg.ConnectTimeout = 30 * time.Second
	cfg.UpdateInterval = 1 * time.Second
	return cfg
}

func runServer(cfg *Config) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	cfg.Port = listener.Addr().(*net.TCPAddr).Port
	cfg.ServerURL = fmt.Sprintf("ws://localhost:%d", cfg.Port)

	fmt.Printf("Server running at http://localhost:%d\n", cfg.Port)

	http.HandleFunc("/", fileHandler)
	http.HandleFunc("/ws", wsHandler)
	http.Serve(listener, nil)
}

func fileHandler(w http.ResponseWriter, r *http.Request) {
	path := "." + r.URL.Path

	file, err := os.Open(path)
	if err != nil {
		serveFallback(w, r)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		serveFallback(w, r)
		return
	}

	switch {
	case strings.HasSuffix(path, ".js.gz"):
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Encoding", "gzip")
	case strings.HasSuffix(path, ".wasm.gz"):
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("Content-Encoding", "gzip")
	case strings.HasSuffix(path, ".gz"):
		w.Header().Set("Content-Encoding", "gzip")
		baseExt := filepath.Ext(strings.TrimSuffix(path, ".gz"))
		if mimeType := mime.TypeByExtension(baseExt); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
	}

	io.Copy(w, file)
}

func serveFallback(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.Dir(".")).ServeHTTP(w, r)
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("Upgrade error:", err)
		return
	}
	defer conn.Close()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}
		conn.WriteMessage(websocket.TextMessage, message)
	}
}

func runLoadTest(cfg *Config) {
	if cfg.ServerPID == 0 {
		cfg.ServerPID = findServerPID()
	}

	metricsCh := make(chan Metrics)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		collectMetrics(cfg, metricsCh)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		generateLoad(cfg, metricsCh, &wg)
	}()

	wg.Wait()
	generateGraphs()
}

func generateLoad(cfg *Config, metricsCh chan<- Metrics, wg *sync.WaitGroup) {
	clientSem := make(chan struct{}, cfg.LdtClients)
	var activeClients int
	var mu sync.Mutex

	ticker := time.NewTicker(cfg.UpdateInterval)
	defer ticker.Stop()

	for i := 0; i < cfg.LdtClients; i += cfg.LdtStep {
		for j := 0; j < cfg.LdtStep; j++ {
			clientSem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() { <-clientSem; wg.Done() }()
				conn, _, err := websocket.DefaultDialer.Dial(cfg.ServerURL, nil)
				if err != nil {
					return
				}
				defer conn.Close()

				mu.Lock()
				activeClients++
				mu.Unlock()

				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()
				for range ticker.C {
					conn.WriteMessage(websocket.TextMessage, []byte("ping"))
				}
			}()
		}

		select {
		case <-ticker.C:
			mu.Lock()
			metricsCh <- Metrics{
				Timestamp: time.Now(),
				Clients:   activeClients,
			}
			mu.Unlock()
		case <-time.After(cfg.ConnectTimeout):
			log.Fatal("Connection timeout exceeded")
		}
	}
}

func collectMetrics(cfg *Config, metricsCh <-chan Metrics) {
	proc, _ := process.NewProcess(int32(cfg.ServerPID))
	file, _ := os.Create("metrics.csv")
	defer file.Close()

	writer := csv.NewWriter(file)
	writer.Write([]string{"timestamp", "cpu", "ram", "clients"})

	for metric := range metricsCh {
		cpu, _ := proc.Percent(0)
		ram, _ := proc.MemoryPercent()

		metric.CpuUsage = cpu
		metric.RamUsage = float64(ram) // Преобразование float32 в float64

		writer.Write([]string{
			metric.Timestamp.Format(time.RFC3339),
			fmt.Sprintf("%.2f", cpu),
			fmt.Sprintf("%.2f", metric.RamUsage),
			strconv.Itoa(metric.Clients),
		})
		writer.Flush()
	}
}

func findServerPID() int {
	processes, _ := process.Processes()
	for _, p := range processes {
		name, _ := p.Name()
		if runtime.GOOS == "windows" {
			if name == "server.exe" {
				return int(p.Pid)
			}
		} else {
			if name == "server" {
				return int(p.Pid)
			}
		}
	}
	log.Fatal("Server process not found")
	return 0
}

func generateGraphs() {
	data := readCSV("metrics.csv")

	// 1. CPU vs Time
	createPlot("CPU Load", "Time", "CPU (%)", data, func(m Metrics) float64 {
		return m.CpuUsage
	}).Save(4*vg.Inch, 4*vg.Inch, "cpu_time.png")

	// 2. RAM vs Time
	createPlot("RAM Usage", "Time", "RAM (%)", data, func(m Metrics) float64 {
		return m.RamUsage
	}).Save(4*vg.Inch, 4*vg.Inch, "ram_time.png")

	// 3. CPU vs Clients
	createScatter("CPU vs Clients", "Clients", "CPU (%)", data).Save(4*vg.Inch, 4*vg.Inch, "cpu_clients.png")
}

func readCSV(path string) []Metrics {
	file, _ := os.Open(path)
	defer file.Close()

	r := csv.NewReader(file)
	records, _ := r.ReadAll()

	var data []Metrics
	for _, record := range records[1:] { // Skip header
		t, _ := time.Parse(time.RFC3339, record[0])
		cpu, _ := strconv.ParseFloat(record[1], 64)
		ram, _ := strconv.ParseFloat(record[2], 64)
		clients, _ := strconv.Atoi(record[3])

		data = append(data, Metrics{
			Timestamp: t,
			CpuUsage:  cpu,
			RamUsage:  ram,
			Clients:   clients,
		})
	}
	return data
}

func createPlot(title, xLabel, yLabel string, data []Metrics, yVal func(Metrics) float64) *plot.Plot {
	p := plot.New()
	p.Title.Text = title
	p.X.Label.Text = xLabel
	p.Y.Label.Text = yLabel

	pts := make(plotter.XYs, len(data))
	for i, m := range data {
		pts[i].X = float64(m.Timestamp.Unix())
		pts[i].Y = yVal(m)
	}

	plotutil.AddLinePoints(p, title, pts)
	p.X.Tick.Marker = plot.TimeTicks{Format: "15:04:05"}
	return p
}

func createScatter(title, xLabel, yLabel string, data []Metrics) *plot.Plot {
	p := plot.New()
	p.Title.Text = title
	p.X.Label.Text = xLabel
	p.Y.Label.Text = yLabel

	pts := make(plotter.XYs, len(data))
	for i, m := range data {
		pts[i].X = float64(m.Clients)
		pts[i].Y = m.CpuUsage
	}

	s, _ := plotter.NewScatter(pts)
	p.Add(s)
	return p
}