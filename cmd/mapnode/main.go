package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	mapapi "github.com/justinzjj/TrustMap_prototype/Mapnode/api"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/app"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/bootstrap"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(arguments []string, stderr io.Writer) int {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "serve":
			return runServe(arguments[1:], stderr)
		case "replay":
			return runReplay(arguments[1:], stderr)
		default:
			if arguments[0] != "" && arguments[0][0] != '-' {
				fmt.Fprintf(stderr, "unknown command %q\n", arguments[0])
				return 2
			}
		}
	}
	return runServe(arguments, stderr)
}

func runServe(arguments []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("mapnode", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", os.Getenv("MAPNODE_CONFIG"), "path to the MapNode JSON configuration")
	healthURL := flags.String("healthcheck", "", "check an HTTP health endpoint and exit")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	explicitConfig := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "config" {
			explicitConfig = true
		}
	})
	if *healthURL != "" {
		if explicitConfig {
			fmt.Fprintln(stderr, "--config and --healthcheck are mutually exclusive")
			return 2
		}
		if err := bootstrap.CheckHealth(*healthURL); err != nil {
			fmt.Fprintf(stderr, "mapnode healthcheck failed: %v\n", err)
			return 1
		}
		return 0
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "--config is required")
		return 2
	}
	config, manifest, err := bootstrap.LoadValidated(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mapnode bootstrap failed: %v\n", err)
		return 1
	}
	runtimeContext, cancelRuntimeValidation := context.WithTimeout(context.Background(), 10*time.Second)
	application, err := app.Open(runtimeContext, config, manifest)
	cancelRuntimeValidation()
	if err != nil {
		fmt.Fprintf(stderr, "mapnode runtime validation failed during Phase 3 startup: %v\n", err)
		return 1
	}
	defer application.Close()

	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	logger.Printf("mapnode=%s chain_id=%s confirmed Gateway indexer, dependency evidence gossip, and serialized transaction executor configured", config.Name, config.HomeChain.ChainID)
	if config.DirectVerifier.Profile.MeasuredDirectCostGas == nil {
		logger.Printf("mapnode=%s chain_id=%s direct verifier profile=%s is uncalibrated; measured_direct_cost_gas is null", config.Name, config.HomeChain.ChainID, config.DirectVerifier.Profile.ProfileID)
	}
	server := &http.Server{
		Addr:              config.API.Listen,
		Handler:           mapapi.NewHandler(application),
		ReadHeaderTimeout: 5 * time.Second,
	}
	workerContext, cancelWorker := context.WithCancel(context.Background())
	workerCount := 2
	workerDone := make(chan error, 3)
	criticalExit := make(chan error, 1)
	runWorker := func(name string, worker func(context.Context) error) {
		go func() {
			err := worker(workerContext)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Printf("%s stopped: %v", name, err)
			}
			workerDone <- err
			select {
			case criticalExit <- err:
			default:
			}
		}()
	}
	runWorker("confirmed Gateway indexer", application.RunConfirmedIndexer)
	runWorker("serialized request executor", application.RunRequestWorker)
	if config.P2P.Enabled {
		workerCount++
		runWorker("dependency evidence workers", application.RunDependencyEvidenceWorkers)
	}
	defer func() {
		cancelWorker()
		for range workerCount {
			<-workerDone
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		cancelWorker()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	go func() {
		select {
		case <-workerContext.Done():
			return
		case <-criticalExit:
			cancelWorker()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		}
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Printf("HTTP server failed: %v", err)
		return 1
	}
	return 0
}
