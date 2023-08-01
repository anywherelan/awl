package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/anywherelan/awl"
	"github.com/anywherelan/awl/cli"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/update"
	"github.com/ipfs/go-log/v2"
)

func main() {
	cli.New(update.AppTypeAwl).Run()

	uid := os.Geteuid()
	if uid == 0 {
		// this is required to allow listening on config.AdminHttpServerIP address
		//nolint:gosec
		err := exec.Command("ifconfig", "lo0", "alias", config.AdminHttpServerIP, "up").Run()
		if err != nil {
			fmt.Printf("error: `ifconfig lo0 alias %s up`: %v\n", config.AdminHttpServerIP, err)
		}
	}

	app := awl.New()
	logger := app.SetupLoggerAndConfig()
	ctx, ctxCancel := context.WithCancel(context.Background())

	err := app.Init(ctx, nil)
	if err != nil {
		logger.Fatalf("failed to init server: %v", err)
	}
	app.Api.SetupFrontend(awl.FrontendStatic())

	if app.Conf.Update.TrayAutoCheckEnabled {
		go func() {
			if config.IsDevVersion() {
				logger.Info("updates auto check is disabled for dev version")
				return
			}
			interval, err := time.ParseDuration(app.Conf.Update.TrayAutoCheckInterval)
			if err != nil {
				logger.Errorf("update auto check: interval parse: %v", err)
				return
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			checkForUpdates(app.Conf, logger)
			for range ticker.C {
				checkForUpdates(app.Conf, logger)
			}
		}()
	}

	quit := make(chan os.Signal, 2)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	logger.Infof("received exit signal '%s'", <-quit)
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

func checkForUpdates(conf *config.Config, logger *log.ZapEventLogger) {
	updService, err := update.NewUpdateService(conf, logger, update.AppTypeAwl)
	if err != nil {
		logger.Errorf("update auto check: creating update service: %v", err)
		return
	}
	updStatus, err := updService.CheckForUpdates()
	if err != nil {
		logger.Errorf("update auto check: check for updates: %v", err)
		return
	}
	if !updStatus {
		return
	}

	logger.Infof("New version available: %s, current version: %s", updService.NewVersion.VersionTag(), config.Version)
}
