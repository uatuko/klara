package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	addr     = flag.String("addr", ":50051", "tcp address to listen on")
	debug    = flag.Bool("debug", false, "enable debug logs")
	name     = flag.String("name", "mta.local", "fqdn or address literal to identify the mta")
	spaceId  = flag.String("space-id", "mail", "restrict api access to actors within this space")
	tknzAddr = flag.String("tknz-addr", "", "tknz grpc address")
)

func _init() error {
	zerolog.LevelFieldName = "severity"
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Logger = log.Output(zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "15:04:05.000000",
		})
	}

	return nil
}

func main() {
	flag.Parse()

	if err := _init(); err != nil {
		log.Fatal().Err(err).Msg("initialisation failed")
	}

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen")
	}

	var opts []grpc.DialOption
	if strings.HasPrefix(*tknzAddr, "localhost:") {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	}

	tknzConn, err := grpc.NewClient(*tknzAddr, opts...)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise tknz connection")
	}

	srv := http.Server{
		Handler: h2c.NewHandler(newHandler(tknzConn), &http2.Server{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	context.AfterFunc(ctx, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		log.Info().Msg("stopping mta")
		if err := srv.Shutdown(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to stop mta gracefully")
		}
	})

	log.Debug().Str("addr", l.Addr().String()).Msg("starting mta")
	if err = srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error().Err(err).Msg("mta failed")
	}
}
