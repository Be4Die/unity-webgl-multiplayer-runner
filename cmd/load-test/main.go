package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v3/process"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

var (
	clients     = flag.Int("clients", 0, "Number of clients to start")
	stepClients = flag.Int("step-clients", 1, "Number of clients per step")
	stepDelay   = flag.Int("step-delay", 1000, "Delay between steps in milliseconds")
	clientName  = flag.String("client-name", "client.exe", "Client executable name")
	serverName  = flag.String("server-name", "", "Server process name")
)

type Metric struct {
	Timestamp   time.Time
	Clients     int
	CPUPercent  float64
	RAMBytes    uint64
}

type ProcessManager struct {
	processes []*os.Process
	mutex     sync.Mutex
}

func (pm *ProcessManager) AddProcess(p *os.Process) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	pm.processes = append(pm.processes, p)
}

func (pm *ProcessManager) KillAll() {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	for _, p := range pm.processes {
		p.Kill()
	}
}

func main() {
	flag.Parse()
	startTime := time.Now()

	// Find server process
	serverPID, err := findServerPID()
	if err != nil {
		log.Fatal(err)
	}

	// Metrics collection
	var metrics []Metric
	metricsMutex := &sync.Mutex{}
	activeClients := 0

	// Process manager
	pm := &ProcessManager{}

	// Start metrics collector
	go collectMetrics(serverPID, &activeClients, metricsMutex, &metrics)

	// Handle interrupts
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go handleInterrupt(signals, pm, &metrics, startTime)

	// Start clients in steps
	var wg sync.WaitGroup
	for i := 0; i < *clients; i += *stepClients {
	    count := *stepClients
	    if remaining := *clients - i; remaining < count {
	        count = remaining
	    }
	
	    for j := 0; j < count; j++ {
	        wg.Add(1)
	        go func() {
	            defer wg.Done()
	            // Добавлены аргументы командной строки
	            cmd := exec.Command(*clientName, "-batchmode", "-nographics")
	            if err := cmd.Start(); err != nil {
	                log.Printf("Error starting client: %v", err)
	                return
	            }
	            pm.AddProcess(cmd.Process)
	            metricsMutex.Lock()
	            activeClients++
	            metricsMutex.Unlock()
	            cmd.Wait()
	        }()
	    }
	
	    time.Sleep(time.Duration(*stepDelay) * time.Millisecond)
	}

	wg.Wait()
	generateGraphs(metrics, startTime)
}

func findServerPID() (int32, error) {
	processes, _ := process.Processes()
	for _, p := range processes {
		name, _ := p.Name()
		if name == *serverName {
			return p.Pid, nil
		}
	}
	return 0, fmt.Errorf("server process '%s' not found", *serverName)
}

func collectMetrics(serverPID int32, activeClients *int, mutex *sync.Mutex, metrics *[]Metric) {
	p, _ := process.NewProcess(serverPID)
	for {
		cpu, _ := p.CPUPercent()
		mem, _ := p.MemoryInfo()

		mutex.Lock()
		*metrics = append(*metrics, Metric{
			Timestamp:   time.Now(),
			Clients:     *activeClients,
			CPUPercent:  cpu,
			RAMBytes:    mem.RSS,
		})
		mutex.Unlock()

		time.Sleep(1 * time.Second)
	}
}

func handleInterrupt(signals chan os.Signal, pm *ProcessManager, metrics *[]Metric, startTime time.Time) {
	<-signals
	log.Println("\nReceived interrupt, terminating clients...")
	pm.KillAll()
	log.Println("Generating graphs...")
	generateGraphs(*metrics, startTime)
	os.Exit(0)
}

func generateGraphs(metrics []Metric, startTime time.Time) {
	createPlot("CPU Usage Over Time", "Time (s)", "CPU (%)", metrics, func(m Metric) (x, y float64) {
		return m.Timestamp.Sub(startTime).Seconds(), m.CPUPercent
	}, "cpu_vs_time.png")

	createPlot("RAM Usage Over Time", "Time (s)", "RAM (MB)", metrics, func(m Metric) (x, y float64) {
		return m.Timestamp.Sub(startTime).Seconds(), float64(m.RAMBytes) / 1024 / 1024
	}, "ram_vs_time.png")

	createPlot("CPU Usage vs Clients", "Clients", "CPU (%)", metrics, func(m Metric) (x, y float64) {
		return float64(m.Clients), m.CPUPercent
	}, "cpu_vs_clients.png")

	createPlot("RAM Usage vs Clients", "Clients", "RAM (MB)", metrics, func(m Metric) (x, y float64) {
		return float64(m.Clients), float64(m.RAMBytes) / 1024 / 1024
	}, "ram_vs_clients.png")

	createPlot("Clients Over Time", "Time (s)", "Clients", metrics, func(m Metric) (x, y float64) {
		return m.Timestamp.Sub(startTime).Seconds(), float64(m.Clients)
	}, "clients_vs_time.png")
}

func createPlot(title, xLabel, yLabel string, metrics []Metric, xyFunc func(Metric) (x, y float64), filename string) {
	p := plot.New()
	p.Title.Text = title
	p.X.Label.Text = xLabel
	p.Y.Label.Text = yLabel

	points := make(plotter.XYs, len(metrics))
	for i, m := range metrics {
		points[i].X, points[i].Y = xyFunc(m)
	}

	line, _ := plotter.NewLine(points)
	p.Add(line)

	if err := p.Save(10*vg.Inch, 6*vg.Inch, filename); err != nil {
		log.Printf("Error saving plot %s: %v", filename, err)
	}
}