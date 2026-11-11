package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/uatuko/go-message/mail"
	"github.com/uatuko/go-smtp"
	"google.golang.org/grpc"

	"go.klara.mx/internal/pb"
	"go.klara.mx/internal/srv"
)

var (
	errMxUnavailable = srv.NewErrorf(srv.ErrUnavailable, "no server accepted mail")
	errNoMxRecords   = srv.NewErrorf(srv.ErrInvalidData, "no mx records found")

	resolver net.Resolver
)

type mta struct {
	pb.UnimplementedMtaServer
}

func (m *mta) Transfer(ctx context.Context, req *pb.MtaTransferRequest) (*pb.MtaTransferResponse, error) {
	rpath := req.GetReturnPath()
	if rpath == "" {
		return nil, srv.NewErrorf(srv.ErrInvalidData, "missing return path")
	}

	addr, err := mail.ParseAddress(rpath)
	if err != nil {
		return nil, srv.NewErrorf(srv.ErrInvalidData, "invalid return path")
	}
	rpath = addr.Address

	reqRcpts := req.GetRecipients()
	if len(reqRcpts) == 0 {
		return nil, srv.NewErrorf(srv.ErrInvalidData, "missing recipients")
	}

	var domain string
	rcpts := make([]string, 0, len(reqRcpts))
	for _, rcpt := range req.GetRecipients() {
		addr, err = mail.ParseAddress(rcpt)
		if err != nil {
			return nil, srv.NewErrorf(srv.ErrInvalidData, "invalid recipient(s)")
		}

		_, name, _ := strings.Cut(addr.Address, "@")
		if !isDomainName(name) {
			return nil, srv.NewErrorf(srv.ErrInvalidData, "invalid recipient(s)")
		}

		if domain == "" {
			domain = name
		} else if domain != name {
			return nil, srv.NewErrorf(srv.ErrInvalidData, "recipients from multiple domains")
		}

		rcpts = append(rcpts, addr.Address)
	}

	data := req.GetData()
	if data == nil {
		return nil, srv.NewErrorf(srv.ErrInvalidData, "missing mail data")
	}

	if err = send(ctx, domain, rpath, rcpts, data); err != nil {
		return nil, err
	}

	return &pb.MtaTransferResponse{}, nil
}

func authzInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	sId := ctx.Value(srv.CtxSpaceIdKey{}).(string)
	if sId != *spaceId {
		log := zerolog.Ctx(ctx)
		log.Warn().
			Str("space-id", sId).
			Msg("actor outside of allowed space")

		return nil, srv.NewErrorf(srv.ErrPermissionDenied, "operation not allowed")
	}

	return handler(ctx, req)
}

func newHandler(tknzConn *grpc.ClientConn) http.Handler {
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			srv.UnaryLoggerInterceptor,
			srv.UnaryErrorInterceptor,
			srv.NewAuthnInterceptor(tknzConn),
			authzInterceptor,
		),
	)
	pb.RegisterMtaServer(grpcServer, &mta{})

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := srv.NewResponseWriter(w)

		defer func() {
			log.Info().
				Str("protocol", r.Proto).
				Str("method", r.Method).
				Str("uri", r.URL.RequestURI()).
				Str("user_agent", r.UserAgent()).
				Str("referrer", r.Referer()).
				Uint32("status", rw.Status()).
				Uint64("response_size", rw.Size()).
				Dur("latency", time.Since(start)).
				Msg("http request")
		}()

		r = r.WithContext(log.Logger.WithContext(r.Context()))

		// Serve gRPC
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(rw, r)
			return
		}

		// Serve http
		mux.ServeHTTP(rw, r)
	})
}

func send(ctx context.Context, domain string, from string, to []string, data []byte) error {
	log := zerolog.Ctx(ctx)
	log.Info().
		Str("domain", domain).
		Int("n_recipients", len(to)).
		Int("bytes", len(data)).
		Msg("transferring mail")

	mxRecords, err := resolver.LookupMX(ctx, domain)
	if err != nil {
		var e *net.DNSError
		if errors.As(err, &e) && e.IsNotFound {
			// No MX records found for the domain
			return errNoMxRecords
		}

		log.Error().
			Err(err).
			Msg("failed to lookup mx records")

		return srv.NewError(srv.ErrInternal, err)
	}

	var d net.Dialer
	for _, mx := range mxRecords {
		if mx.Host == "." {
			// Null MX (ref: https://www.rfc-editor.org/rfc/rfc7505.html)
			log.Warn().
				Str("domain", domain).
				Msg("domain does not accept any mail")

			break
		}

		addr := fmt.Sprintf("%s:25", mx.Host)
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			log.Error().
				Err(err).
				Str("addr", addr).
				Msg("failed to dial")

			continue
		}

		c, err := smtp.NewClientStartTLSWithLocalName(
			conn,
			&tls.Config{
				ServerName: mx.Host,
			},
			*name,
		)
		if err != nil {
			log.Error().
				Err(err).
				Str("addr", addr).
				Msg("failed to initialise smtp session")

			continue
		}

		err = c.SendMail(from, to, bytes.NewReader(data))
		if err != nil {
			log.Error().
				Err(err).
				Str("addr", addr).
				Msg("failed to transfer mail")

			continue
		}

		// Successfully sent mail
		return nil
	}

	return errMxUnavailable
}
