package llmobservability

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var (
	signalNotify = func(ch chan os.Signal, signals ...os.Signal) {
		signal.Notify(ch, signals...)
	}
	signalStop = func(ch chan os.Signal) {
		signal.Stop(ch)
	}
)

// RegisterSignalShutdown closes the client when the process receives a shutdown signal.
// It does not replace an explicit defer client.Close(...), but it helps protect
// fire-and-forget telemetry in short-lived processes that exit on SIGINT or SIGTERM.
func RegisterSignalShutdown(client Client, timeout time.Duration, signals ...os.Signal) func() {
	if client == nil {
		return func() {}
	}
	if len(signals) == 0 {
		signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	var once sync.Once
	finalize := func(shouldClose bool) {
		once.Do(func() {
			if shouldClose {
				client.Close(timeout)
			}
			close(done)
			signalStop(ch)
		})
	}
	signalNotify(ch, signals...)
	go func() {
		select {
		case <-ch:
			finalize(true)
		case <-done:
			finalize(false)
		}
	}()
	return func() {
		finalize(false)
	}
}
