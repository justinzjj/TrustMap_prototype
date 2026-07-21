package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/justinzjj/TrustMap_prototype/Mapnode/bootstrap"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(arguments []string, stderr io.Writer) int {
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
	err = bootstrap.ValidateRuntime(runtimeContext, config, manifest)
	cancelRuntimeValidation()
	if err != nil {
		fmt.Fprintf(stderr, "mapnode runtime validation failed: %v\n", err)
		return 1
	}

	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	logger.Printf("mapnode=%s chain_id=%s phase2 bootstrap; chain indexing/p2p enabled in later phases", config.Name, config.HomeChain.ChainID)
	if config.DirectVerifier.Profile.MeasuredDirectCostGas == nil {
		logger.Printf("mapnode=%s chain_id=%s direct verifier profile=%s is uncalibrated; measured_direct_cost_gas is null", config.Name, config.HomeChain.ChainID, config.DirectVerifier.Profile.ProfileID)
	}
	server := &http.Server{
		Addr:              config.API.Listen,
		Handler:           bootstrap.NewHealthHandler(true),
		ReadHeaderTimeout: 5 * time.Second,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Printf("HTTP server failed: %v", err)
		return 1
	}
	return 0
}
