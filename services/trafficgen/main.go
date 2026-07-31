package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	logger "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	controller "github.com/kunal-narang/model-canary-controller/services/controller/proto/generated"
)

func main() {
	controllerAddr := flag.String("controller-addr", "localhost:50051", "gRPC address of ControllerService")
	promptsFile := flag.String("prompts-file", "data/prompts.json", "path to prompts JSON file")
	rps := flag.Float64("rps", 5, "target requests per second")
	duration := flag.Int("duration", 0, "run for N seconds then stop; 0 = run until interrupted")
	concurrency := flag.Int("concurrency", 4, "number of parallel sender goroutines")
	logFile := flag.String("log-file", "", "output JSONL log path; '-' for stdout (default: results/<experiment-id>_<ts>.jsonl)")
	experimentID := flag.String("experiment-id", "experiment", "experiment identifier embedded in every log line")
	metricsAddr := flag.String("metrics-addr", "0.0.0.0:9101", "Prometheus scrape endpoint address")
	promptTypes := flag.String("prompt-types", "", "comma-separated prompt types to include; empty = all")
	flag.Parse()

	// Resolve log file path.
	logPath := *logFile
	if logPath == "" {
		if err := os.MkdirAll("results", 0o750); err != nil {
			logger.WithError(err).Fatal("create results dir")
		}
		ts := time.Now().UTC().Format("20060102T150405Z")
		logPath = filepath.Join("results", fmt.Sprintf("%s_%s.jsonl", *experimentID, ts))
	}

	// Load prompts.
	var types []string
	if *promptTypes != "" {
		for t := range strings.SplitSeq(*promptTypes, ",") {
			if s := strings.TrimSpace(t); s != "" {
				types = append(types, s)
			}
		}
	}
	prompts, err := LoadPrompts(*promptsFile, types)
	if err != nil {
		logger.WithError(err).Fatal("load prompts")
	}
	logger.WithFields(logger.Fields{"count": len(prompts), "file": *promptsFile}).Info("loaded prompts")

	// Open log writer.
	jsonlLog, err := NewLogger(logPath)
	if err != nil {
		logger.WithError(err).Fatal("open log")
	}
	defer func() {
		if err := jsonlLog.Close(); err != nil {
			logger.WithError(err).Error("close log")
		}
	}()

	// Register Prometheus metrics on a private registry (keeps the default
	// registry clean if the process is embedded in a test harness).
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	// Start Prometheus HTTP server.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	metricsSrv := &http.Server{Addr: *metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Error("metrics server")
		}
	}()
	logger.WithField("addr", "http://"+*metricsAddr+"/metrics").Info("prometheus metrics listening")

	// Dial controller with retry logic: attempt connection for up to 30 s.
	conn, err := dialWithRetry(*controllerAddr, 30*time.Second)
	if err != nil {
		logger.WithError(err).WithField("addr", *controllerAddr).Fatal("connect to controller")
	}
	defer func() {
		if err := conn.Close(); err != nil {
			logger.WithError(err).Error("close connection")
		}
	}()

	client := controller.NewControllerServiceClient(conn)
	logger.WithField("addr", *controllerAddr).Info("connected to controller")

	// Build a cancellable context; honour --duration if set.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if *duration > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*duration)*time.Second)
		defer cancel()
	}

	// Listen for OS signals to trigger graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.WithField("signal", sig).Info("shutting down")
		cancel()
	}()

	cfg := Config{
		ExperimentID: *experimentID,
		RPS:          *rps,
		Concurrency:  *concurrency,
		Client:       client,
		Prompts:      prompts,
		Log:          jsonlLog,
		Metrics:      metrics,
	}

	logger.WithFields(logger.Fields{
		"experiment_id": *experimentID,
		"rps":           *rps,
		"concurrency":   *concurrency,
	}).Info("starting experiment")
	start := time.Now()

	// Run blocks until ctx is cancelled, all in-flight RPCs drain, and the log
	// is ready to be flushed (flush happens in the deferred logger.Close above).
	summary := Run(ctx, cfg)

	elapsed := time.Since(start).Seconds()
	actualRPS := 0.0
	if elapsed > 0 {
		actualRPS = float64(summary.Sent) / elapsed
	}

	// Shut down the metrics server; ignore timeout errors during exit.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutCancel()
	_ = metricsSrv.Shutdown(shutCtx)

	logger.WithFields(logger.Fields{
		"sent":       summary.Sent,
		"elapsed_s":  fmt.Sprintf("%.1f", elapsed),
		"actual_rps": fmt.Sprintf("%.2f", actualRPS),
	}).Info("done")
}

// dialWithRetry establishes a gRPC connection, retrying until timeout.
func dialWithRetry(addr string, timeout time.Duration) (*grpc.ClientConn, error) {
	deadline := time.Now().Add(timeout)
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(), //nolint:staticcheck
	}

	for {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		conn, err := grpc.DialContext(ctx, addr, opts...) //nolint:staticcheck
		cancel()
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s: %w", timeout, err)
		}
		logger.WithError(err).Warn("controller not yet reachable, retrying")
		time.Sleep(2 * time.Second)
	}
}
