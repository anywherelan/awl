package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/cli"
)

func main() {
	cli.New().Run()

	app := awl.New()
	logger := app.SetupLoggerAndConfig()
	ctx, ctxCancel := context.WithCancel(context.Background())

	err := app.Init(ctx, nil)
	if err != nil {
		logger.Fatalf("failed to init server: %v", err)
	}
	app.Api.SetupFrontend(awl.FrontendStatic())

	quit := make(chan os.Signal, 2)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	logger.Infof("received signal %s", <-quit)
	finishedCh := make(chan struct{})
	go func() {
		select {
		case <-time.After(3 * time.Second):
			logger.Fatal("exit timeout reached: terminating")
		case <-finishedCh:
			// ok
		case sig := <-quit:
			logger.Fatalf("duplicate exit signal %s: terminating", sig)
		}
	}()
	ctxCancel()
	app.Close()

	finishedCh <- struct{}{}
	logger.Info("exited normally")
}
