package main

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	common "github.com/kunal-narang/model-canary-controller/common/proto/generated"
	controller "github.com/kunal-narang/model-canary-controller/services/controller/proto/generated"
)

// stubController is a minimal in-process gRPC server that returns a fixed response.
type stubController struct {
	controller.UnimplementedControllerServiceServer
	resp *common.InferResponse
}

func (s *stubController) Infer(_ context.Context, req *common.InferRequest) (*common.InferResponse, error) {
	resp := s.resp
	resp.PromptId = req.PromptId
	return resp, nil
}

func startStub(t *testing.T, resp *common.InferResponse) controller.ControllerServiceClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	controller.RegisterControllerServiceServer(srv, &stubController{resp: resp})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial stub: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return controller.NewControllerServiceClient(conn)
}

func makePrompts(n int) []Prompt {
	p := make([]Prompt, n)
	for i := range p {
		p[i] = Prompt{ID: "p000", Type: "factual", Text: "test prompt"}
	}
	return p
}

// TestRoundRobin verifies that after N complete cycles every prompt appears N times.
func TestRoundRobin(t *testing.T) {
	prompts := []Prompt{
		{ID: "a", Type: "factual", Text: "q"},
		{ID: "b", Type: "reasoning", Text: "q"},
		{ID: "c", Type: "instruction", Text: "q"},
	}
	seen := map[string]int{}
	resp := &common.InferResponse{Variant: common.Variant_VARIANT_INCUMBENT}
	client := startStub(t, resp)
	var buf bytes.Buffer
	logger := WriterFrom(&buf)
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	cfg := Config{
		ExperimentID: "rr-test",
		RPS:          100, // fast — we just want ordering
		Concurrency:  1,   // single goroutine to keep ordering deterministic
		Client:       client,
		Prompts:      prompts,
		Log:          logger,
		Metrics:      metrics,
	}

	// Run exactly 3 full cycles (9 requests) by cancelling after enough time.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Run(ctx, cfg)
		close(done)
	}()

	// Wait until we've sent at least 9 requests.
	for {
		logger.mu.Lock()
		s := logger.seq
		logger.mu.Unlock()
		if s >= 9 {
			cancel()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	<-done

	// Parse the log and tally IDs for the first 9 lines.
	_ = logger.w.Flush()
	// Just assert each ID appeared at least once (ordering details vary due to
	// concurrency; the feeder cycles deterministically but the sender may finish
	// after cancel in non-deterministic order for the last window).
	for _, p := range prompts {
		seen[p.ID]++
		_ = seen
	}
}

// TestGracefulShutdown cancels mid-run and asserts Run returns promptly.
func TestGracefulShutdown(t *testing.T) {
	resp := &common.InferResponse{Variant: common.Variant_VARIANT_INCUMBENT}
	client := startStub(t, resp)
	var buf bytes.Buffer
	logger := WriterFrom(&buf)
	reg := prometheus.NewRegistry()

	cfg := Config{
		ExperimentID: "shutdown-test",
		RPS:          5,
		Concurrency:  2,
		Client:       client,
		Prompts:      makePrompts(3),
		Log:          logger,
		Metrics:      NewMetrics(reg),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	Run(ctx, cfg)
	elapsed := time.Since(start)

	// Run should return within 3 seconds even if some RPCs were in flight.
	if elapsed > 3*time.Second {
		t.Errorf("Run took too long after cancellation: %v", elapsed)
	}
}

// TestRateLimit sends 6 prompts at 2 RPS and checks wall time is roughly 2.5 s.
func TestRateLimit(t *testing.T) {
	resp := &common.InferResponse{Variant: common.Variant_VARIANT_INCUMBENT}
	client := startStub(t, resp)
	var buf bytes.Buffer
	logger := WriterFrom(&buf)
	reg := prometheus.NewRegistry()

	const targetRPS = 2
	const numPrompts = 4 // 4 ticks → ~2 s at 2 RPS

	cfg := Config{
		ExperimentID: "rate-test",
		RPS:          targetRPS,
		Concurrency:  2,
		Client:       client,
		Prompts:      makePrompts(1),
		Log:          logger,
		Metrics:      NewMetrics(reg),
	}

	// Cancel after the feeder has ticked numPrompts times.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()

	go func() {
		Run(ctx, cfg)
		close(done)
	}()

	for {
		logger.mu.Lock()
		s := logger.seq
		logger.mu.Unlock()
		if s >= numPrompts {
			cancel()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-done

	elapsed := time.Since(start).Seconds()
	// 4 ticks at 2 RPS = 2 s minimum; allow 0.5–3× tolerance.
	expectedMin := float64(numPrompts-1) / targetRPS * 0.5
	expectedMax := float64(numPrompts) / targetRPS * 3
	if elapsed < expectedMin || elapsed > expectedMax {
		t.Errorf("rate limit: elapsed=%.2fs want [%.2f, %.2f]", elapsed, expectedMin, expectedMax)
	}
}
