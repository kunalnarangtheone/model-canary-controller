package main

import (
	"context"
	"sync"
	"time"

	common "github.com/kunal-narang/model-canary-controller/common/proto/generated"
	controller "github.com/kunal-narang/model-canary-controller/services/controller/proto/generated"
)

// Config holds all runtime parameters for a run.
type Config struct {
	ExperimentID string
	RPS          float64
	Concurrency  int
	Client       controller.ControllerServiceClient
	Prompts      []Prompt
	Log          *Logger
	Metrics      *Metrics
}

// Run sends prompts at the configured RPS until ctx is cancelled.
// It blocks until all in-flight RPCs have returned and returns a summary.
func Run(ctx context.Context, cfg Config) Summary {
	queue := make(chan Prompt) // unbuffered; senders block until feeder sends
	var wg sync.WaitGroup

	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sendLoop(ctx, cfg, queue)
		}()
	}

	feeder(ctx, cfg.Prompts, cfg.RPS, queue)
	close(queue)
	wg.Wait()

	return Summary{Sent: cfg.Log.seq}
}

// Summary contains aggregate stats printed at shutdown.
type Summary struct {
	Sent int64
}

// feeder pushes prompts round-robin into queue at the target RPS. It exits when
// ctx is cancelled, which causes the queue channel to be closed by Run.
func feeder(ctx context.Context, prompts []Prompt, rps float64, queue chan<- Prompt) {
	if rps <= 0 {
		rps = 1
	}
	interval := time.Duration(float64(time.Second) / rps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p := prompts[i%len(prompts)]
			i++
			select {
			case queue <- p:
			case <-ctx.Done():
				return
			}
		}
	}
}

// sendLoop pulls prompts from queue, calls Infer, logs and records metrics.
func sendLoop(ctx context.Context, cfg Config, queue <-chan Prompt) {
	for {
		select {
		case <-ctx.Done():
			// drain remaining items that the feeder already pushed
			for range queue {
			}
			return
		case p, ok := <-queue:
			if !ok {
				return
			}
			send(ctx, cfg, p)
		}
	}
}

func send(ctx context.Context, cfg Config, p Prompt) {
	req := &common.InferRequest{
		PromptId:   p.ID,
		PromptText: p.Text,
		// Variant intentionally left as VARIANT_UNSPECIFIED (0).
	}

	start := time.Now()
	resp, err := cfg.Client.Infer(ctx, req)
	wallMs := time.Since(start).Milliseconds()

	line := LogLine{
		ExperimentID: cfg.ExperimentID,
		PromptID:     p.ID,
		PromptType:   p.Type,
		WallMs:       wallMs,
	}

	if err != nil {
		line.Error = err.Error()
		line.Variant = "UNKNOWN"
		_ = cfg.Log.Write(line)
		cfg.Metrics.RecordError(p.Type, "UNKNOWN", "transport")
		return
	}

	variantStr := resp.Variant.String()
	line.Variant = variantStr
	line.ResponseText = resp.ResponseText
	line.InputTokens = resp.InputTokens
	line.OutputTokens = resp.OutputTokens
	line.LatencyMs = resp.LatencyMs
	line.Refused = resp.Refused

	if resp.Error != "" {
		line.Error = resp.Error
		_ = cfg.Log.Write(line)
		cfg.Metrics.RecordError(p.Type, variantStr, "worker")
		return
	}

	_ = cfg.Log.Write(line)

	if resp.Refused {
		cfg.Metrics.RecordRefusal(p.Type, variantStr, float64(wallMs))
	} else {
		cfg.Metrics.RecordOK(p.Type, variantStr, float64(wallMs))
	}
}
